package stepanel

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	httputil "github.com/cyberducttape/StePanel/internal/http"
	"github.com/cyberducttape/StePanel/internal/upload"
)

// errUploadLengthRequired reports an archive upload whose size cannot be
// admitted before its body is read: no Content-Length and no configured
// STEPANEL_MAX_UPLOAD_BYTES ceiling to reserve instead.
var errUploadLengthRequired = errors.New("archive uploads require a Content-Length header")

// uploadIdleTimeout bounds how long an admitted upload may receive no body
// bytes. A stalled client then releases its upload slot and capacity
// reservation instead of holding them until the 60-minute upload ceiling.
var uploadIdleTimeout = 2 * time.Minute

var capacityReservationSequence uint64

// capacityError reports that a filesystem cannot hold a workflow's demands
// plus the STEPANEL_MIN_FREE_BYTES reserve.
type capacityError struct{ message string }

func (e *capacityError) Error() string { return e.message }

// capacityDemand is the number of bytes a workflow will consume on the
// filesystem that holds Path.
type capacityDemand struct {
	Path  string
	Bytes uint64
}

type deviceDemand struct {
	device uint64
	path   string
	bytes  uint64
}

// groupDemands sums demands per filesystem, so an import root and a site
// root on the same device are charged together.
func groupDemands(workflow string, demands []capacityDemand) ([]deviceDemand, error) {
	var grouped []deviceDemand
	index := map[uint64]int{}
	for _, demand := range demands {
		device, err := filesystemDevice(demand.Path)
		if err != nil {
			return nil, fmt.Errorf("inspect %s capacity at %s: %w", workflow, demand.Path, err)
		}
		i, seen := index[device]
		if !seen {
			i = len(grouped)
			index[device] = i
			grouped = append(grouped, deviceDemand{device: device, path: demand.Path})
		}
		if demand.Bytes > ^uint64(0)-grouped[i].bytes {
			return nil, fmt.Errorf("%s capacity estimate overflow", workflow)
		}
		grouped[i].bytes += demand.Bytes
	}
	return grouped, nil
}

func filesystemDevice(path string) (uint64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("filesystem identity unavailable")
	}
	return uint64(stat.Dev), nil
}

// capacityLedger records free space promised to admitted uploads but not yet
// written. Admission charges outstanding promises against free space, so
// concurrent uploads cannot each claim the same bytes. The zero value is
// ready to use.
type capacityLedger struct {
	mu   sync.Mutex
	held map[uint64]uint64
	db   *sql.DB
}

// capacityReservation is one upload's outstanding promise.
type capacityReservation struct {
	ledger       *capacityLedger
	workflow     string
	reserve      uint64
	held         map[uint64]uint64
	stagingPath  string
	stagingDev   uint64
	consumedSize uint64
	id           string

	// An upload without Content-Length is admitted progressively: it holds
	// demandsFor(admitted) and extends by step before the written size
	// reaches admitted, up to limit. See reserveProgressive.
	progressive bool
	admitted    uint64
	limit       uint64
	step        uint64
	demandsFor  func(uint64) []capacityDemand
}

