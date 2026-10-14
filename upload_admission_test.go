package stepanel

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/upload"
)

func uploadTestConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	cfg := Config{ImportRoot: filepath.Join(root, "imports"), WebRoot: filepath.Join(root, "web"), MaxUpload: 1 << 20, MinFreeBytes: 1}
	for _, path := range []string{cfg.ImportRoot, filepath.Join(cfg.WebRoot, "sites")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

func archiveUploadRequest(t *testing.T, path, field, filename string, content []byte) *http.Request {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

// unreadBody fails the test if the handler consumes the request body.
type unreadBody struct {
	t *testing.T
}

func (b unreadBody) Read([]byte) (int, error) {
	b.t.Error("request body was read before admission rejected it")
	return 0, io.EOF
}

func TestInspectStreamsArchiveWithoutMultipartSpool(t *testing.T) {
	cfg := uploadTestConfig(t)
	archive, err := os.ReadFile(makeTarGz(t, map[string]string{"cpmove-account/homedir/public_html/index.php": "ok"}))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg, Auth: Auth{}}
	request := archiveUploadRequest(t, "/api/cpmove/inspect", "backup", "cpmove-account.tar.gz", archive)
	response := httptest.NewRecorder()
	app.inspect(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("inspect status = %d, body = %s", response.Code, response.Body.String())
	}
	// MultipartReader leaves an empty sentinel form; FormFile and
	// ParseMultipartForm would have recorded the spooled file part here.
	if request.MultipartForm != nil && len(request.MultipartForm.File) > 0 {
		t.Fatal("inspect parsed the multipart form; the archive must stream without a spool copy")
	}
	var info struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &info); err != nil || info.UploadID == "" {
		t.Fatalf("inspect response = %s, err = %v", response.Body.String(), err)
	}
	staged, err := os.ReadFile(cpmoveUploadPath(cfg.ImportRoot, info.UploadID))
	if err != nil || !bytes.Equal(staged, archive) {
		t.Fatalf("staged upload does not match the archive: %v", err)
	}
}

func TestInspectRejectsUploadBeforeReadingBodyWhenCapacityIsShort(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 1 << 62
	app := &App{Config: cfg, Auth: Auth{}}
	request := httptest.NewRequest(http.MethodPost, "/api/cpmove/inspect", unreadBody{t})
	request.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	request.ContentLength = 1 << 20
	response := httptest.NewRecorder()
	app.inspect(response, request)
	if response.Code != http.StatusInsufficientStorage || !strings.Contains(response.Body.String(), "insufficient free space") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if list, err := os.ReadDir(cfg.ImportRoot); err != nil || len(list) != 0 {
		t.Fatalf("rejected upload staged files: %v %v", list, err)
	}
}

func TestInspectRequiresLengthWithoutUploadCeiling(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MaxUpload = 0
	app := &App{Config: cfg, Auth: Auth{}}
	request := httptest.NewRequest(http.MethodPost, "/api/cpmove/inspect", unreadBody{t})
	request.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	request.ContentLength = -1
	response := httptest.NewRecorder()
	app.inspect(response, request)
	if response.Code != http.StatusLengthRequired {
		t.Fatalf("status = %d, want 411; body = %s", response.Code, response.Body.String())
	}
}

func TestInspectRejectsOversizedArchiveWithoutLeavingStagedObject(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MaxUpload = 1024
	app := &App{Config: cfg, Auth: Auth{}}
	request := archiveUploadRequest(t, "/api/cpmove/inspect", "backup", "big.tar.gz", bytes.Repeat([]byte("x"), 4096))
	response := httptest.NewRecorder()
	app.inspect(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", response.Code, response.Body.String())
	}
	if list, err := os.ReadDir(cfg.ImportRoot); err != nil || len(list) != 0 {
		t.Fatalf("oversized upload staged files: %v %v", list, err)
	}
}

