package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"github.com/cyberducttape/StePanel/internal/sqliteutil"
	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "identity-repair" {
		os.Exit(runIdentityRepair(os.Args[2:]))
	}
	webRootFlag := flag.String("webroot", "/var/www", "Web root directory")
	controlPlaneDBFlag := flag.String("control-plane-db", os.Getenv("STEPANEL_CONTROL_PLANE_DB"), "SQLite control-plane database used for fencing")
	recoveryRootFlag := flag.String("recovery-root", os.Getenv("STEPANEL_RECOVERY_ROOT"), "durable recovery journal root")
	snapshotRecoveryRootFlag := flag.String("snapshot-recovery-root", os.Getenv("STEPANEL_SNAPSHOT_RECOVERY_ROOT"), "panel-owned site transaction snapshot root")
	socketFlag := flag.String("socket", "", "serve the broker on a Unix socket instead of stdin/stdout")
	socketGroupFlag := flag.String("socket-group", "", "group allowed to access the Unix socket")
	maxConcurrentFlag := flag.Int("max-concurrent", rootbroker.DefaultMaxConcurrent, "maximum privileged operations executed at once on the Unix socket")
	flag.Parse()

	if *webRootFlag == "" {
		log.Fatal("webroot is required")
	}
	if *controlPlaneDBFlag == "" || !filepath.IsAbs(*controlPlaneDBFlag) {
		log.Fatal("STEPANEL_CONTROL_PLANE_DB must be an absolute path")
	}
	if *recoveryRootFlag == "" {
		// Standalone stdin/stdout use retains the historical root-owned
		// location; the installed service receives STEPANEL_RECOVERY_ROOT.
		*recoveryRootFlag = "/var/lib/stepanel/recovery"
	}
	if !filepath.IsAbs(*recoveryRootFlag) {
		log.Fatal("STEPANEL_RECOVERY_ROOT must be an absolute path")
	}
	if *snapshotRecoveryRootFlag == "" {
		*snapshotRecoveryRootFlag = "/var/www/sites/.stepanel-recovery"
	}
	if !filepath.IsAbs(*snapshotRecoveryRootFlag) {
		log.Fatal("STEPANEL_SNAPSHOT_RECOVERY_ROOT must be an absolute path")
	}

	logger := log.New(os.Stderr, "[stepanel-root] ", log.LstdFlags)

	var fencingDB *sql.DB
	var err error
	if *controlPlaneDBFlag != "" {
		fencingDB, err = openFencingDatabase(*controlPlaneDBFlag)
		if err != nil {
			logger.Fatalf("failed to open fencing database: %v", err)
		}
		fencingDB.SetMaxOpenConns(1)
		if err := sqliteutil.SetDatabasePageLimit(fencingDB, sqliteutil.DefaultDatabaseMaxBytes); err != nil {
			logger.Fatalf("apply control-plane database growth limit: %v", err)
		}
		defer fencingDB.Close()
	}
	broker, err := rootbroker.NewBrokerWithFencingDBAndSnapshotRoot(*webRootFlag, *recoveryRootFlag, *snapshotRecoveryRootFlag, fencingDB, logger)
	if err != nil {
		logger.Fatalf("failed to create broker: %v", err)
	}

	if *socketFlag != "" {
		scheduler, err := rootbroker.NewScheduler(broker, *maxConcurrentFlag)
		if err != nil {
			logger.Fatalf("failed to create broker scheduler: %v", err)
		}
		if err := serveSocket(scheduler, *socketFlag, *socketGroupFlag, logger); err != nil {
			logger.Fatal(err)
		}
		return
	}

	serveRequests(broker, os.Stdin, os.Stdout, logger)
}

// openFencingDatabase requires a database initialized by the panel service.
// Opening with mode=rw prevents the privileged broker from creating a root-
// owned file ahead of the stepanel service on fresh installations.
func openFencingDatabase(path string) (*sql.DB, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return nil, fmt.Errorf("fencing database path must be absolute")
	}
	u := &url.URL{Scheme: "file", Path: filepath.Clean(path)}
	query := url.Values{}
	query.Set("mode", "rw")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", rootbroker.FencingDatabaseBusyTimeout.Milliseconds()))
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping existing fencing database: %w", err)
	}
	if err := validateFencingDatabaseSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("validate existing fencing database: %w", err)
	}
	return db, nil
}

