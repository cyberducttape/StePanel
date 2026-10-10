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
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
	_ "modernc.org/sqlite"
)

func main() {
	webRootFlag := flag.String("webroot", "/var/www", "Web root directory")
	controlPlaneDBFlag := flag.String("control-plane-db", os.Getenv("STEPANEL_CONTROL_PLANE_DB"), "SQLite control-plane database used for fencing")
	recoveryRootFlag := flag.String("recovery-root", os.Getenv("STEPANEL_RECOVERY_ROOT"), "durable recovery journal root")
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

	logger := log.New(os.Stderr, "[stepanel-root] ", log.LstdFlags)

	var fencingDB *sql.DB
	var err error
	if *controlPlaneDBFlag != "" {
		fencingDB, err = sql.Open("sqlite", "file:"+*controlPlaneDBFlag+"?_pragma=busy_timeout(5000)")
		if err != nil {
			logger.Fatalf("failed to open fencing database: %v", err)
		}
		defer fencingDB.Close()
	}
	broker, err := rootbroker.NewBrokerWithFencingDB(*webRootFlag, *recoveryRootFlag, fencingDB, logger)
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
	// maxSocketRequestBytes covers the 64 MiB helper input limit after JSON
	// base64 encoding, plus the surrounding request.
	maxSocketRequestBytes = 96 << 20
)

func serveRequests(broker executor, reader io.Reader, writer io.Writer, logger *log.Logger) {
	// Read requests from stdin, write responses to stdout. Each request and
	// response is a single JSON line.
	decoder := json.NewDecoder(reader)
	encoder := json.NewEncoder(writer)

	for {
		var req rootbroker.Request
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				break
			}
			logger.Printf("decode error: %v", err)
			resp := rootbroker.Response{
				OK:    false,
				Error: fmt.Sprintf("decode error: %v", err),
			}
			_ = encoder.Encode(resp)
			continue
		}
		if err := encoder.Encode(executeRequest(broker, &req, logger)); err != nil {
			logger.Printf("encode error: %v", err)
		}
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
	decoder := json.NewDecoder(io.LimitReader(conn, maxSocketRequestBytes))
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
		// The operation may legitimately outlast the read deadline.
		if err := conn.SetReadDeadline(time.Time{}); err != nil {
			logger.Printf("clear broker socket read deadline: %v", err)
			return
		}
		if !writeSocketResponse(conn, encoder, executeRequest(broker, &req, logger), limits, logger) {
			return
		}
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