func TestCPMoveImportRejectsArchiveSizedBodies(t *testing.T) {
	app := &App{Config: uploadTestConfig(t), Auth: Auth{}}
	request := httptest.NewRequest(http.MethodPost, "/api/cpmove/import", unreadBody{t})
	request.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	request.ContentLength = cpmoveImportRequestBytes + 1
	response := httptest.NewRecorder()
	app.importBackup(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
}

func TestCapacityCheckSumsDemandsOnSharedFilesystem(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	free, err := availableBytes(cfg.ImportRoot)
	if err != nil {
		t.Fatal(err)
	}
	share := free / 10 * 6
	sites := filepath.Join(cfg.WebRoot, "sites")
	var ledger capacityLedger
	if err := ledger.check(cfg, "test", []capacityDemand{{Path: cfg.ImportRoot, Bytes: share}}); err != nil {
		t.Fatalf("single demand of 60%% free was rejected: %v", err)
	}
	err = ledger.check(cfg, "test", []capacityDemand{{Path: cfg.ImportRoot, Bytes: share}, {Path: sites, Bytes: share}})
	var capacity *capacityError
	if !errors.As(err, &capacity) {
		t.Fatalf("two 60%% demands on one filesystem were admitted: %v", err)
	}
	if err := ledger.check(cfg, "test", []capacityDemand{{Path: cfg.ImportRoot, Bytes: ^uint64(0)}, {Path: sites, Bytes: 1}}); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflowing demand error = %v", err)
	}
}

// TestCapacityReservationsPreventDoubleAdmission is the concurrency guard:
// two uploads that each fit alone must not both be admitted against the same
// free space while the first is still in progress.
func TestCapacityReservationsPreventDoubleAdmission(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	free, err := availableBytes(cfg.ImportRoot)
	if err != nil {
		t.Fatal(err)
	}
	demand := []capacityDemand{{Path: cfg.ImportRoot, Bytes: free / 10 * 6}}
	var ledger capacityLedger
	first, err := ledger.reserve(cfg, "test", cfg.ImportRoot, demand)
	if err != nil {
		t.Fatalf("first reservation rejected: %v", err)
	}
	if ledger.heldBytes(cfg.ImportRoot) != demand[0].Bytes {
		t.Fatalf("held = %d, want %d", ledger.heldBytes(cfg.ImportRoot), demand[0].Bytes)
	}
	_, err = ledger.reserve(cfg, "test", cfg.ImportRoot, demand)
	var capacity *capacityError
	if !errors.As(err, &capacity) || !strings.Contains(err.Error(), "reserved by in-progress uploads") {
		t.Fatalf("second reservation error = %v, want capacity error naming the outstanding reservation", err)
	}
	if err := ledger.check(cfg, "test", demand); !errors.As(err, &capacity) {
		t.Fatalf("job check ignored the outstanding reservation: %v", err)
	}
	first.release()
	first.release() // idempotent
	if ledger.heldBytes(cfg.ImportRoot) != 0 {
		t.Fatalf("held after release = %d, want 0", ledger.heldBytes(cfg.ImportRoot))
	}
	second, err := ledger.reserve(cfg, "test", cfg.ImportRoot, demand)
	if err != nil {
		t.Fatalf("reservation after release rejected: %v", err)
	}
	second.release()
}

func TestCapacityReservationsCoordinateAcrossLedgerInstances(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	free, err := availableBytes(cfg.ImportRoot)
	if err != nil {
		t.Fatal(err)
	}
	demand := []capacityDemand{{Path: cfg.ImportRoot, Bytes: free / 10 * 6}}
	hold, err := first.reserve(cfg, "cross-process", cfg.ImportRoot, demand)
	if err != nil {
		t.Fatalf("first shared reservation rejected: %v", err)
	}
	defer hold.release()
	if _, err := second.reserve(cfg, "cross-process", cfg.ImportRoot, demand); err == nil {
		t.Fatal("second ledger admitted a reservation already held by another process")
	}
	hold.release()
	secondHold, err := second.reserve(cfg, "cross-process", cfg.ImportRoot, demand)
	if err != nil {
		t.Fatalf("reservation after shared release rejected: %v", err)
	}
	secondHold.release()
}

