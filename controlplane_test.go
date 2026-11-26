package stepanel

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cyberducttape/StePanel/internal/migration"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestControlPlaneStateBlobIsTransactionalAndPersistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-plane.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("control-plane database mode = %o, want 600", got)
	}
	type state struct {
		Owner string   `json:"owner"`
		Sites []string `json:"sites"`
	}
	store := &struct{}{}
	value := state{Owner: "customer", Sites: []string{"site-a"}}
	found, err := bindControlPlaneState(store, db, "test-state", &value)
	if err != nil || found {
		t.Fatalf("initial state binding = found %v err %v", found, err)
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if bound, err := persistBoundControlPlaneState(store, payload); !bound || err != nil {
		t.Fatalf("persist state = bound %v err %v", bound, err)
	}
	value = state{}
	found, err = bindControlPlaneState(&struct{}{}, db, "test-state", &value)
	if err != nil || !found || value.Owner != "customer" || len(value.Sites) != 1 {
		t.Fatalf("reloaded state = %#v found %v err %v", value, found, err)
	}
}

func TestInitializeControlPlaneDatabaseCreatesServiceOwnedSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "control-plane.db")
	if err := initializeControlPlaneDatabase(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("initialized database mode = %o, want 600", got)
	}
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='capacity_reservations'`).Scan(&exists); err != nil || exists != 1 {
		t.Fatalf("initialized database schema lookup = %d, %v", exists, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='resource_locks'`).Scan(&exists); err != nil || exists != 1 {
		t.Fatalf("initialized fencing schema lookup = %d, %v", exists, err)
	}
}

func TestInitializeControlPlaneDatabaseRequiresAbsolutePath(t *testing.T) {
	if err := initializeControlPlaneDatabase(filepath.Join("relative", "control.db")); err == nil {
		t.Fatal("relative initialization path unexpectedly succeeded")
	}
}

func TestControlPlanePageLimitMustBeAppliedToEachConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded-control.db")
	limit := int64(16 << 20)
	db, err := openControlPlaneDBWithMaxBytes(path, limit)
	if err != nil {
		t.Fatal(err)
	}
	var pageSize, maxPages int64
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA max_page_count`).Scan(&maxPages); err != nil {
		t.Fatal(err)
	}
	if maxPages*pageSize > limit || maxPages*pageSize < limit-pageSize {
		t.Fatalf("page cap = %d * %d, expected floor of %d bytes", maxPages, pageSize, limit)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var otherPages int64
	if err := other.QueryRow(`PRAGMA max_page_count`).Scan(&otherPages); err != nil {
		t.Fatal(err)
	}
	if otherPages == maxPages {
		t.Fatal("test expected the independent SQLite connection not to inherit the connection-local cap")
	}
	if err := setControlPlaneDBLimit(other, limit); err != nil {
		t.Fatalf("apply cap to independent writer connection: %v", err)
	}
	if err := other.QueryRow(`PRAGMA max_page_count`).Scan(&otherPages); err != nil || otherPages != maxPages {
		t.Fatalf("independent connection cap = %d, err=%v; want %d", otherPages, err, maxPages)
	}
}

func TestControlPlaneStateCASReloadsAndMergesStaleIndependentConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-plane.db")
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

	firstStore := &struct{ id int }{id: 1}
	secondStore := &struct{ id int }{id: 2}
	first := map[string]string{"owner": "first"}
	second := map[string]string{}
	if found, err := bindControlPlaneState(firstStore, firstDB, "cas-test", &first); err != nil || found {
		t.Fatalf("first bind = found %v err %v", found, err)
	}
	if _, err := persistBoundControlPlaneState(firstStore, []byte(`{"owner":"seed"}`)); err != nil {
		t.Fatal(err)
	}
	if found, err := bindControlPlaneState(secondStore, secondDB, "cas-test", &second); err != nil || !found {
		t.Fatalf("second bind = found %v err %v", found, err)
	}
	first = map[string]string{"owner": "first-update"}
	if _, err := persistBoundControlPlaneState(firstStore, []byte(`{"owner":"first-update"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := persistBoundControlPlaneState(secondStore, []byte(`{"owner":"seed","second":"preserved"}`)); err != nil {
		t.Fatalf("stale control-plane write was not retried and merged: %v", err)
	}
	var payload string
	if err := firstDB.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'cas-test'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != `{"owner":"first-update","second":"preserved"}` {
		t.Fatalf("merged write produced %s", payload)
	}
	if second["owner"] != "first-update" || second["second"] != "preserved" {
		t.Fatalf("writer live state did not adopt merged snapshot: %#v", second)
	}
}