func newCapacityLedger(db *sql.DB) (*capacityLedger, error) {
	if db == nil {
		return &capacityLedger{}, nil
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS capacity_reservations (
		reservation_id TEXT PRIMARY KEY,
		device INTEGER NOT NULL,
		bytes INTEGER NOT NULL,
		workflow TEXT NOT NULL,
		staging_path TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS capacity_reservations_device_idx ON capacity_reservations(device);`); err != nil {
		return nil, fmt.Errorf("create capacity reservation ledger: %w", err)
	}
	return &capacityLedger{db: db}, nil
}

func capacitySQLBytes(value uint64) (int64, error) {
	if value > uint64(^uint64(0)>>1) {
		return 0, errors.New("capacity reservation exceeds SQLite integer range")
	}
	return int64(value), nil
}

func capacityReservationID() string {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err == nil {
		return "cap-" + hex.EncodeToString(entropy[:])
	}
	// Entropy failure must not turn capacity admission into a reservation-ID
	// collision. The process-local sequence covers concurrent callers while
	// the timestamp keeps fallback IDs distinct across process starts.
	sequence := atomic.AddUint64(&capacityReservationSequence, 1)
	return fmt.Sprintf("cap-%d-%d", time.Now().UnixNano(), sequence)
}

func (l *capacityLedger) durableHeld(tx *sql.Tx, device uint64) (uint64, error) {
	var held sql.NullInt64
	if err := tx.QueryRow(`SELECT SUM(bytes) FROM capacity_reservations WHERE device=?`, device).Scan(&held); err != nil {
		return 0, err
	}
	if !held.Valid || held.Int64 < 0 {
		return 0, nil
	}
	return uint64(held.Int64), nil
}

// progressiveUploadStep is how far ahead of the bytes written an upload of
// unknown length is admitted. Each extension is checked against current free
// space and every other outstanding reservation.
var progressiveUploadStep uint64 = 256 << 20

// reserveProgressive admits an upload of unknown length one step at a time
// instead of reserving the whole upload ceiling up front, which would refuse
// a small streamed upload whenever the ceiling's worth of space is not free.
func (l *capacityLedger) reserveProgressive(cfg Config, workflow, stagingPath string, limit uint64, demandsFor func(uint64) []capacityDemand) (*capacityReservation, error) {
	first := min(progressiveUploadStep, limit)
	reservation, err := l.reserve(cfg, workflow, stagingPath, demandsFor(first))
	if err != nil {
		return nil, err
	}
	reservation.progressive = true
	reservation.admitted = first
	reservation.limit = limit
	reservation.step = progressiveUploadStep
	reservation.demandsFor = demandsFor
	return reservation, nil
}

// extendLocked adds demands to the reservation if every filesystem can absorb
// them on top of all outstanding holds and the reserve. The caller holds l.mu.
func (r *capacityReservation) extendLocked(demands []capacityDemand) error {
	grouped, err := groupDemands(r.workflow, demands)
	if err != nil {
		return err
	}
	l := r.ledger
	if l.db != nil {
		tx, txErr := l.db.Begin()
		if txErr != nil {
			return fmt.Errorf("begin capacity extension: %w", txErr)
		}
		rollback := true
		defer func() {
			if rollback {
				_ = tx.Rollback()
			}
		}()
		if _, txErr = tx.Exec(`INSERT INTO state_blobs(name,payload,updated_at,revision) VALUES('capacity-ledger-lock',X'',unixepoch(),0) ON CONFLICT(name) DO UPDATE SET updated_at=excluded.updated_at`); txErr != nil {
			return fmt.Errorf("lock capacity ledger extension: %w", txErr)
		}
		for _, demand := range grouped {
			outstanding, readErr := l.durableHeld(tx, demand.device)
			if readErr != nil {
				return fmt.Errorf("read capacity before extension: %w", readErr)
			}
			free, statErr := availableBytes(demand.path)
			if statErr != nil {
				return fmt.Errorf("inspect %s capacity at %s: %w", r.workflow, demand.path, statErr)
			}
			if required := saturatingAdd(saturatingAdd(outstanding, demand.bytes), r.reserve); free < required {
				return &capacityError{fmt.Sprintf("insufficient free space at %s to continue this %s: %d bytes available, %d more needed beyond %d already reserved", demand.path, r.workflow, free, demand.bytes, outstanding)}
			}
		}
		for _, demand := range grouped {
			bytes, convErr := capacitySQLBytes(demand.bytes)
			if convErr != nil {
				return convErr
			}
			if _, txErr = tx.Exec(`UPDATE capacity_reservations SET bytes=bytes+? WHERE reservation_id=? AND device=?`, bytes, r.id, demand.device); txErr != nil {
				return fmt.Errorf("extend capacity reservation: %w", txErr)
			}
			r.held[demand.device] += demand.bytes
		}
		if txErr = tx.Commit(); txErr != nil {
			return fmt.Errorf("commit capacity extension: %w", txErr)
		}
		rollback = false
		return nil
	}
	for _, demand := range grouped {
		free, err := availableBytes(demand.path)
		if err != nil {
			return fmt.Errorf("inspect %s capacity at %s: %w", r.workflow, demand.path, err)
		}
		if required := saturatingAdd(saturatingAdd(l.held[demand.device], demand.bytes), r.reserve); free < required {
			return &capacityError{fmt.Sprintf("insufficient free space at %s to continue this %s: %d bytes available, %d more needed beyond %d already reserved", demand.path, r.workflow, free, demand.bytes, l.held[demand.device])}
		}
	}
	for _, demand := range grouped {
		l.held[demand.device] += demand.bytes
		r.held[demand.device] += demand.bytes
		if l.db != nil && r.id != "" {
			bytes, convErr := capacitySQLBytes(demand.bytes)
			if convErr != nil {
				return convErr
			}
			if _, dbErr := l.db.Exec(`UPDATE capacity_reservations SET bytes=bytes+? WHERE reservation_id=? AND device=?`, bytes, r.id, demand.device); dbErr != nil {
				return fmt.Errorf("extend capacity reservation: %w", dbErr)
			}
		}
	}
	return nil
}

// check verifies that every filesystem can absorb the demands on top of
// outstanding reservations and the configured reserve, without holding
// anything. Durable jobs use it: their archive is already on disk.
func (l *capacityLedger) check(cfg Config, workflow string, demands []capacityDemand) error {
	_, err := l.admit(cfg, workflow, demands, false)
	return err
}

// reserve is check plus a hold that lasts until release. stagingPath names
// the filesystem the upload is streamed into; consume shrinks the hold there
// as bytes land.
func (l *capacityLedger) reserve(cfg Config, workflow, stagingPath string, demands []capacityDemand) (*capacityReservation, error) {
	reservation, err := l.admit(cfg, workflow, demands, true)
	if err != nil {
		return nil, err
	}
	device, err := filesystemDevice(stagingPath)
	if err != nil {
		reservation.release()
		return nil, fmt.Errorf("inspect %s staging filesystem: %w", workflow, err)
	}
	reservation.stagingPath, reservation.stagingDev = stagingPath, device
	return reservation, nil
}

func (l *capacityLedger) admit(cfg Config, workflow string, demands []capacityDemand, hold bool) (*capacityReservation, error) {
	grouped, err := groupDemands(workflow, demands)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db != nil {
		return l.admitDurable(cfg, workflow, grouped, hold)
	}
	for _, demand := range grouped {
		outstanding := l.held[demand.device]
		if demand.bytes > ^uint64(0)-cfg.MinFreeBytes || demand.bytes+cfg.MinFreeBytes > ^uint64(0)-outstanding {
			return nil, fmt.Errorf("%s capacity estimate overflow", workflow)
		}
		required := demand.bytes + cfg.MinFreeBytes
		free, err := availableBytes(demand.path)
		if err != nil {
			return nil, fmt.Errorf("inspect %s capacity at %s: %w", workflow, demand.path, err)
		}
		if free < required+outstanding {
			message := fmt.Sprintf("insufficient free space at %s: %d bytes available, %d required for this %s", demand.path, free, required, workflow)
			if outstanding > 0 {
				message += fmt.Sprintf(" plus %d reserved by in-progress uploads", outstanding)
			}
			return nil, &capacityError{message}
		}
	}
	reservation := &capacityReservation{ledger: l, workflow: workflow, reserve: cfg.MinFreeBytes, held: map[uint64]uint64{}}
	if hold {
		if l.held == nil {
			l.held = map[uint64]uint64{}
		}
		for _, demand := range grouped {
			l.held[demand.device] += demand.bytes
			reservation.held[demand.device] = demand.bytes
		}
	}
	return reservation, nil
}

func (l *capacityLedger) admitDurable(cfg Config, workflow string, grouped []deviceDemand, hold bool) (*capacityReservation, error) {
	tx, err := l.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin capacity reservation: %w", err)
	}
	defer tx.Rollback()
	// This write takes SQLite's RESERVED lock before reading the ledger. That
	// makes admission atomic across panel and worker processes.
	if _, err := tx.Exec(`INSERT INTO state_blobs(name,payload,updated_at,revision) VALUES('capacity-ledger-lock',X'',unixepoch(),0) ON CONFLICT(name) DO UPDATE SET updated_at=excluded.updated_at`); err != nil {
		return nil, fmt.Errorf("lock capacity ledger: %w", err)
	}
	for _, demand := range grouped {
		outstanding, err := l.durableHeld(tx, demand.device)
		if err != nil {
			return nil, fmt.Errorf("read capacity reservations: %w", err)
		}
		if demand.bytes > ^uint64(0)-cfg.MinFreeBytes || demand.bytes+cfg.MinFreeBytes > ^uint64(0)-outstanding {
			return nil, fmt.Errorf("%s capacity estimate overflow", workflow)
		}
		free, err := availableBytes(demand.path)
		if err != nil {
			return nil, fmt.Errorf("inspect %s capacity at %s: %w", workflow, demand.path, err)
		}
		if free < demand.bytes+cfg.MinFreeBytes+outstanding {
			message := fmt.Sprintf("insufficient free space at %s: %d bytes available, %d required for this %s", demand.path, free, demand.bytes+cfg.MinFreeBytes, workflow)
			if outstanding > 0 {
				message += fmt.Sprintf(" plus %d reserved by in-progress workflows", outstanding)
			}
			return nil, &capacityError{message}
		}
	}
	reservation := &capacityReservation{ledger: l, workflow: workflow, reserve: cfg.MinFreeBytes, held: map[uint64]uint64{}, id: capacityReservationID()}
	if hold {
		for _, demand := range grouped {
			bytes, err := capacitySQLBytes(demand.bytes)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(`INSERT INTO capacity_reservations(reservation_id,device,bytes,workflow,staging_path,created_at) VALUES(?,?,?,?,?,unixepoch())`, reservation.id, demand.device, bytes, workflow, demand.path); err != nil {
				return nil, fmt.Errorf("persist capacity reservation: %w", err)
			}
			reservation.held[demand.device] = demand.bytes
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit capacity reservation: %w", err)
	}
	return reservation, nil
}

// consume records that the upload has written size bytes in total to its
// staging filesystem, releasing that much of the hold, and verifies that
// free space still covers every outstanding reservation plus the reserve.
// Upload streams call it periodically so a consumer outside the ledger
// cannot fill the disk underneath admitted uploads.
func (r *capacityReservation) consume(size int64) error {
	if r == nil || size < 0 {
		return nil
	}
	l := r.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	written := uint64(size)
	if written > r.consumedSize {
		landed := min(written-r.consumedSize, r.held[r.stagingDev])
		r.held[r.stagingDev] -= landed
		if l.held != nil {
			l.held[r.stagingDev] -= landed
		}
		if l.db != nil && r.id != "" && landed > 0 {
			if bytes, err := capacitySQLBytes(landed); err == nil {
				if _, dbErr := l.db.Exec(`UPDATE capacity_reservations SET bytes=MAX(bytes-?,0) WHERE reservation_id=? AND device=?`, bytes, r.id, r.stagingDev); dbErr != nil {
					return fmt.Errorf("update capacity reservation after write: %w", dbErr)
				}
			} else {
				return err
			}
		}
		r.consumedSize = written
	}
	if r.progressive {
		// Stay at least half a step ahead of the writer.
		for r.admitted < r.limit && saturatingAdd(written, r.step/2) > r.admitted {
			next := min(r.step, r.limit-r.admitted)
			if err := r.extendLocked(r.demandsFor(next)); err != nil {
				return err
			}
			r.admitted += next
		}
		if written > r.admitted {
			return &capacityError{fmt.Sprintf("this %s exceeded its admitted size of %d bytes", r.workflow, r.admitted)}
		}
	}
	free, err := availableBytes(r.stagingPath)
	if err != nil {
		return fmt.Errorf("inspect %s capacity at %s: %w", r.workflow, r.stagingPath, err)
	}
	held := l.held[r.stagingDev]
	if l.db != nil {
		var durable sql.NullInt64
		if err := l.db.QueryRow(`SELECT SUM(bytes) FROM capacity_reservations WHERE device=?`, r.stagingDev).Scan(&durable); err != nil {
			return fmt.Errorf("read capacity after write: %w", err)
		}
		held = 0
		if durable.Valid && durable.Int64 > 0 {
			held = uint64(durable.Int64)
		}
	}
	if required := saturatingAdd(held, r.reserve); free < required {
		return &capacityError{fmt.Sprintf("free space at %s fell to %d bytes during this %s; %d are needed for in-progress uploads and the reserve", r.stagingPath, free, r.workflow, required)}
	}
	return nil
}

// release returns whatever the reservation still holds. It is idempotent.
func (r *capacityReservation) release() {
	if r == nil {
		return
	}
	l := r.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db != nil && r.id != "" {
		_, _ = l.db.Exec(`DELETE FROM capacity_reservations WHERE reservation_id=?`, r.id)
	}
	for device, bytes := range r.held {
		if l.held != nil {
			l.held[device] -= bytes
		}
		delete(r.held, device)
	}
}

// grow adds a newly measured demand to an existing reservation before the
// workflow starts the write that needs it. This is used by backups because
// database dump sizes are not knowable until the helper has produced them.
func (r *capacityReservation) grow(path string, bytes uint64) error {
	if r == nil || bytes == 0 {
		return nil
	}
	device, err := filesystemDevice(path)
	if err != nil {
		return fmt.Errorf("inspect %s capacity at %s: %w", r.workflow, path, err)
	}
	l := r.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db != nil {
		sqlBytes, convErr := capacitySQLBytes(bytes)
		if convErr != nil {
			return convErr
		}
		tx, txErr := l.db.Begin()
		if txErr != nil {
			return fmt.Errorf("begin capacity growth: %w", txErr)
		}
		rollback := true
		defer func() {
			if rollback {
				_ = tx.Rollback()
			}
		}()
		if _, txErr = tx.Exec(`INSERT INTO state_blobs(name,payload,updated_at,revision) VALUES('capacity-ledger-lock',X'',unixepoch(),0) ON CONFLICT(name) DO UPDATE SET updated_at=excluded.updated_at`); txErr != nil {
			return fmt.Errorf("lock capacity ledger growth: %w", txErr)
		}
		var held sql.NullInt64
		if txErr = tx.QueryRow(`SELECT SUM(bytes) FROM capacity_reservations WHERE device=?`, device).Scan(&held); txErr != nil {
			return fmt.Errorf("read capacity before growth: %w", txErr)
		}
		outstanding := uint64(0)
		if held.Valid && held.Int64 > 0 {
			outstanding = uint64(held.Int64)
		}
		free, statErr := availableBytes(path)
		if statErr != nil {
			return fmt.Errorf("inspect %s capacity at %s: %w", r.workflow, path, statErr)
		}
		if free < saturatingAdd(saturatingAdd(outstanding, bytes), r.reserve) {
			return &capacityError{fmt.Sprintf("insufficient free space at %s for an additional %d bytes needed by this %s", path, bytes, r.workflow)}
		}
		if _, txErr = tx.Exec(`UPDATE capacity_reservations SET bytes=bytes+? WHERE reservation_id=? AND device=?`, sqlBytes, r.id, device); txErr != nil {
			return fmt.Errorf("grow capacity reservation: %w", txErr)
		}
		if txErr = tx.Commit(); txErr != nil {
			return fmt.Errorf("commit capacity growth: %w", txErr)
		}
		rollback = false
		r.held[device] += bytes
		return nil
	}
	if bytes > ^uint64(0)-l.held[device] {
		return fmt.Errorf("%s capacity estimate overflow", r.workflow)
	}
	free, err := availableBytes(path)
	if err != nil {
		return fmt.Errorf("inspect %s capacity at %s: %w", r.workflow, path, err)
	}
	if free < saturatingAdd(saturatingAdd(l.held[device], bytes), r.reserve) {
		return &capacityError{fmt.Sprintf("insufficient free space at %s for an additional %d bytes needed by this %s", path, bytes, r.workflow)}
	}
	l.held[device] += bytes
	r.held[device] += bytes
	return nil
}

// shrink releases an overestimate after a staged object has been measured.
// It is deliberately scoped to one reservation so another workflow's hold
// cannot be released accidentally.
func (r *capacityReservation) shrink(path string, bytes uint64) error {
	if r == nil || bytes == 0 {
		return nil
	}
	device, err := filesystemDevice(path)
	if err != nil {
		return fmt.Errorf("inspect %s capacity at %s: %w", r.workflow, path, err)
	}
	l := r.ledger
	l.mu.Lock()
	defer l.mu.Unlock()
	released := min(bytes, r.held[device])
	if released == 0 {
		return nil
	}
	if l.db != nil && r.id != "" {
		sqlBytes, convErr := capacitySQLBytes(released)
		if convErr != nil {
			return convErr
		}
		if _, dbErr := l.db.Exec(`UPDATE capacity_reservations SET bytes=MAX(bytes-?,0) WHERE reservation_id=? AND device=?`, sqlBytes, r.id, device); dbErr != nil {
			return fmt.Errorf("shrink capacity reservation: %w", dbErr)
		}
	}
	r.held[device] -= released
	if l.held != nil {
		l.held[device] -= released
	}
	return nil
}

// heldBytes reports the bytes currently promised on path's filesystem.
func (l *capacityLedger) heldBytes(path string) uint64 {
	device, err := filesystemDevice(path)
	if err != nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db != nil {
		var held sql.NullInt64
		if err := l.db.QueryRow(`SELECT SUM(bytes) FROM capacity_reservations WHERE device=?`, device).Scan(&held); err == nil && held.Valid && held.Int64 > 0 {
			return uint64(held.Int64)
		}
		return 0
	}
	return l.held[device]
}

// admitCapacity preserves the package-level capacity check used by older
// callers. New request paths should use capacityLedger so concurrent upload
// reservations are accounted for.
func admitCapacity(cfg Config, workflow string, demands []capacityDemand) error {
	return (&capacityLedger{}).check(cfg, workflow, demands)
}

// declaredUploadBytes is the archive size admitted before the body is read.
// Content-Length bounds the archive (multipart framing only adds to it); a
// streamed body without one reports known=false with the configured upload
// ceiling as its limit, and is admitted progressively.
func declaredUploadBytes(r *http.Request, maxUpload int64) (size uint64, known bool, err error) {
	switch {
	case r.ContentLength >= 0:
		return uint64(r.ContentLength), true, nil
	case maxUpload > 0:
		return uint64(maxUpload), false, nil
	default:
		return 0, false, errUploadLengthRequired
	}
}

// archiveUploadDemands budgets an archive before its body is accepted: the
// staged archive and the extracted tree in the import root, plus the
// site-manager staging tree beside the canonical sites. WPress archives are
// stored uncompressed, so each tree is about the archive size; a cPanel
// archive's expanded size is unknown until inspection, so the gzip size is a
// lower bound that the post-inspection check later refines.
func archiveUploadDemands(cfg Config, archive uint64) []capacityDemand {
	return []capacityDemand{
		{Path: cfg.ImportRoot, Bytes: saturatingAdd(archive, archive)},
		{Path: filepath.Join(cfg.WebRoot, "sites"), Bytes: archive},
	}
}

// stagedArchiveDemands budgets a durable job whose archive is already on
// disk: only the extracted tree and the site-manager staging tree remain.
func stagedArchiveDemands(cfg Config, expanded uint64) []capacityDemand {
	return []capacityDemand{
		{Path: cfg.ImportRoot, Bytes: expanded},
		{Path: filepath.Join(cfg.WebRoot, "sites"), Bytes: expanded},
	}
}

func saturatingAdd(a, b uint64) uint64 {
	if a > ^uint64(0)-b {
		return ^uint64(0)
	}
	return a + b
}

// uploadSpaceCheck preserves the pre-ledger streaming check for callers that
// need a standalone, non-reserving callback. Upload handlers use
// capacityReservation.consume instead.
func uploadSpaceCheck(cfg Config, declared uint64) func(int64) error {
	return func(written int64) error {
		if written < 0 {
			return fmt.Errorf("invalid upload byte count")
		}
		remaining := uint64(0)
		if uint64(written) < declared {
			remaining = declared - uint64(written)
		}
		return admitCapacity(cfg, "upload", []capacityDemand{{Path: cfg.ImportRoot, Bytes: remaining}})
	}
}

// idleDeadlineBody refreshes the connection read deadline as body bytes
// arrive, so a stalled upload fails after uploadIdleTimeout while an active
// one may run until the request's absolute ceiling.
type idleDeadlineBody struct {
	body        io.ReadCloser
	controller  *http.ResponseController
	idle        time.Duration
	ceiling     time.Time
	nextRefresh time.Time
}

// guardUploadBody installs the idle deadline on r.Body. The ceiling is the
// route's context deadline (the 60-minute upload class).
func guardUploadBody(w http.ResponseWriter, r *http.Request) {
	ceiling, ok := r.Context().Deadline()
	if !ok {
		ceiling = time.Now().Add(httputil.DefaultTimeouts().UploadRead)
	}
	r.Body = &idleDeadlineBody{body: r.Body, controller: http.NewResponseController(w), idle: uploadIdleTimeout, ceiling: ceiling}
}

func (b *idleDeadlineBody) Read(p []byte) (int, error) {
	// Refreshing at most once a second keeps per-read overhead negligible;
	// a stall is therefore detected between idle-1s and idle after the last
	// byte arrived.
	if now := time.Now(); !now.Before(b.nextRefresh) {
		deadline := now.Add(b.idle)
		if deadline.After(b.ceiling) {
			deadline = b.ceiling
		}
		if err := b.controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return 0, fmt.Errorf("set upload read deadline: %w", err)
		}
		b.nextRefresh = now.Add(time.Second)
	}
	return b.body.Read(p)
}

func (b *idleDeadlineBody) Close() error { return b.body.Close() }

// uploadRejection is a client-facing validation failure raised before any
// archive byte is staged.
type uploadRejection struct {
	status  int
	message string
}

func (e *uploadRejection) Error() string { return e.message }

// writeUploadError maps admission and streaming failures to responses and
// records the rejection reason. Unclassified failures are logged with their
// cause; the client receives a generic message.
func writeUploadErrorResponse(w http.ResponseWriter, r *http.Request, err error) int {
	var rejection *uploadRejection
	var capacity *capacityError
	reason := uploadRejectInvalid
	switch {
	case errors.As(err, &rejection):
		http.Error(w, "invalid upload request", rejection.status)
	case errors.As(err, &capacity):
		reason = uploadRejectCapacity
		http.Error(w, capacity.Error(), http.StatusInsufficientStorage)
	case errors.Is(err, errUploadLengthRequired):
		reason = uploadRejectLength
		http.Error(w, "archive uploads require a Content-Length header", http.StatusLengthRequired)
	case errors.Is(err, upload.ErrFileTooLarge):
		reason = uploadRejectTooLarge
		http.Error(w, "upload exceeds the configured size limit", http.StatusRequestEntityTooLarge)
	case errors.Is(err, os.ErrDeadlineExceeded):
		reason = uploadRejectStalled
		http.Error(w, fmt.Sprintf("upload stalled: no data received for %s", uploadIdleTimeout), http.StatusRequestTimeout)
	case errors.Is(err, upload.ErrMissingFile):
		http.Error(w, "backup file is required", http.StatusBadRequest)
	case errors.Is(err, upload.ErrMalformed), errors.Is(err, upload.ErrUnexpectedPart), errors.Is(err, upload.ErrFieldTooLarge), errors.Is(err, http.ErrNotMultipart), errors.Is(err, http.ErrMissingBoundary):
		// Multipart parser errors can include request-controlled headers, field
		// names, or filenames. Keep those details out of the response; the
		// parser's text is not safe to reflect into an HTTP response.
		http.Error(w, "invalid multipart upload", http.StatusBadRequest)
	default:
		reason = uploadRejectInternal
		if r != nil {
			requestID, _ := r.Context().Value(requestIDContextKey{}).(string)
			log.Printf(`{"level":"error","request_id":%q,"path":%q,"error":%q}`, requestID, r.URL.Path, "stage upload: "+err.Error())
		}
		http.Error(w, "could not stage upload", http.StatusInternalServerError)
	}
	return reason
}

// writeUploadError is retained for package-level callers that predate the
// application metrics hook.
func writeUploadError(w http.ResponseWriter, err error) {
	writeUploadErrorResponse(w, nil, err)
}

func (a *App) writeUploadError(w http.ResponseWriter, r *http.Request, err error) {
	reason := writeUploadErrorResponse(w, r, err)
	a.Metrics.ObserveUploadRejected(reason)
}

// stageArchiveUpload admits and streams the "backup" file part of r into the
// object opts.Create opens. It reserves capacity for the declared archive
// size before reading the body, refreshes an idle read deadline while
// streaming, and charges written bytes against the reservation. On failure
// it has already written the response. On success the caller must release
// the reservation once the upload no longer needs its hold.
func (a *App) stageArchiveUpload(w http.ResponseWriter, r *http.Request, workflow string, opts upload.Options) (*upload.Result, *capacityReservation, bool) {
	if err := os.MkdirAll(a.Config.ImportRoot, 0700); err != nil {
		a.writeUploadError(w, r, fmt.Errorf("prepare upload storage: %w", err))
		return nil, nil, false
	}
	declared, known, err := declaredUploadBytes(r, a.Config.MaxUpload)
	var reservation *capacityReservation
	if err == nil {
		if known {
			reservation, err = a.capacity.reserve(a.Config, workflow, a.Config.ImportRoot, archiveUploadDemands(a.Config, declared))
		} else {
			demandsFor := func(archive uint64) []capacityDemand { return archiveUploadDemands(a.Config, archive) }
			reservation, err = a.capacity.reserveProgressive(a.Config, workflow, a.Config.ImportRoot, declared, demandsFor)
		}
	}
	if err != nil {
		a.writeUploadError(w, r, err)
		return nil, nil, false
	}
	guardUploadBody(w, r)
	reader, err := r.MultipartReader()
	if err != nil {
		reservation.release()
		a.writeUploadError(w, r, err)
		return nil, nil, false
	}
	opts.FileField = "backup"
	opts.MaxFileBytes = a.Config.MaxUpload
	opts.CheckSpace = reservation.consume
	staged, err := upload.Stream(reader, opts)
	if err != nil {
		reservation.release()
		a.writeUploadError(w, r, err)
		return nil, nil, false
	}
	a.Metrics.ObserveUploadStaged(staged.Size)
	return staged, reservation, true
}

// databaseDataDir names the filesystem a local database server stores its
// data in, or "" for a remote server or an unknown layout (in which case no
// database capacity is reserved). STEPANEL_DB_DATA_DIR overrides it.
func databaseDataDir(cfg Config) string {
	if cfg.DBDataDir != "" {
		return cfg.DBDataDir
	}
	host := strings.TrimSpace(cfg.DBHost)
	if host != "" && host != "localhost" && host != "127.0.0.1" && host != "::1" && !strings.HasPrefix(host, "localhost:") && !strings.HasPrefix(host, "/") {
		return ""
	}
	candidates := []string{"/var/lib/mysql"}
	if strings.EqualFold(cfg.DBEngine, "postgresql") {
		candidates = []string{"/var/lib/pgsql", "/var/lib/postgresql"}
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

// checkCPMoveCapacity admits a cPanel restore whose archive is already
// staged: the inspected expanded tree in the import root, the site-manager
// staging tree, and -- when databases are restored into a local server --
// their growth in its data directory, on top of in-progress reservations.
func (a *App) checkCPMoveCapacity(expandedBytes, databaseBytes int64, restoreDatabases bool) error {
	if expandedBytes < 0 || databaseBytes < 0 {
		return errors.New("invalid cpmove size estimate")
	}
	demands := stagedArchiveDemands(a.Config, uint64(expandedBytes))
	if dir := databaseDataDir(a.Config); restoreDatabases && databaseBytes > 0 && dir != "" {
		// A restored dump can occupy about twice its size in the data
		// directory once indexes and logs are counted.
		demands = append(demands, capacityDemand{Path: dir, Bytes: saturatingAdd(uint64(databaseBytes), uint64(databaseBytes))})
	}
	return a.capacity.check(a.Config, "cpmove", demands)
}