func TestAppCapacityLedgerPreservesOwnerIdentity(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	initialized, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{capacity: initialized}
	ledger := app.capacityLedger()
	if ledger != initialized {
		t.Fatal("App rebuilt the initialized capacity ledger instead of preserving its process identity")
	}

	reservation, err := ledger.reserve(cfg, "app-owner", cfg.ImportRoot, []capacityDemand{{Path: cfg.ImportRoot, Bytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()

	var ownerPID int
	var ownerStart string
	if err := db.QueryRow(`SELECT owner_pid,owner_start FROM capacity_reservations WHERE reservation_id=?`, reservation.id).Scan(&ownerPID, &ownerStart); err != nil {
		t.Fatal(err)
	}
	if ownerPID != os.Getpid() || ownerPID <= 0 || ownerStart == "" || ownerStart != initialized.ownerStart {
		t.Fatalf("reservation owner = (%d, %q), want current process (%d, %q)", ownerPID, ownerStart, os.Getpid(), initialized.ownerStart)
	}
}

func TestCapacityLedgerReclaimsOnlyReservationsFromDeadProcesses(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := first.reserve(cfg, "live-owner", cfg.ImportRoot, []capacityDemand{{Path: cfg.ImportRoot, Bytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if _, err := newCapacityLedger(db); err != nil {
		t.Fatalf("second live process ledger initialization failed: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM capacity_reservations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("live reservation count = %d, err=%v; want 1", count, err)
	}
	if _, err := db.Exec(`UPDATE capacity_reservations SET owner_pid=?,owner_start=?`, 2147483647, "old-boot:stale-process"); err != nil {
		t.Fatal(err)
	}
	if _, err := newCapacityLedger(db); err != nil {
		t.Fatalf("ledger failed to reclaim dead process reservation: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM capacity_reservations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan reservation count = %d, err=%v; want 0", count, err)
	}
	reusedPID, err := first.reserve(cfg, "reused-pid", cfg.ImportRoot, []capacityDemand{{Path: cfg.ImportRoot, Bytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer reusedPID.release()
	if _, err := db.Exec(`UPDATE capacity_reservations SET owner_start='previous-boot:same-pid' WHERE reservation_id=?`, reusedPID.id); err != nil {
		t.Fatal(err)
	}
	if _, err := newCapacityLedger(db); err != nil {
		t.Fatalf("ledger failed to reclaim reused-PID reservation: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM capacity_reservations WHERE reservation_id=?`, reusedPID.id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reused-PID reservation count = %d, err=%v; want 0", count, err)
	}
}

func TestCapacityLedgerMigratesAndReclaimsLegacyReservationRows(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE capacity_reservations;
		CREATE TABLE capacity_reservations (
			reservation_id TEXT PRIMARY KEY,
			device INTEGER NOT NULL,
			bytes INTEGER NOT NULL,
			workflow TEXT NOT NULL,
			staging_path TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		INSERT INTO capacity_reservations VALUES('legacy',1,100,'old-workflow','/tmp',unixepoch());`); err != nil {
		t.Fatal(err)
	}
	if _, err := newCapacityLedger(db); err != nil {
		t.Fatalf("migrate legacy capacity reservations: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM capacity_reservations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy orphan reservation count = %d, err=%v; want 0", count, err)
	}
	var ownerColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('capacity_reservations') WHERE name IN ('owner_pid','owner_start')`).Scan(&ownerColumns); err != nil || ownerColumns != 2 {
		t.Fatalf("capacity owner columns = %d, err=%v; want 2", ownerColumns, err)
	}
}

func TestCapacityLedgerOwnerColumnMigrationCanRaceAcrossServices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	firstDB, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer firstDB.Close()
	secondDB, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondDB.Close()
	if _, err := firstDB.Exec(`DROP TABLE capacity_reservations;
		CREATE TABLE capacity_reservations (
			reservation_id TEXT NOT NULL,
			device INTEGER NOT NULL,
			bytes INTEGER NOT NULL,
			workflow TEXT NOT NULL,
			staging_path TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY (reservation_id, device)
		);`); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, db := range []*sql.DB{firstDB, secondDB} {
		go func(db *sql.DB) {
			<-start
			_, err := newCapacityLedger(db)
			errs <- err
		}(db)
	}
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent capacity ledger migration failed: %v", err)
		}
	}
}

func TestDurableCapacityReservationSupportsMultipleDevices(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := ledger.admitDurable(cfg, "multi-device", []deviceDemand{
		{device: 11, path: cfg.ImportRoot, bytes: 1},
		{device: 22, path: cfg.ImportRoot, bytes: 1},
	}, true)
	if err != nil {
		t.Fatalf("multi-device reservation rejected: %v", err)
	}
	defer reservation.release()
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM capacity_reservations WHERE reservation_id=?`, reservation.id).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("stored %d device rows for one reservation, want 2", rows)
	}
}

func TestDurableCapacityReservationGrowsWithoutInMemoryLedger(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := ledger.reserve(cfg, "database-backup", cfg.ImportRoot, []capacityDemand{{Path: cfg.ImportRoot, Bytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if ledger.held != nil {
		t.Fatal("durable ledger unexpectedly populated the process-local hold map")
	}
	if err := reservation.grow(cfg.ImportRoot, 4096); err != nil {
		t.Fatalf("durable reservation growth failed: %v", err)
	}
	if got := ledger.heldBytes(cfg.ImportRoot); got < 4097 {
		t.Fatalf("durable held bytes = %d, want at least 4097", got)
	}
}

func TestDurableCapacityReservationConsumesAndExtendsWithoutInMemoryLedger(t *testing.T) {
	withProgressiveStep(t, 1000)
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	demandsFor := func(bytes uint64) []capacityDemand {
		return []capacityDemand{{Path: cfg.ImportRoot, Bytes: bytes}}
	}
	reservation, err := ledger.reserveProgressive(cfg, "streamed-upload", cfg.ImportRoot, 2500, demandsFor)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if err := reservation.consume(600); err != nil {
		t.Fatalf("durable consume failed: %v", err)
	}
	if got := ledger.heldBytes(cfg.ImportRoot); got != 1400 {
		t.Fatalf("durable held bytes after consume and extension = %d, want 1400", got)
	}
	if err := reservation.consume(1000); err != nil {
		t.Fatalf("durable progressive extension failed: %v", err)
	}
	if got := ledger.heldBytes(cfg.ImportRoot); got != 1000 {
		t.Fatalf("durable held bytes after extension = %d, want 1000", got)
	}
}

func TestDurableCapacityConsumeDoesNotAdvanceLocalStateBeforeSQLite(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := ledger.reserve(cfg, "streamed-upload", cfg.ImportRoot, []capacityDemand{{Path: cfg.ImportRoot, Bytes: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if _, err := db.Exec(`CREATE TRIGGER reject_capacity_consume BEFORE UPDATE ON capacity_reservations BEGIN SELECT RAISE(ABORT, 'injected reservation update failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := reservation.consume(400); err == nil {
		t.Fatal("consume succeeded despite durable reservation update failure")
	}
	device, err := filesystemDevice(cfg.ImportRoot)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.held[device] != 1000 || reservation.consumedSize != 0 {
		t.Fatalf("local reservation advanced despite failed SQLite update: held=%d consumed=%d", reservation.held[device], reservation.consumedSize)
	}
	if held := ledger.heldBytes(cfg.ImportRoot); held != 1000 {
		t.Fatalf("durable reservation after failed update = %d, want 1000", held)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_capacity_consume`); err != nil {
		t.Fatal(err)
	}
	if err := reservation.consume(400); err != nil {
		t.Fatalf("consume retry after SQLite recovery: %v", err)
	}
	if reservation.held[device] != 600 || reservation.consumedSize != 400 {
		t.Fatalf("local reservation after durable retry: held=%d consumed=%d, want 600/400", reservation.held[device], reservation.consumedSize)
	}
	if held := ledger.heldBytes(cfg.ImportRoot); held != 600 {
		t.Fatalf("durable reservation after retry = %d, want 600", held)
	}
	if _, err := db.Exec(`DELETE FROM capacity_reservations WHERE reservation_id=? AND device=?`, reservation.id, device); err != nil {
		t.Fatal(err)
	}
	if err := reservation.consume(500); err == nil {
		t.Fatal("consume advanced after its durable reservation row disappeared")
	}
	if reservation.held[device] != 600 || reservation.consumedSize != 400 {
		t.Fatalf("local state advanced after durable reservation disappeared: held=%d consumed=%d, want 600/400", reservation.held[device], reservation.consumedSize)
	}
}

func TestDurableCapacityExtensionCommitFailureDoesNotAdvanceLocalHold(t *testing.T) {
	withProgressiveStep(t, 1000)
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ledger, err := newCapacityLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	demandsFor := func(bytes uint64) []capacityDemand {
		return []capacityDemand{{Path: cfg.ImportRoot, Bytes: bytes}}
	}
	reservation, err := ledger.reserveProgressive(cfg, "streamed-upload", cfg.ImportRoot, 2500, demandsFor)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if _, err := db.Exec(`CREATE TABLE capacity_commit_parent (id INTEGER PRIMARY KEY);
CREATE TABLE capacity_commit_guard (parent_id INTEGER, FOREIGN KEY(parent_id) REFERENCES capacity_commit_parent(id) DEFERRABLE INITIALLY DEFERRED);
CREATE TRIGGER reject_capacity_extension AFTER UPDATE ON capacity_reservations WHEN NEW.bytes > OLD.bytes BEGIN INSERT INTO capacity_commit_guard(parent_id) VALUES(999); END`); err != nil {
		t.Fatal(err)
	}
	if err := reservation.consume(600); err == nil {
		t.Fatal("progressive extension committed despite deferred constraint failure")
	}
	device, err := filesystemDevice(cfg.ImportRoot)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.held[device] != 400 || reservation.consumedSize != 600 || reservation.admitted != 1000 {
		t.Fatalf("local state after failed extension commit: held=%d consumed=%d admitted=%d, want 400/600/1000", reservation.held[device], reservation.consumedSize, reservation.admitted)
	}
	if held := ledger.heldBytes(cfg.ImportRoot); held != 400 {
		t.Fatalf("durable reservation after failed extension commit = %d, want 400", held)
	}
	if _, err := db.Exec(`DROP TRIGGER reject_capacity_extension; DROP TABLE capacity_commit_guard; DROP TABLE capacity_commit_parent`); err != nil {
		t.Fatal(err)
	}
	if err := reservation.consume(600); err != nil {
		t.Fatalf("extension retry after commit failure: %v", err)
	}
	if reservation.held[device] != 1400 || reservation.admitted != 2000 {
		t.Fatalf("local state after successful extension retry: held=%d admitted=%d, want 1400/2000", reservation.held[device], reservation.admitted)
	}
	if held := ledger.heldBytes(cfg.ImportRoot); held != 1400 {
		t.Fatalf("durable reservation after successful retry = %d, want 1400", held)
	}
}

func TestDurableCapacityMutationsRejectMissingReservationRows(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*capacityReservation, Config) error
	}{
		{
			name: "grow",
			mutate: func(reservation *capacityReservation, cfg Config) error {
				return reservation.grow(cfg.ImportRoot, 100)
			},
		},
		{
			name: "extend",
			mutate: func(reservation *capacityReservation, cfg Config) error {
				reservation.ledger.mu.Lock()
				defer reservation.ledger.mu.Unlock()
				return reservation.extendLocked([]capacityDemand{{Path: cfg.ImportRoot, Bytes: 100}})
			},
		},
		{
			name: "shrink",
			mutate: func(reservation *capacityReservation, cfg Config) error {
				return reservation.shrink(cfg.ImportRoot, 100)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := uploadTestConfig(t)
			cfg.MinFreeBytes = 0
			db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ledger, err := newCapacityLedger(db)
			if err != nil {
				t.Fatal(err)
			}
			reservation, err := ledger.reserve(cfg, "test", cfg.ImportRoot, []capacityDemand{{Path: cfg.ImportRoot, Bytes: 1000}})
			if err != nil {
				t.Fatal(err)
			}
			defer reservation.release()
			device, err := filesystemDevice(cfg.ImportRoot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DELETE FROM capacity_reservations WHERE reservation_id=? AND device=?`, reservation.id, device); err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(reservation, cfg); err == nil {
				t.Fatal("mutation succeeded after its durable reservation row disappeared")
			}
			if reservation.held[device] != 1000 || reservation.consumedSize != 0 {
				t.Fatalf("local reservation changed after failed durable mutation: held=%d consumed=%d", reservation.held[device], reservation.consumedSize)
			}
			if got := ledger.heldBytes(cfg.ImportRoot); got != 0 {
				t.Fatalf("durable hold after external deletion = %d, want 0", got)
			}
		})
	}
}

func TestCapacityReservationConsumeReleasesWrittenBytes(t *testing.T) {
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	var ledger capacityLedger
	reservation, err := ledger.reserve(cfg, "test", cfg.ImportRoot, []capacityDemand{{Path: cfg.ImportRoot, Bytes: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if err := reservation.consume(400); err != nil {
		t.Fatal(err)
	}
	if held := ledger.heldBytes(cfg.ImportRoot); held != 600 {
		t.Fatalf("held after 400 written = %d, want 600", held)
	}
	if err := reservation.consume(400); err != nil || ledger.heldBytes(cfg.ImportRoot) != 600 {
		t.Fatalf("repeated consume changed the hold: %d %v", ledger.heldBytes(cfg.ImportRoot), err)
	}
	if err := reservation.consume(5000); err != nil || ledger.heldBytes(cfg.ImportRoot) != 0 {
		t.Fatalf("overrun consume should floor the hold at zero: %d %v", ledger.heldBytes(cfg.ImportRoot), err)
	}
	cfg.MinFreeBytes = 1 << 62
	short, err := (&capacityLedger{}).reserve(Config{ImportRoot: cfg.ImportRoot, WebRoot: cfg.WebRoot}, "test", cfg.ImportRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	short.reserve = 1 << 62
	var capacity *capacityError
	if err := short.consume(1); !errors.As(err, &capacity) {
		t.Fatalf("consume did not detect free space falling below the reserve: %v", err)
	}
}

func TestDeclaredUploadBytes(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.ContentLength = 42
	if size, known, err := declaredUploadBytes(request, 10); err != nil || size != 42 || !known {
		t.Fatalf("declared = %d, %v, %v; want known Content-Length", size, known, err)
	}
	request.ContentLength = -1
	if size, known, err := declaredUploadBytes(request, 10); err != nil || size != 10 || known {
		t.Fatalf("declared = %d, %v, %v; want the ceiling as an unknown-length limit", size, known, err)
	}
	if _, _, err := declaredUploadBytes(request, 0); !errors.Is(err, errUploadLengthRequired) {
		t.Fatalf("err = %v, want errUploadLengthRequired", err)
	}
}

func withProgressiveStep(t *testing.T, step uint64) {
	t.Helper()
	previous := progressiveUploadStep
	progressiveUploadStep = step
	t.Cleanup(func() { progressiveUploadStep = previous })
}

// An upload of unknown length holds one step ahead of the bytes written
// instead of the whole upload ceiling.
func TestProgressiveReservationExtendsAheadOfWrites(t *testing.T) {
	withProgressiveStep(t, 1000)
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	var ledger capacityLedger
	demandsFor := func(archive uint64) []capacityDemand { return []capacityDemand{{Path: cfg.ImportRoot, Bytes: archive}} }
	reservation, err := ledger.reserveProgressive(cfg, "test", cfg.ImportRoot, 3500, demandsFor)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if held := ledger.heldBytes(cfg.ImportRoot); held != 1000 {
		t.Fatalf("initial hold = %d, want one step (1000), not the 3500 ceiling", held)
	}
	if err := reservation.consume(400); err != nil || ledger.heldBytes(cfg.ImportRoot) != 600 || reservation.admitted != 1000 {
		t.Fatalf("after 400 written: held %d admitted %d err %v", ledger.heldBytes(cfg.ImportRoot), reservation.admitted, err)
	}
	// Within half a step of the admitted size: extend before the writer arrives.
	if err := reservation.consume(600); err != nil || reservation.admitted != 2000 || ledger.heldBytes(cfg.ImportRoot) != 1400 {
		t.Fatalf("after 600 written: held %d admitted %d err %v", ledger.heldBytes(cfg.ImportRoot), reservation.admitted, err)
	}
	if err := reservation.consume(3200); err != nil || reservation.admitted != 3500 {
		t.Fatalf("extension must stop at the ceiling: admitted %d err %v", reservation.admitted, err)
	}
	var capacity *capacityError
	if err := reservation.consume(3501); !errors.As(err, &capacity) {
		t.Fatalf("writing past the ceiling = %v, want capacity error", err)
	}
}

func TestProgressiveReservationStopsWhenSpaceRunsOut(t *testing.T) {
	withProgressiveStep(t, 1000)
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	var ledger capacityLedger
	demandsFor := func(archive uint64) []capacityDemand { return []capacityDemand{{Path: cfg.ImportRoot, Bytes: archive}} }
	reservation, err := ledger.reserveProgressive(cfg, "test", cfg.ImportRoot, 1<<40, demandsFor)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	reservation.reserve = 1 << 62 // the filesystem can no longer absorb another step
	var capacity *capacityError
	if err := reservation.consume(900); !errors.As(err, &capacity) || !strings.Contains(err.Error(), "to continue this test") {
		t.Fatalf("extension without space = %v, want capacity error", err)
	}
}

// A chunked upload smaller than free space must not be refused because the
// configured ceiling exceeds free space.
func TestChunkedUploadIsNotRefusedForTheUploadCeiling(t *testing.T) {
	withProgressiveStep(t, 64<<10)
	cfg := uploadTestConfig(t)
	cfg.MinFreeBytes = 0
	cfg.MaxUpload = 1 << 60 // far beyond any filesystem
	app := &App{Config: cfg, Auth: Auth{}}
	request := archiveUploadRequest(t, "/api/cpmove/inspect", "backup", "site.tar.gz", bytes.Repeat([]byte("x"), 4096))
	request.ContentLength = -1
	response := httptest.NewRecorder()
	app.inspect(response, request)
	if response.Code == http.StatusInsufficientStorage || strings.Contains(response.Body.String(), "insufficient free space") {
		t.Fatalf("small chunked upload refused for capacity: %d %s", response.Code, response.Body.String())
	}
	if held := app.capacityLedger().heldBytes(cfg.ImportRoot); held != 0 {
		t.Fatalf("reservation leaked after the request: %d bytes held", held)
	}
}

// TestJobEventsOutliveServerWriteTimeout guards the event stream against the
// server-wide write deadline: a job update sent after WriteTimeout elapsed
// must still reach the client.
func TestJobEventsOutliveServerWriteTimeout(t *testing.T) {
	app := &App{Config: uploadTestConfig(t), Auth: Auth{}, Jobs: NewJobs()}
	server := httptest.NewUnstartedServer(http.HandlerFunc(app.jobEvents))
	server.Config.WriteTimeout = 200 * time.Millisecond
	server.Start()
	defer server.Close()
	response, err := http.Get(server.URL + "/api/jobs/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	lines := bufio.NewReader(response.Body)
	if line, err := lines.ReadString('\n'); err != nil || line != "event: snapshot\n" {
		t.Fatalf("first line = %q, err = %v", line, err)
	}
	time.Sleep(400 * time.Millisecond)
	app.Jobs.publish(Job{ID: "job-late", Kind: "site.backup", State: "running"})
	deadline := time.After(5 * time.Second)
	found := make(chan error, 1)
	go func() {
		for {
			line, err := lines.ReadString('\n')
			if err != nil {
				found <- err
				return
			}
			if strings.HasPrefix(line, "data:") && strings.Contains(line, "job-late") {
				found <- nil
				return
			}
		}
	}()
	select {
	case err := <-found:
		if err != nil {
			t.Fatalf("stream closed before the late job event arrived: %v", err)
		}
	case <-deadline:
		t.Fatal("late job event never arrived")
	}
}

func TestWriteUploadErrorMapsStatuses(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{&uploadRejection{http.StatusUnprocessableEntity, "bad"}, http.StatusUnprocessableEntity},
		{&capacityError{"full"}, http.StatusInsufficientStorage},
		{errUploadLengthRequired, http.StatusLengthRequired},
		{http.ErrNotMultipart, http.StatusBadRequest},
		{fmt.Errorf("read body: %w", os.ErrDeadlineExceeded), http.StatusRequestTimeout},
		{errors.New("disk exploded"), http.StatusInternalServerError},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		(&App{}).writeUploadError(response, httptest.NewRequest(http.MethodPost, "/api/cpmove/inspect", nil), test.err)
		if response.Code != test.want {
			t.Errorf("writeUploadError(%v) = %d, want %d", test.err, response.Code, test.want)
		}
	}
}

func TestWriteUploadErrorDoesNotReflectMultipartParserDetails(t *testing.T) {
	response := httptest.NewRecorder()
	err := fmt.Errorf("request-controlled <script>alert(1)</script>: %w", http.ErrNotMultipart)
	(&App{}).writeUploadError(response, httptest.NewRequest(http.MethodPost, "/api/cpmove/inspect", nil), err)

	if got := response.Body.String(); strings.Contains(got, "request-controlled") || strings.Contains(got, "<script>") || !strings.Contains(got, "invalid multipart upload") {
		t.Fatalf("multipart parser detail was reflected: %q", got)
	}
}

func TestStageArchiveUploadDoesNotReflectMultipartParserDetails(t *testing.T) {
	root := t.TempDir()
	app := &App{Config: uploadTestConfig(t), Metrics: NewMetrics()}
	app.Config.ImportRoot = filepath.Join(root, "imports")
	request := httptest.NewRequest(http.MethodPost, "/api/cpmove/inspect", strings.NewReader("request-controlled"))
	request.Header.Set("Content-Type", "multipart/form-data")
	response := httptest.NewRecorder()
	_, reservation, ok := app.stageArchiveUpload(response, request, "cpmove inspect", upload.Options{})
	if ok || reservation != nil {
		t.Fatal("malformed multipart request was accepted")
	}
	if strings.Contains(response.Body.String(), "script") || strings.Contains(response.Body.String(), "request-controlled") {
		t.Fatalf("multipart parser detail was reflected: %q", response.Body.String())
	}
}

// TestInspectAbortsStalledUpload guards the upload slot and reservation: a
// client that stops sending body bytes is cut off after uploadIdleTimeout
// with 408, and its partial object and capacity hold are released.
func TestInspectAbortsStalledUpload(t *testing.T) {
	previous := uploadIdleTimeout
	uploadIdleTimeout = 300 * time.Millisecond
	t.Cleanup(func() { uploadIdleTimeout = previous })
	cfg := uploadTestConfig(t)
	app := &App{Config: cfg, Auth: Auth{}, Metrics: NewMetrics()}
	server := httptest.NewServer(http.HandlerFunc(app.inspect))
	defer server.Close()

	reader, writer := io.Pipe()
	defer writer.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/cpmove/inspect", reader)
	if err != nil {
		t.Fatal(err)
	}
	const boundary = "stall"
	request.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	request.ContentLength = 1 << 20
	go func() {
		// Send the part header and some archive bytes, then stall.
		_, _ = io.WriteString(writer, "--"+boundary+"\r\nContent-Disposition: form-data; name=\"backup\"; filename=\"a.tar.gz\"\r\n\r\n")
		_, _ = writer.Write(bytes.Repeat([]byte("x"), 64<<10))
	}()
	started := time.Now()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestTimeout {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 408; body = %s", response.StatusCode, body)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("stall detected after %s", elapsed)
	}
	if list, err := os.ReadDir(cfg.ImportRoot); err != nil || len(list) != 0 {
		t.Fatalf("stalled upload left staged files: %v %v", list, err)
	}
	if held := app.capacityLedger().heldBytes(cfg.ImportRoot); held != 0 {
		t.Fatalf("stalled upload kept %d reserved bytes", held)
	}
	var metrics bytes.Buffer
	app.Metrics.Write(&metrics)
	if !strings.Contains(metrics.String(), `stepanel_upload_rejections_total{reason="stalled"} 1`) {
		t.Fatal("stalled upload was not counted")
	}
}

func TestInspectReleasesReservationAfterSuccessfulUpload(t *testing.T) {
	cfg := uploadTestConfig(t)
	archive, err := os.ReadFile(makeTarGz(t, map[string]string{"cpmove-account/homedir/public_html/index.php": "ok"}))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg, Auth: Auth{}, Metrics: NewMetrics()}
	response := httptest.NewRecorder()
	app.inspect(response, archiveUploadRequest(t, "/api/cpmove/inspect", "backup", "cpmove-account.tar.gz", archive))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if held := app.capacityLedger().heldBytes(cfg.ImportRoot); held != 0 {
		t.Fatalf("completed upload kept %d reserved bytes", held)
	}
}

func TestInspectResponseDoesNotReflectArchiveMetadata(t *testing.T) {
	cfg := uploadTestConfig(t)
	archive, err := os.ReadFile(makeTarGz(t, map[string]string{"cpmove-account/homedir/public_html/index.php": "ok"}))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg, Auth: Auth{}, Metrics: NewMetrics()}
	response := httptest.NewRecorder()
	app.inspect(response, archiveUploadRequest(t, "/api/cpmove/inspect", "backup", "<script>alert(1)</script>.tar.gz", archive))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "<script>") || strings.Contains(response.Body.String(), "detected_user") {
		t.Fatalf("inspection response reflected archive metadata: %s", response.Body.String())
	}
}

func TestCapacityReservationIDsAreUniqueAcrossRapidAllocations(t *testing.T) {
	seen := make(map[string]struct{}, 10000)
	for i := 0; i < 10000; i++ {
		id := capacityReservationID()
		if _, exists := seen[id]; exists {
			t.Fatalf("capacity reservation ID repeated: %q", id)
		}
		seen[id] = struct{}{}
	}
}