func TestControlPlaneStateCASConflictingKeyPreservesDurablePeerValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-plane.db")
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
	firstStore, secondStore := &struct{ id int }{id: 1}, &struct{ id int }{id: 2}
	first := map[string]string{"resource": "original"}
	second := map[string]string{}
	if _, err := bindControlPlaneState(firstStore, firstDB, "conflict-test", &first); err != nil {
		t.Fatal(err)
	}
	if _, err := persistBoundControlPlaneState(firstStore, []byte(`{"resource":"original"}`)); err != nil {
		t.Fatal(err)
	}
	if found, err := bindControlPlaneState(secondStore, secondDB, "conflict-test", &second); err != nil || !found {
		t.Fatalf("second bind = found %v err %v", found, err)
	}
	if _, err := persistBoundControlPlaneState(firstStore, []byte(`{"resource":"panel-value"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := persistBoundControlPlaneState(secondStore, []byte(`{"resource":"stale-worker-value"}`)); err == nil {
		t.Fatal("conflicting stale write unexpectedly succeeded")
	}
	var payload string
	if err := firstDB.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'conflict-test'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != `{"resource":"panel-value"}` || second["resource"] != "panel-value" {
		t.Fatalf("conflict did not preserve/refresh durable peer state: payload=%s local=%#v", payload, second)
	}
	second["resource"] = "worker-retry-value"
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := persistBoundControlPlaneState(secondStore, encoded); err != nil {
		t.Fatalf("retry from refreshed state failed: %v", err)
	}
	if err := firstDB.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'conflict-test'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != `{"resource":"worker-retry-value"}` {
		t.Fatalf("retry did not apply from refreshed durable state: %s", payload)
	}
}

func TestControlPlaneStateCASCrossProcessMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-plane.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	panelStore := &struct{ id string }{id: "panel"}
	panelState := map[string]string{"seed": "value"}
	if found, err := bindControlPlaneState(panelStore, db, "cross-process", &panelState); err != nil || found {
		t.Fatalf("panel bind = found %v err %v", found, err)
	}
	if _, err := persistBoundControlPlaneState(panelStore, []byte(`{"seed":"value"}`)); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	proceed := filepath.Join(root, "proceed")
	result := filepath.Join(root, "result")
	child := exec.Command(os.Args[0], "-test.run=^TestControlPlaneStateCASCrossProcessHelper$", "-test.v")
	child.Env = append(os.Environ(),
		"STEPANEL_CAS_CHILD=1",
		"STEPANEL_CAS_DB="+path,
		"STEPANEL_CAS_READY="+ready,
		"STEPANEL_CAS_PROCEED="+proceed,
		"STEPANEL_CAS_RESULT="+result,
	)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	waitForControlPlaneTestFile(t, ready)

	if _, err := persistBoundControlPlaneState(panelStore, []byte(`{"panel":"updated"}`)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proceed, []byte("go"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("worker process failed: %v", err)
	}
	payload, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	var resultData struct {
		Durable map[string]string `json:"durable"`
		Local   map[string]string `json:"local"`
	}
	if err := json.Unmarshal(payload, &resultData); err != nil {
		t.Fatal(err)
	}
	if resultData.Durable["panel"] != "updated" || resultData.Durable["worker"] != "updated" {
		t.Fatalf("cross-process durable state = %#v", resultData.Durable)
	}
	if resultData.Local["panel"] != "updated" || resultData.Local["worker"] != "updated" {
		t.Fatalf("worker live state did not refresh from merged durable state: %#v", resultData.Local)
	}
}

func TestControlPlaneStateCASCrossProcessHelper(t *testing.T) {
	if os.Getenv("STEPANEL_CAS_CHILD") != "1" {
		return
	}
	db, err := openControlPlaneDB(os.Getenv("STEPANEL_CAS_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	workerStore := &struct{ id string }{id: "worker"}
	workerState := map[string]string{}
	if found, err := bindControlPlaneState(workerStore, db, "cross-process", &workerState); err != nil || !found {
		t.Fatalf("worker bind = found %v err %v", found, err)
	}
	if err := os.WriteFile(os.Getenv("STEPANEL_CAS_READY"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForControlPlaneTestFile(t, os.Getenv("STEPANEL_CAS_PROCEED"))
	if _, err := persistBoundControlPlaneState(workerStore, []byte(`{"worker":"updated"}`)); err != nil {
		t.Fatal(err)
	}
	payload, _, err := readControlPlaneBlobWithRevision(db, "cross-process")
	if err != nil {
		t.Fatal(err)
	}
	result := struct {
		Durable map[string]string `json:"durable"`
		Local   map[string]string `json:"local"`
	}{Durable: map[string]string{}, Local: workerState}
	if err := json.Unmarshal(payload, &result.Durable); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("STEPANEL_CAS_RESULT"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkMergeControlPlaneState(b *testing.B) {
	for _, siteCount := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("%d_sites", siteCount), func(b *testing.B) {
			baseState := make(map[string]json.RawMessage, siteCount)
			for i := 0; i < siteCount; i++ {
				baseState[fmt.Sprintf("site-%d", i)] = json.RawMessage(`{"revision":0}`)
			}
			intendedState := make(map[string]json.RawMessage, len(baseState))
			remoteState := make(map[string]json.RawMessage, len(baseState))
			for key, value := range baseState {
				intendedState[key] = value
				remoteState[key] = value
			}
			intendedState["site-0"] = json.RawMessage(`{"revision":1}`)
			remoteState[fmt.Sprintf("site-%d", siteCount-1)] = json.RawMessage(`{"revision":2}`)
			base, _ := json.Marshal(baseState)
			intended, _ := json.Marshal(intendedState)
			remote, _ := json.Marshal(remoteState)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := mergeControlPlaneState(base, intended, remote); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func waitForControlPlaneTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func TestControlPlaneBackupCanBeVerified(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "control-plane.db")
	db, err := openControlPlaneDB(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at) VALUES ('test', '{}', unixepoch())`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(root, "backup", "control-plane.db")
	if err := backupControlPlane(source, backup); err != nil {
		t.Fatal(err)
	}
	if err := verifyControlPlaneBackup(backup); err != nil {
		t.Fatal(err)
	}
}

func TestControlPlaneBackupRejectsMissingSource(t *testing.T) {
	root := t.TempDir()
	if err := backupControlPlane(filepath.Join(root, "missing.db"), filepath.Join(root, "backup.db")); err == nil {
		t.Fatal("backup unexpectedly created a missing source database")
	}
}

func TestControlPlaneRestorePreservesCurrentDatabase(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db, err := openControlPlaneDB(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at) VALUES ('source', '{}', unixepoch())`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "live.db")
	db, err = openControlPlaneDB(destination)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at) VALUES ('old', '{}', unixepoch())`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := restoreControlPlane(source, destination); err != nil {
		t.Fatal(err)
	}
	restored, err := openControlPlaneDB(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var name string
	if err := restored.QueryRow(`SELECT name FROM state_blobs WHERE name = 'source'`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination + ".pre-restore-"); err == nil {
		t.Fatal("unexpected fixed backup path")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	foundPrevious := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "live.db.pre-restore-") {
			foundPrevious = true
		}
	}
	if !foundPrevious {
		t.Fatal("current database was not preserved")
	}
}

// TestControlPlaneMigrationsReconcileAdHocLegacyVersion simulates a database
// created by the pre-migration-engine binary: it recorded a bare "version 1"
// marker after successfully running ALTER TABLE statements one-off, so the
// columns those statements added are already physically present even though
// each was never individually tracked. Opening it with the new migration
// engine must recognize that fact structurally and record versions 2-4
// without re-running SQL that would now fail against existing columns.
func TestControlPlaneMigrationsReconcileAdHocLegacyVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// controlPlaneSchema already includes the columns the old ad-hoc ALTER
	// statements used to add one-off, matching what a real database looks
	// like once those statements had all succeeded historically.
	if _, err := raw.Exec(controlPlaneSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(migration.SchemaMigrationsTable); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO control_plane_migrations (version) VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatalf("reconcile legacy control-plane database: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM control_plane_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(controlPlaneMigrations) {
		t.Fatalf("expected all %d migrations recorded, got %d", len(controlPlaneMigrations), count)
	}
	if _, err := db.Exec(`INSERT INTO jobs (id, kind, state, owner, started_at, updated_at, payload, next_attempt_at, cancel_requested) VALUES ('j1','k','queued','o',0,0,'{}',NULL,0)`); err != nil {
		t.Fatalf("legacy columns unusable after reconciliation: %v", err)
	}
}

// TestControlPlaneMigrationsBackfillMissingColumnOnPartiallyAppliedLegacyDB
// covers a database that stalled between the old ad-hoc ALTER statements
// (the exact failure mode the string-matching approach could not
// distinguish from "already applied"): the migration engine must still add
// the column that is genuinely missing.
func TestControlPlaneMigrationsBackfillMissingColumnOnPartiallyAppliedLegacyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	// Base schema only, as it existed before any of the three ALTER-based
	// columns were ever added - not even next_attempt_at.
	const baseOnly = `
CREATE TABLE jobs (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    operation_key TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    owner TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    finished_at INTEGER,
    payload BLOB NOT NULL,
    lease_owner TEXT,
    lease_expires_at INTEGER,
    updated_at INTEGER NOT NULL
);
CREATE TABLE api_tokens (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER,
    revoked_at INTEGER
);
`
	if _, err := raw.Exec(baseOnly); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatalf("backfill missing columns on partially applied database: %v", err)
	}
	defer db.Close()

	for _, column := range []struct{ table, name string }{{"jobs", "next_attempt_at"}, {"jobs", "cancel_requested"}, {"api_tokens", "scopes"}} {
		var exists bool
		rows, err := db.Query(`SELECT 1 FROM pragma_table_info(?) WHERE name = ?`, column.table, column.name)
		if err != nil {
			t.Fatal(err)
		}
		exists = rows.Next()
		rows.Close()
		if !exists {
			t.Fatalf("expected column %s.%s to be backfilled", column.table, column.name)
		}
	}
}