// validateFencingDatabaseSchema is deliberately read-only: the privileged
// broker must refuse an unrelated, stale, or partially initialized database,
// never create or migrate its own fencing state. The panel initializer
// creates resource_locks before the service is started.
type fencingColumnSpec struct {
	typeName   string
	primaryKey bool
}

func validateFencingDatabaseSchema(db *sql.DB) error {
	if db == nil {
		return errors.New("fencing database is required")
	}
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM control_plane_migrations`).Scan(&version); err != nil {
		return fmt.Errorf("read control-plane migration ledger: %w", err)
	}
	if version == 0 {
		return errors.New("control-plane migration ledger is empty")
	}
	for table, expected := range map[string]map[string]fencingColumnSpec{
		"resource_locks": {
			"resource_key": {typeName: "TEXT", primaryKey: true},
			"owner_id":     {typeName: "TEXT"},
			"lease_until":  {typeName: "INTEGER"},
			"generation":   {typeName: "INTEGER"},
			"acquired_at":  {typeName: "INTEGER"},
		},
		"site_identities": {
			"site":       {typeName: "TEXT", primaryKey: true},
			"username":   {typeName: "TEXT"},
			"created_at": {typeName: "INTEGER"},
		},
		"tenant_sites": {
			"site":       {typeName: "TEXT", primaryKey: true},
			"username":   {typeName: "TEXT"},
			"updated_at": {typeName: "INTEGER"},
		},
	} {
		if err := validateSQLiteTableColumns(db, table, expected); err != nil {
			return err
		}
	}
	return validateSQLiteUniqueColumnIndex(db, "site_identities", "username")
}

func validateSQLiteTableColumns(db *sql.DB, table string, expected map[string]fencingColumnSpec) error {
	// Table names are constants supplied by the schema contract above, not
	// request data. PRAGMA table_info has no bind-parameter form.
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return fmt.Errorf("inspect %s schema: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("read %s schema: %w", table, err)
		}
		if spec, required := expected[name]; required {
			if !strings.EqualFold(strings.TrimSpace(columnType), spec.typeName) {
				return fmt.Errorf("%s.%s has type %q, want %s", table, name, columnType, spec.typeName)
			}
			if spec.primaryKey && primaryKey == 0 {
				return fmt.Errorf("%s.%s is not a primary key", table, name)
			}
			delete(expected, name)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate %s schema: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close %s schema: %w", table, err)
	}
	if len(expected) != 0 {
		missing := make([]string, 0, len(expected))
		for column := range expected {
			missing = append(missing, column)
		}
		sort.Strings(missing)
		return fmt.Errorf("%s is missing required columns: %s", table, strings.Join(missing, ", "))
	}
	return nil
}

func validateSQLiteUniqueColumnIndex(db *sql.DB, table, column string) error {
	// Table names are fixed by validateFencingDatabaseSchema.
	rows, err := db.Query("PRAGMA index_list(" + table + ")")
	if err != nil {
		return fmt.Errorf("inspect %s indexes: %w", table, err)
	}
	type index struct {
		name string
	}
	var indexes []index
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return fmt.Errorf("read %s indexes: %w", table, err)
		}
		if unique == 1 && partial == 0 {
			indexes = append(indexes, index{name: name})
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate %s indexes: %w", table, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close %s indexes: %w", table, err)
	}
	for _, candidate := range indexes {
		quoted := strings.ReplaceAll(candidate.name, "'", "''")
		indexRows, err := db.Query("PRAGMA index_info('" + quoted + "')")
		if err != nil {
			return fmt.Errorf("inspect %s unique index: %w", table, err)
		}
		columns := make([]string, 0, 1)
		for indexRows.Next() {
			var sequence, cid int
			var name sql.NullString
			if err := indexRows.Scan(&sequence, &cid, &name); err != nil {
				_ = indexRows.Close()
				return fmt.Errorf("read %s unique index: %w", table, err)
			}
			if !name.Valid {
				columns = nil
				break
			}
			columns = append(columns, name.String)
		}
		if err := indexRows.Err(); err != nil {
			_ = indexRows.Close()
			return fmt.Errorf("iterate %s unique index: %w", table, err)
		}
		if err := indexRows.Close(); err != nil {
			return fmt.Errorf("close %s unique index: %w", table, err)
		}
		if len(columns) == 1 && columns[0] == column {
			return nil
		}
	}
	return fmt.Errorf("%s.%s has no non-partial unique index", table, column)
}

// executor runs one decoded broker request. The subprocess transport uses the
// broker directly; the socket transport goes through the scheduler.
type executor interface {
	Execute(ctx context.Context, req *rootbroker.Request) (*rootbroker.Response, error)
}

// connLimits bounds socket I/O. An authorized peer that connects and stalls,
// or never reads its response, must not hold broker resources indefinitely.
type connLimits struct {
	read  time.Duration
	write time.Duration
}

var defaultConnLimits = connLimits{read: 30 * time.Second, write: 30 * time.Second}

const (
	// maxSocketConnections bounds concurrently served connections. Execution
	// itself is bounded separately by the scheduler.
	maxSocketConnections = 64
	// maxRequestBytes bounds one encoded request on either transport. Bulk
	// data (database dumps) is passed as a validated file path the broker
	// streams from, never inline; the largest inline payload is the bounded
	// legacy restore-dump body (rootbroker.MaxInlineDumpBytes) after base64.
	maxRequestBytes = 8 << 20
	// maxBufferedRequestBytes is the broker-wide budget for request bytes
	// read but not yet released, across all connections. Connection count
	// alone would allow maxSocketConnections * maxRequestBytes of buffered
	// input in a privileged process.
	maxBufferedRequestBytes = 64 << 20
)

var (
	errRequestTooLarge        = fmt.Errorf("request exceeds the %d-byte broker request limit", maxRequestBytes)
	errRequestBudgetExhausted = errors.New("broker request memory budget is exhausted; retry the operation")
)

// requestBudget accounts for request bytes held by all connections.
type requestBudget struct {
	used  atomic.Int64
	limit int64
}

var brokerRequestBudget = &requestBudget{limit: maxBufferedRequestBytes}

func (b *requestBudget) reserve(n int64) bool {
	for {
		used := b.used.Load()
		if used+n > b.limit {
			return false
		}
		if b.used.CompareAndSwap(used, used+n) {
			return true
		}
	}
}

func (b *requestBudget) release(n int64) {
	b.used.Add(-n)
}

// budgetedReader enforces the per-request limit and charges every byte read
// to the shared budget until the request it belongs to has been served.
// The JSON decoder may read ahead into the next request; those bytes stay
// charged to the connection until it is released.
type budgetedReader struct {
	reader  io.Reader
	budget  *requestBudget
	limit   int64
	read    int64 // bytes read for the current request
	charged int64 // bytes charged to the budget
}

func newBudgetedReader(reader io.Reader, budget *requestBudget, limit int64) *budgetedReader {
	return &budgetedReader{reader: reader, budget: budget, limit: limit}
}

func (r *budgetedReader) Read(p []byte) (int, error) {
	remaining := r.limit - r.read
	if remaining <= 0 {
		return 0, errRequestTooLarge
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	want := int64(len(p))
	if !r.budget.reserve(want) {
		return 0, errRequestBudgetExhausted
	}
	n, err := r.reader.Read(p)
	r.budget.release(want - int64(n))
	r.read += int64(n)
	r.charged += int64(n)
	return n, err
}

// nextRequest releases the budget held by the request just served and
// starts the per-request limit again.
func (r *budgetedReader) nextRequest() {
	r.budget.release(r.charged)
	r.read, r.charged = 0, 0
}

func serveRequests(broker executor, reader io.Reader, writer io.Writer, logger *log.Logger) {
	// Read requests from stdin, write responses to stdout. Each request and
	// response is a single JSON line.
	budgeted := newBudgetedReader(reader, brokerRequestBudget, maxRequestBytes)
	defer budgeted.nextRequest()
	decoder := json.NewDecoder(budgeted)
	encoder := json.NewEncoder(writer)

	for {
		var req rootbroker.Request
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				break
			}
			if errors.Is(err, errRequestTooLarge) || errors.Is(err, errRequestBudgetExhausted) {
				// There is no resynchronization point inside an oversized
				// request; answer once and stop.
				logger.Printf("decode error: %v", err)
				_ = encoder.Encode(rootbroker.Response{OK: false, Error: err.Error()})
				return
			}
			logger.Printf("decode error: %v", err)
			resp := rootbroker.Response{
				OK:    false,
				Error: fmt.Sprintf("decode error: %v", err),
			}
			_ = encoder.Encode(resp)
			continue
		}
		attachFilesStream(&req, decoder, budgeted)
		if err := encoder.Encode(executeRequest(broker, &req, logger)); err != nil {
			logger.Printf("encode error: %v", err)
		}
		if req.Files != nil && req.Files.StreamContent {
			return // a raw body owns the connection until its final frame
		}
		budgeted.nextRequest()
	}
}

// executeRequest bounds each operation. Long-running typed operations have
// explicit budgets; the client context remains an independent, often shorter
// cap.
func executeRequest(broker executor, req *rootbroker.Request, logger *log.Logger) *rootbroker.Response {
	ctx, cancel := context.WithTimeout(context.Background(), rootbroker.RequestTimeout(req))
	defer cancel()
	resp, err := broker.Execute(ctx, req)
	if err != nil {
		logger.Printf("execute error: %v", err)
		return &rootbroker.Response{
			OK:    false,
			Error: fmt.Sprintf("execute error: %v", err),
		}
	}
	return resp
}

// serveConn serves one socket connection with bounded reads and writes. Unlike
// the subprocess transport, a malformed request closes the connection: there
// is no reliable resynchronization point in a byte stream from a misbehaving
// peer.
func serveConn(broker executor, conn net.Conn, limits connLimits, logger *log.Logger) {
	defer conn.Close()
	budgeted := newBudgetedReader(conn, brokerRequestBudget, maxRequestBytes)
	defer budgeted.nextRequest()
	decoder := json.NewDecoder(budgeted)
	encoder := json.NewEncoder(conn)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(limits.read)); err != nil {
			logger.Printf("set broker socket read deadline: %v", err)
			return
		}
		var req rootbroker.Request
		if err := decoder.Decode(&req); err != nil {
			var netErr net.Error
			switch {
			case errors.Is(err, io.EOF):
			case errors.As(err, &netErr) && netErr.Timeout():
				logger.Printf("closing idle broker socket connection")
			default:
				logger.Printf("decode error: %v", err)
				writeSocketResponse(conn, encoder, &rootbroker.Response{OK: false, Error: fmt.Sprintf("decode error: %v", err)}, limits, logger)
			}
			return
		}
		attachFilesStream(&req, decoder, budgeted)
		// Bound a streamed upload independently. Ordinary broker operations
		// can outlast their initial request read deadline.
		if req.Files != nil && req.Files.StreamContent {
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Minute)); err != nil {
				logger.Printf("set streamed broker body deadline: %v", err)
				return
			}
		} else if err := conn.SetReadDeadline(time.Time{}); err != nil {
			logger.Printf("clear broker socket read deadline: %v", err)
			return
		}
		if !writeSocketResponse(conn, encoder, executeRequest(broker, &req, logger), limits, logger) {
			return
		}
		if req.Files != nil && req.Files.StreamContent {
			return // do not parse any bytes after a streamed request as JSON
		}
		budgeted.nextRequest()
	}
}

func attachFilesStream(req *rootbroker.Request, decoder *json.Decoder, reader io.Reader) {
	if req != nil && req.Files != nil && req.Files.StreamContent {
		// JSON decoding can read ahead into the raw framed body. Preserve those
		// bytes before continuing through the budgeted connection reader.
		req.Files.ContentReader = rootbroker.NewFileContentReader(io.MultiReader(decoder.Buffered(), reader))
	}
}

func writeSocketResponse(conn net.Conn, encoder *json.Encoder, resp *rootbroker.Response, limits connLimits, logger *log.Logger) bool {
	if err := conn.SetWriteDeadline(time.Now().Add(limits.write)); err != nil {
		logger.Printf("set broker socket write deadline: %v", err)
		return false
	}
	if err := encoder.Encode(resp); err != nil {
		logger.Printf("encode error: %v", err)
		return false
	}
	return true
}

func serveSocket(broker executor, socketPath, socketGroup string, logger *log.Logger) error {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove existing broker socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on broker socket: %w", err)
	}
	defer listener.Close()
	if err := os.Chmod(socketPath, 0660); err != nil {
		return fmt.Errorf("set broker socket mode: %w", err)
	}
	if socketGroup != "" {
		group, err := user.LookupGroup(socketGroup)
		if err != nil {
			return fmt.Errorf("lookup broker socket group %q: %w", socketGroup, err)
		}
		gid, err := strconv.Atoi(group.Gid)
		if err != nil {
			return fmt.Errorf("parse broker socket group %q: %w", socketGroup, err)
		}
		if err := os.Chown(socketPath, os.Getuid(), gid); err != nil {
			return fmt.Errorf("set broker socket group: %w", err)
		}
	}
	if socketGroup == "" {
		return fmt.Errorf("socket-group is required for broker socket authorization")
	}
	group, err := user.LookupGroup(socketGroup)
	if err != nil {
		return fmt.Errorf("lookup broker socket group %q: %w", socketGroup, err)
	}
	allowedGID, err := strconv.Atoi(group.Gid)
	if err != nil {
		return fmt.Errorf("parse broker socket group %q: %w", socketGroup, err)
	}

	return acceptConnections(listener, broker, func(conn net.Conn) error {
		unixConn, ok := conn.(*net.UnixConn)
		if !ok {
			return fmt.Errorf("not a Unix socket connection")
		}
		return authorizeSocketPeer(unixConn, allowedGID)
	}, logger)
}

// acceptConnections serves each authorized connection on its own goroutine so
// a long privileged operation never blocks unrelated callers or health
// probes. The scheduler bounds actual execution.
func acceptConnections(listener net.Listener, broker executor, authorize func(net.Conn) error, logger *log.Logger) error {
	slots := make(chan struct{}, maxSocketConnections)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept broker socket connection: %w", err)
		}
		if err := authorize(conn); err != nil {
			logger.Printf("rejecting broker socket peer: %v", err)
			_ = conn.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			logger.Printf("rejecting broker socket peer: connection limit reached")
			writeSocketResponse(conn, json.NewEncoder(conn), &rootbroker.Response{OK: false, Error: "root broker connection limit reached"}, defaultConnLimits, logger)
			_ = conn.Close()
			continue
		}
		go func() {
			defer func() { <-slots }()
			serveConn(broker, conn, defaultConnLimits, logger)
		}()
	}
}

func authorizeSocketPeer(conn *net.UnixConn, allowedGID int) error {
	var credential *syscall.Ucred
	var controlErr error
	raw, err := conn.SyscallConn()
	if err != nil {
		return fmt.Errorf("inspect socket peer: %w", err)
	}
	if err := raw.Control(func(fd uintptr) {
		credential, controlErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return fmt.Errorf("inspect socket peer: %w", err)
	}
	if controlErr != nil {
		return fmt.Errorf("read socket peer credentials: %w", controlErr)
	}
	if credential == nil || int(credential.Gid) != allowedGID {
		return fmt.Errorf("peer group is not authorized")
	}
	return nil
}