// TestControlPlaneMigrationsRejectNewerSchemaVersion ensures an older binary
// refuses to run against a database a newer binary has already migrated,
// rather than silently operating on an unrecognized schema.
func TestControlPlaneMigrationsRejectNewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO control_plane_migrations (version) VALUES (?)`, 999); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := openControlPlaneDB(path); err == nil {
		t.Fatal("expected opening a database with a future schema version to fail")
	} else if !strings.Contains(err.Error(), "newer than this binary supports") {
		t.Fatalf("unexpected error opening future-versioned database: %v", err)
	}
}

func TestControlPlaneMigrationsSerializeConcurrentOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.db")
	const callers = 2
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			db, err := openControlPlaneDB(path)
			if err != nil {
				errs <- err
				return
			}
			errs <- db.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent control-plane open failed: %v", err)
		}
	}
}

// The control-plane database holds sessions and tokens: it is created 0600
// before SQLite opens it, whatever the umask, and unsafe locations are refused.
func TestControlPlaneDatabaseIsCreatedPrivatelyAndRefusesUnsafePaths(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "control.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS permission_probe (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(name)
		if errors.Is(err, os.ErrNotExist) && name != path {
			continue
		}
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, %v; want 0600", name, info.Mode().Perm(), err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// An existing database or sidecar with a loose mode is tightened before open.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-journal", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareControlPlaneDatabaseFile(path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-journal"} {
		if info, err := os.Stat(name); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode after prepare = %v, %v; want 0600", name, info.Mode().Perm(), err)
		}
	}

	// A symlinked database is refused rather than followed.
	linkDir := filepath.Join(t.TempDir(), "linked")
	if err := os.Mkdir(linkDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(linkDir, "control.db")); err != nil {
		t.Fatal(err)
	}
	if err := prepareControlPlaneDatabaseFile(filepath.Join(linkDir, "control.db")); err == nil {
		t.Error("a symlinked database was accepted")
	}

	// A directory writable by others is refused.
	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := openControlPlaneDB(filepath.Join(shared, "control.db")); err == nil || !strings.Contains(err.Error(), "writable by others") {
		t.Errorf("world-writable directory = %v, want refusal", err)
	}
	if _, err := os.Stat(filepath.Join(shared, "control.db")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a database was created in a refused directory")
	}
}

// A volume made group-writable for the service's own group (Kubernetes
// fsGroup) is accepted.
func TestControlPlaneDatabaseAcceptsItsOwnGroupWritableDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "volume")
	if err := os.Mkdir(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o2770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, -1, os.Getegid()); err != nil {
		t.Fatal(err)
	}
	db, err := openControlPlaneDB(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatalf("own-group-writable volume: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

type codedSQLiteError int

func (e codedSQLiteError) Error() string { return fmt.Sprintf("sqlite error %d", int(e)) }
func (e codedSQLiteError) Code() int     { return int(e) }

func TestControlPlaneStorageErrorsAreClassified(t *testing.T) {
	for _, test := range []struct {
		err  error
		want error
	}{
		{codedSQLiteError(13), errControlPlaneStorageExhausted},
		{codedSQLiteError(13 | 3<<8), errControlPlaneStorageExhausted},
		{fmt.Errorf("write: %w", syscall.ENOSPC), errControlPlaneStorageExhausted},
		{codedSQLiteError(10 | 12<<8), errControlPlaneStorageUnavailable},
		{codedSQLiteError(8), errControlPlaneStorageUnavailable},
		{codedSQLiteError(11), errControlPlaneStorageUnavailable},
		{errors.New("sql: database is closed"), errControlPlaneStorageUnavailable},
	} {
		got := classifyControlPlaneStorageError(test.err)
		if !errors.Is(got, test.want) || !errors.Is(got, test.err) {
			t.Errorf("classify(%v) = %v, want %v wrapping the original", test.err, got, test.want)
		}
	}
}

// A storage failure is returned as such, without a merge or a refreshed-state
// conflict, marks the control plane degraded, and a later successful write
// clears it.
func TestControlPlaneStorageFailureIsNotAConflict(t *testing.T) {
	db := openTestControlPlane(t)
	store := &ResourceStore{values: map[string]ResourceProfile{}}
	if _, err := bindControlPlaneState(store, db, "resources-storage", &store.values); err != nil {
		t.Fatal(err)
	}
	store.values["shop"] = ResourceProfile{Site: "shop", CPUPercent: 100, MemoryMB: 512, State: "pending"}
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	if !controlPlaneStorageReadiness().Ready {
		t.Fatal("a successful write left the control plane degraded")
	}
	if _, err := db.Exec(`CREATE TRIGGER deny_state_writes BEFORE UPDATE ON state_blobs BEGIN SELECT RAISE(ABORT, 'disk I/O error'); END`); err != nil {
		t.Fatal(err)
	}
	store.values["blog"] = ResourceProfile{Site: "blog", CPUPercent: 100, MemoryMB: 512, State: "pending"}
	err := store.persistLocked()
	if err == nil {
		t.Fatal("a failed write reported success")
	}
	if errors.Is(err, errControlPlaneStateRefreshed) || errors.Is(err, errControlPlaneRevisionConflict) {
		t.Fatalf("a storage failure was reported as a conflict: %v", err)
	}
	if !errors.Is(err, errControlPlaneStorageUnavailable) {
		t.Fatalf("storage failure = %v, want errControlPlaneStorageUnavailable", err)
	}
	if check := controlPlaneStorageReadiness(); check.Ready || !strings.Contains(check.Detail, "unavailable") {
		t.Fatalf("readiness after a storage failure = %+v", check)
	}
	if _, err := db.Exec(`DROP TRIGGER deny_state_writes`); err != nil {
		t.Fatal(err)
	}
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	if !controlPlaneStorageReadiness().Ready {
		t.Fatal("a successful write did not clear the degraded state")
	}
}

// The size limit is a per-connection SQLite setting; a connection replaced by
// database/sql (after a driver error, or here by lifetime) must keep it.
func TestControlPlaneSizeLimitSurvivesConnectionReplacement(t *testing.T) {
	const maxBytes = 32 << 20
	db, err := openControlPlaneDBWithMaxBytes(filepath.Join(t.TempDir(), "control.db"), maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var pageSize, before int64
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA max_page_count`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	db.SetConnMaxLifetime(time.Nanosecond)
	time.Sleep(10 * time.Millisecond)
	var after int64
	if err := db.QueryRow(`PRAGMA max_page_count`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	want := int64(maxBytes) / pageSize
	if before != want || after != want {
		t.Fatalf("max_page_count before/after replacing the connection = %d/%d, want %d", before, after, want)
	}
	stats := db.Stats()
	if stats.MaxIdleClosed+stats.MaxLifetimeClosed == 0 {
		t.Fatal("the test did not replace the connection")
	}
}

// A state-blob write from a request queued behind a long transaction on the
// single connection fails at its deadline as a storage error, not as a
// revision conflict that would be merged and retried.
func TestControlPlaneStateWriteFailsFastWhenTheConnectionIsHeld(t *testing.T) {
	db := openTestControlPlane(t)
	store := &ResourceStore{values: map[string]ResourceProfile{}}
	if _, err := bindControlPlaneState(store, db, "resources-deadline", &store.values); err != nil {
		t.Fatal(err)
	}
	previous := controlPlaneStateWriteTimeout
	controlPlaneStateWriteTimeout = 100 * time.Millisecond
	t.Cleanup(func() { controlPlaneStateWriteTimeout = previous })
	held, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	store.values["shop"] = ResourceProfile{Site: "shop", CPUPercent: 100, MemoryMB: 512, State: "pending"}
	start := time.Now()
	err = store.persistLocked()
	if err == nil {
		t.Fatal("a write succeeded while the only connection was held")
	}
	if errors.Is(err, errControlPlaneRevisionConflict) || errors.Is(err, errControlPlaneStateRefreshed) {
		t.Fatalf("a deadline was reported as a conflict: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the write waited %s instead of failing at its deadline", elapsed)
	}
	if err := held.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := store.persistLocked(); err != nil {
		t.Fatalf("write after the connection was released: %v", err)
	}
}
