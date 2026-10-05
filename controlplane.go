package stepanel

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cyberducttape/StePanel/internal/migration"
	"github.com/cyberducttape/StePanel/internal/recovery"
	_ "modernc.org/sqlite"
)

const controlPlaneSchema = `
CREATE TABLE IF NOT EXISTS jobs (
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
    next_attempt_at INTEGER,
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS jobs_operation_idx ON jobs(kind, owner, operation_key);
CREATE UNIQUE INDEX IF NOT EXISTS jobs_operation_unique_idx ON jobs(kind, owner, operation_key) WHERE operation_key <> '';
CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_empty_operation_unique_idx ON jobs(kind, owner) WHERE operation_key = '' AND state IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS jobs_state_idx ON jobs(state, updated_at);
CREATE TABLE IF NOT EXISTS accounts (
    username TEXT PRIMARY KEY,
    payload BLOB NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tenant_sites (
    site TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS tenant_sites_user_idx ON tenant_sites(username);
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    expiry INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions(username);
CREATE TABLE IF NOT EXISTS api_tokens (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL,
    name TEXT NOT NULL,
    token_hash TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    scopes TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    expires_at INTEGER,
    revoked_at INTEGER
);
CREATE INDEX IF NOT EXISTS api_tokens_user_idx ON api_tokens(username, revoked_at);
CREATE TABLE IF NOT EXISTS state_blobs (
    name TEXT PRIMARY KEY,
    payload BLOB NOT NULL,
    updated_at INTEGER NOT NULL,
    revision INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS totp_replay (
    username TEXT PRIMARY KEY,
    last_counter INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
`

type controlPlaneStateBinding struct {
	db     *sql.DB
	name   string
	target any
}

var controlPlaneStateBindings sync.Map

// SQLite migrations are protected by a cross-process file lock, but the
// driver must first open and ping a connection before runControlPlaneMigrations
// can acquire that lock. Serialize opens within a process so panel startup
// cannot race itself into SQLITE_BUSY during that pre-lock window.
var controlPlaneOpenMu sync.Mutex

func openControlPlaneDB(path string) (*sql.DB, error) {
	controlPlaneOpenMu.Lock()
	defer controlPlaneOpenMu.Unlock()

	if stringsTrimmed := filepath.Clean(path); stringsTrimmed == "." || stringsTrimmed == "" {
		return nil, errors.New("control-plane database path is empty")
	}
	root := filepath.Dir(path)
	if err := os.MkdirAll(root, 0750); err != nil {
		return nil, fmt.Errorf("create control-plane database directory: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve control-plane database path: %w", err)
	}
	dsn := "file:" + abs + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open control-plane database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping control-plane database: %w", err)
	}
	if err := runControlPlaneMigrations(db, abs); err != nil {
		_ = db.Close()
		return nil, err
	}
	// SQLite creates a new database using the process umask, which is commonly
	// 0644 on installers. The control plane contains sessions, tokens, and
	// encrypted job metadata, so enforce its private-at-rest contract on every
	// open, including existing databases created by older releases.
	if err := os.Chmod(abs, 0600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure control-plane database: %w", err)
	}
	return db, nil
}

// controlPlaneMigrations returns the list of database schema migrations
var controlPlaneMigrations = []*migration.Migration{
	migration.NewMigration(1, "initial control-plane schema", func(tx *sql.Tx) error {
		_, err := tx.Exec(controlPlaneSchema)
		return err
	}),
	migration.NewMigrationWithCheck(2, "add jobs.next_attempt_at for retry backoff",
		func(tx *sql.Tx) (bool, error) {
			return controlPlaneColumnExists(tx, "jobs", "next_attempt_at")
		},
		func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN next_attempt_at INTEGER`)
			return err
		},
	),
	migration.NewMigrationWithCheck(3, "add jobs.cancel_requested for durable cancellation",
		func(tx *sql.Tx) (bool, error) {
			return controlPlaneColumnExists(tx, "jobs", "cancel_requested")
		},
		func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE jobs ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0`)
			return err
		},
	),
	migration.NewMigrationWithCheck(4, "add api_tokens.scopes for scoped automation tokens",
		func(tx *sql.Tx) (bool, error) {
			return controlPlaneColumnExists(tx, "api_tokens", "scopes")
		},
		func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE api_tokens ADD COLUMN scopes TEXT NOT NULL DEFAULT ''`)
			return err
		},
	),
	migration.NewMigration(5, "add durable webhook delivery replay protection", func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS webhook_deliveries (
            site TEXT NOT NULL,
            delivery_id TEXT NOT NULL,
            received_at INTEGER NOT NULL,
            expires_at INTEGER NOT NULL,
            PRIMARY KEY (site, delivery_id)
        ); CREATE INDEX IF NOT EXISTS webhook_deliveries_expiry_idx ON webhook_deliveries(expires_at);`)
		return err
	}),
	migration.NewMigrationWithCheck(6, "add atomic control-plane state revisions",
		func(tx *sql.Tx) (bool, error) {
			return controlPlaneColumnExists(tx, "state_blobs", "revision")
		},
		func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE state_blobs ADD COLUMN revision INTEGER NOT NULL DEFAULT 0`)
			return err
		},
	),
	migration.NewMigration(7, "add durable worker heartbeats", func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS worker_heartbeats (
            worker_id TEXT PRIMARY KEY,
            host_id TEXT NOT NULL,
            pid INTEGER NOT NULL,
            started_at INTEGER NOT NULL,
            last_seen INTEGER NOT NULL,
            supported_job_kinds TEXT NOT NULL,
            current_jobs TEXT NOT NULL,
            version TEXT NOT NULL,
            build TEXT NOT NULL
        ); CREATE INDEX IF NOT EXISTS worker_heartbeats_seen_idx ON worker_heartbeats(last_seen);`)
		return err
	}),
	migration.NewMigrationWithCheck(8, "make empty-key active jobs idempotent", func(tx *sql.Tx) (bool, error) {
		var name string
		err := tx.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'jobs_active_empty_operation_unique_idx'`).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil, err
	}, func(tx *sql.Tx) error {
		// Preserve the oldest active job for each empty-key operation before
		// installing the unique index. This makes upgrades safe for databases
		// created before the atomic reservation existed.
		if _, err := tx.Exec(`UPDATE jobs SET state = 'failed', finished_at = unixepoch(), lease_owner = NULL, lease_expires_at = NULL, updated_at = unixepoch() WHERE operation_key = '' AND state IN ('queued', 'running') AND rowid NOT IN (SELECT MIN(rowid) FROM jobs WHERE operation_key = '' AND state IN ('queued', 'running') GROUP BY kind, owner)`); err != nil {
			return err
		}
		_, err := tx.Exec(`CREATE UNIQUE INDEX jobs_active_empty_operation_unique_idx ON jobs(kind, owner) WHERE operation_key = '' AND state IN ('queued', 'running')`)
		return err
	}),
	// The Job Center lists the newest jobs, and ClaimNext checks per owner
	// whether an older queued or a running job blocks a candidate. Without
	// these, listing scans and sorts the whole history and each claim rescans
	// the queue for every candidate (BenchmarkJobsClaimNextAtScale).
	migration.NewMigration(9, "index job listing and claim order", func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE INDEX IF NOT EXISTS jobs_started_idx ON jobs(started_at, id);
CREATE INDEX IF NOT EXISTS jobs_owner_state_idx ON jobs(owner, state, started_at, id);`)
		return err
	}),
	migration.NewMigration(10, "record restore rehearsal history", func(tx *sql.Tx) error {
		_, err := tx.Exec(recovery.Schema)
		return err
	}),
	// Records, per store, that every secret has been re-sealed in the
	// context-bound format. Until then the store may still read the legacy
	// unbound format; afterwards it refuses it (see encryption_format.go).
	migration.NewMigration(11, "track context-bound encryption migration", func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS encryption_formats (
            store TEXT PRIMARY KEY,
            version INTEGER NOT NULL,
            updated_at INTEGER NOT NULL
        );`)
		return err
	}),
}

func controlPlaneColumnExists(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, fmt.Errorf("inspect columns of %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// runControlPlaneMigrations applies every migration newer than the database's
// recorded version, in order, each in its own transaction. It refuses to open
// a database stamped with a schema version newer than this binary knows
// about (a downgrade), and it snapshots the database before applying any
// migration to an already-populated database so a bad migration has a
// recovery point.
func runControlPlaneMigrations(db *sql.DB, path string) error {
	lockPath := path + ".migration.lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open control-plane migration lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock control-plane migrations: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	if _, err := db.Exec(migration.SchemaMigrationsTable); err != nil {
		return fmt.Errorf("create control-plane migrations table: %w", err)
	}

	applied := map[int]bool{}
	var maxApplied int
	rows, err := db.Query(`SELECT version FROM control_plane_migrations`)
	if err != nil {
		return fmt.Errorf("read control-plane schema version: %w", err)
	}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("read control-plane schema version: %w", err)
		}
		applied[version] = true
		if version > maxApplied {
			maxApplied = version
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read control-plane schema version: %w", err)
	}
	rows.Close()

	var latestKnown int
	for _, m := range controlPlaneMigrations {
		if m.Version() > latestKnown {
			latestKnown = m.Version()
		}
	}
	if maxApplied > latestKnown {
		return fmt.Errorf("control-plane database schema version %d is newer than this binary supports (up to %d); refusing to open it with an older build", maxApplied, latestKnown)
	}

	var pending []*migration.Migration
	for _, m := range controlPlaneMigrations {
		if !applied[m.Version()] {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	if maxApplied > 0 {
		snapshot, err := snapshotControlPlaneBeforeMigration(db, path, maxApplied)
		if err != nil {
			return fmt.Errorf("snapshot control-plane database before migrating: %w", err)
		}
		log.Printf("control-plane schema v%d snapshot written to %s before applying %d migration(s)", maxApplied, snapshot, len(pending))
		if err := pruneControlPlaneSnapshots(path, controlPlaneSnapshotRetention); err != nil {
			// Retention never blocks a migration; the new snapshot is durable.
			log.Printf("[ERROR] prune control-plane migration snapshots: %v", err)
		}
	}

	for _, m := range pending {
		if err := applyControlPlaneMigration(db, m); err != nil {
			return fmt.Errorf("apply control-plane migration %d (%s): %w", m.Version(), m.Description(), err)
		}
	}
	return nil
}

func applyControlPlaneMigration(db *sql.DB, m *migration.Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	skip := false
	if m.AlreadyApplied != nil {
		skip, err = m.AlreadyApplied(tx)
		if err != nil {
			return err
		}
	}
	if !skip {
		if err := m.Apply(tx); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO control_plane_migrations (version) VALUES (?)`, m.Version()); err != nil {
		return err
	}
	return tx.Commit()
}

// controlPlaneSnapshotRetention is how many pre-migration snapshots are kept.
const controlPlaneSnapshotRetention = 3

// controlPlaneSnapshotDir holds pre-migration snapshots. It is private
// (0700), so a snapshot is never readable by others, even while SQLite is
// still writing it.
func controlPlaneSnapshotDir(path string) string {
	return path + ".snapshots"
}

var controlPlaneSnapshotPattern = regexp.MustCompile(`^pre-migration-v[0-9]+-[0-9]+\.db$`)

// snapshotControlPlaneBeforeMigration writes a verified, durable copy of the
// database at schema version before any migration runs against existing
// data, and returns its path. The copy is written under a temporary name,
// fsynced, checked with PRAGMA quick_check and its schema version, and only
// then renamed into place and the directory fsynced, so a crash can never
// leave a truncated file that looks like a recovery point. It uses the
// already-open handle rather than reopening the database, since the
// control-plane connection pool is limited to a single connection.
func snapshotControlPlaneBeforeMigration(db *sql.DB, path string, version int) (string, error) {
	dir := controlPlaneSnapshotDir(path)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create snapshot directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("inspect snapshot directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("snapshot directory %s is not a directory", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure snapshot directory: %w", err)
	}
	name := fmt.Sprintf("pre-migration-v%d-%d.db", version, time.Now().UTC().UnixNano())
	final := filepath.Join(dir, name)
	partial := filepath.Join(dir, "."+name+".partial")
	removePartial := true
	defer func() {
		if removePartial {
			_ = os.Remove(partial)
		}
	}()
	if _, err := db.Exec(`VACUUM INTO ?`, partial); err != nil {
		return "", err
	}
	if err := syncFile(partial, 0o600); err != nil {
		return "", err
	}
	if err := verifyControlPlaneSnapshot(partial, version); err != nil {
		return "", fmt.Errorf("verify snapshot: %w", err)
	}
	if err := os.Rename(partial, final); err != nil {
		return "", fmt.Errorf("publish snapshot: %w", err)
	}
	removePartial = false
	if err := syncDir(dir); err != nil {
		return "", fmt.Errorf("sync snapshot directory: %w", err)
	}
	return final, nil
}

// syncFile sets mode on path and flushes it to stable storage.
func syncFile(path string, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return fmt.Errorf("secure snapshot: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync snapshot: %w", err)
	}
	return file.Close()
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	syncErr := handle.Sync()
	closeErr := handle.Close()
	return errors.Join(syncErr, closeErr)
}

// verifyControlPlaneSnapshot opens a snapshot read-only and checks that it is
// a sound SQLite database at the expected schema version.
func verifyControlPlaneSnapshot(path string, version int) error {
	snapshot, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer snapshot.Close()
	var check string
	if err := snapshot.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil {
		return err
	}
	if check != "ok" {
		return fmt.Errorf("quick_check: %s", check)
	}
	var recorded int
	if err := snapshot.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM control_plane_migrations`).Scan(&recorded); err != nil {
		return err
	}
	if recorded != version {
		return fmt.Errorf("snapshot records schema version %d, want %d", recorded, version)
	}
	return nil
}

// pruneControlPlaneSnapshots keeps the newest keep snapshots: those in the
// snapshot directory and those earlier releases wrote beside the database
// (<db>.pre-migration-<ns>.bak). Leftover partial files are removed.
func pruneControlPlaneSnapshots(path string, keep int) error {
	type snapshot struct {
		path    string
		modTime time.Time
	}
	var snapshots []snapshot
	dir := controlPlaneSnapshotDir(path)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var errs []error
	for _, entry := range entries {
		full := filepath.Join(dir, entry.Name())
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), ".pre-migration-") && strings.HasSuffix(entry.Name(), ".partial") {
			errs = append(errs, os.Remove(full))
			continue
		}
		if !entry.Type().IsRegular() || !controlPlaneSnapshotPattern.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		snapshots = append(snapshots, snapshot{full, info.ModTime()})
	}
	legacy, err := filepath.Glob(path + ".pre-migration-*.bak")
	if err != nil {
		return err
	}
	for _, candidate := range legacy {
		info, err := os.Lstat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		snapshots = append(snapshots, snapshot{candidate, info.ModTime()})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].modTime.After(snapshots[j].modTime) })
	for i := keep; i < len(snapshots); i++ {
		errs = append(errs, os.Remove(snapshots[i].path))
	}
	errs = append(errs, syncDir(filepath.Dir(path)))
	if len(entries) > 0 {
		errs = append(errs, syncDir(dir))
	}
	return errors.Join(errs...)
}

func readControlPlaneBlob(db *sql.DB, name string) ([]byte, bool, error) {
	var payload []byte
	err := db.QueryRow(`SELECT payload FROM state_blobs WHERE name = ?`, name).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return payload, true, nil
}

func writeControlPlaneBlob(db *sql.DB, name string, payload []byte) error {
	_, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES (?, ?, unixepoch(), 1) ON CONFLICT(name) DO UPDATE SET payload=excluded.payload, updated_at=excluded.updated_at, revision=state_blobs.revision+1`, name, payload)
	return err
}

var controlPlaneStateRevisions sync.Map // Tracks read revisions per store
var controlPlaneStateSnapshots sync.Map // Tracks the last durable payload per store

const controlPlaneStateRetryLimit = 3

var errControlPlaneStateRefreshed = errors.New("control-plane state refreshed from durable peer state")

// bindControlPlaneState makes a legacy JSON-backed store use a transactional
// database blob as its live authority. The target must be a pointer to the
// store's persisted value (normally a map). Existing database state wins;
// callers persist the current value after binding to import legacy state.
//
// IMPORTANT: Bound stores must persist through persistBoundControlPlaneState.
// That wrapper owns the revision snapshot, conflict merge, bounded retry, and
// in-memory recovery contract shared by the panel and external worker.
func bindControlPlaneState(store any, db *sql.DB, name string, target any) (bool, error) {
	controlPlaneStateBindings.Store(store, controlPlaneStateBinding{db: db, name: name, target: target})
	payload, revision, err := readControlPlaneBlobWithRevision(db, name)
	if err != nil {
		return false, fmt.Errorf("read control-plane state %s: %w", name, err)
	}
	if revision < 0 {
		controlPlaneStateRevisions.Store(store, int64(-1))
		controlPlaneStateSnapshots.Store(store, []byte(nil))
		return false, nil
	}
	if err := decodeBoundControlPlaneTarget(target, payload); err != nil {
		return false, fmt.Errorf("decode control-plane state %s: %w", name, err)
	}

	// Payload and revision were read together so they describe one snapshot.
	controlPlaneStateRevisions.Store(store, revision)
	controlPlaneStateSnapshots.Store(store, append([]byte(nil), payload...))

	return true, nil
}

// compareAndSwapControlPlaneState persists state while detecting concurrent
// updates. Returns an error if the database state changed since this process
// read it (indicating a concurrent write from another process).
//
// Higher-level store persistence should use persistBoundControlPlaneState,
// which reloads, merges, and retries boundedly after this primitive reports a
// conflict.
func compareAndSwapControlPlaneState(store any, name string, payload []byte, db *sql.DB) (bool, error) {
	_, ok := controlPlaneStateBindings.Load(store)
	if !ok {
		return false, nil
	}

	// Get the revision this process read
	readRevisionAny, hasRevision := controlPlaneStateRevisions.Load(store)
	if !hasRevision {
		return true, errors.New("control-plane state was not bound before persistence")
	}
	readRevision := readRevisionAny.(int64)

	if readRevision < 0 {
		result, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES (?, ?, unixepoch(), 1) ON CONFLICT(name) DO NOTHING`, name, payload)
		if err != nil {
			return true, fmt.Errorf("insert control-plane state %s: %w", name, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return true, err
		}
		if affected != 1 {
			return true, fmt.Errorf("control-plane state %s was created by another process", name)
		}
		controlPlaneStateRevisions.Store(store, int64(1))
		return true, nil
	}
	result, err := db.Exec(`UPDATE state_blobs SET payload = ?, updated_at = unixepoch(), revision = revision + 1 WHERE name = ? AND revision = ?`, payload, name, readRevision)
	if err != nil {
		return true, fmt.Errorf("write control-plane state %s: %w", name, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return true, err
	}
	if affected != 1 {
		return true, fmt.Errorf("control-plane state %s was updated by another process (expected revision %d)", name, readRevision)
	}
	controlPlaneStateRevisions.Store(store, readRevision+1)

	return true, nil
}

func persistBoundControlPlaneState(store any, payload []byte) (bool, error) {
	binding, ok := controlPlaneStateBindings.Load(store)
	if !ok {
		return false, nil
	}
	state := binding.(controlPlaneStateBinding)
	baseAny, _ := controlPlaneStateSnapshots.Load(store)
	base, _ := baseAny.([]byte)
	intended := append([]byte(nil), payload...)

	for attempt := 0; attempt < controlPlaneStateRetryLimit; attempt++ {
		_, err := compareAndSwapControlPlaneState(store, state.name, payload, state.db)
		if err == nil {
			// A conflict merge may have incorporated peer-owned keys. Keep the
			// live legacy store in sync with the exact image now made durable.
			if restoreErr := restoreBoundControlPlaneTarget(state.target, payload); restoreErr != nil {
				return true, fmt.Errorf("decode persisted control-plane state %s: %w", state.name, restoreErr)
			}
			controlPlaneStateSnapshots.Store(store, append([]byte(nil), payload...))
			return true, nil
		}

		remote, revision, readErr := readControlPlaneBlobWithRevision(state.db, state.name)
		if readErr != nil {
			if restoreErr := restoreBoundControlPlaneTarget(state.target, base); restoreErr != nil {
				return true, fmt.Errorf("control-plane state %s reload failed (%v) and base restore failed: %w", state.name, err, restoreErr)
			}
			return true, fmt.Errorf("reload control-plane state %s after CAS failure (%v): %w", state.name, err, readErr)
		}
		merged, mergeErr := mergeControlPlaneState(base, intended, remote)
		if mergeErr != nil {
			if restoreErr := restoreBoundControlPlaneTarget(state.target, remote); restoreErr != nil {
				return true, fmt.Errorf("reload conflicting control-plane state %s: %w (merge failure: %v; CAS failure: %v)", state.name, restoreErr, mergeErr, err)
			}
			controlPlaneStateRevisions.Store(store, revision)
			controlPlaneStateSnapshots.Store(store, append([]byte(nil), remote...))
			return true, fmt.Errorf("%w: control-plane state %s conflict cannot be merged: %v (CAS: %v)", errControlPlaneStateRefreshed, state.name, mergeErr, err)
		}
		payload = merged
		controlPlaneStateRevisions.Store(store, revision)
		if attempt == controlPlaneStateRetryLimit-1 {
			if restoreErr := restoreBoundControlPlaneTarget(state.target, remote); restoreErr != nil {
				return true, fmt.Errorf("reload control-plane state %s after retry exhaustion: %w (CAS failure: %v)", state.name, restoreErr, err)
			}
			controlPlaneStateSnapshots.Store(store, append([]byte(nil), remote...))
			return true, fmt.Errorf("%w: control-plane state %s remained busy after %d retries: %v", errControlPlaneStateRefreshed, state.name, controlPlaneStateRetryLimit, err)
		}
	}
	return true, errors.New("control-plane state persistence retry exhausted")
}

func readControlPlaneBlobWithRevision(db *sql.DB, name string) ([]byte, int64, error) {
	var payload []byte
	var revision int64
	if err := db.QueryRow(`SELECT payload, revision FROM state_blobs WHERE name = ?`, name).Scan(&payload, &revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, -1, nil
		}
		return nil, 0, err
	}
	return payload, revision, nil
}

// controlPlaneStateCodec is implemented by stores whose durable payload is not
// their runtime representation (for example, encrypted secrets). The binder
// hands such stores the durable bytes instead of unmarshalling them directly
// into live state, so a store's persisted image can never leak into memory.
type controlPlaneStateCodec interface {
	restoreControlPlaneState(payload []byte) error
}

func decodeBoundControlPlaneTarget(target any, payload []byte) error {
	if codec, ok := target.(controlPlaneStateCodec); ok {
		return codec.restoreControlPlaneState(payload)
	}
	return json.Unmarshal(payload, target)
}

func restoreBoundControlPlaneTarget(target any, payload []byte) error {
	if target == nil || len(payload) == 0 {
		return nil
	}
	return decodeBoundControlPlaneTarget(target, payload)
}

// mergeControlPlaneState reapplies a local map mutation to the latest durable
// map. Independent key changes are combined, while divergent changes to the
// same key fail closed so a stale whole-object update cannot silently clobber
// peer state. This is the bounded compatibility layer for legacy JSON stores
// while they migrate to relational tables.
func mergeControlPlaneState(base, intended, remote []byte) ([]byte, error) {
	var baseMap, intendedMap, remoteMap map[string]json.RawMessage
	if len(base) > 0 {
		if err := json.Unmarshal(base, &baseMap); err != nil {
			return nil, err
		}
	}
	if err := json.Unmarshal(intended, &intendedMap); err != nil {
		return nil, err
	}
	if len(remote) > 0 {
		if err := json.Unmarshal(remote, &remoteMap); err != nil {
			return nil, err
		}
	}
	if baseMap == nil {
		baseMap = map[string]json.RawMessage{}
	}
	if remoteMap == nil {
		remoteMap = map[string]json.RawMessage{}
	}
	merged := make(map[string]json.RawMessage)
	for key, value := range remoteMap {
		merged[key] = value
	}
	keys := make(map[string]struct{})
	for key := range baseMap {
		keys[key] = struct{}{}
	}
	for key := range intendedMap {
		keys[key] = struct{}{}
	}
	for key := range keys {
		baseValue, hadBase := baseMap[key]
		intendedValue, hasIntended := intendedMap[key]
		remoteValue, hasRemote := remoteMap[key]
		localChanged := hadBase != hasIntended || hadBase && hasIntended && !bytes.Equal(baseValue, intendedValue)
		remoteChanged := hadBase != hasRemote || hadBase && hasRemote && !bytes.Equal(baseValue, remoteValue)
		if !localChanged {
			continue
		}
		if remoteChanged && (hasIntended != hasRemote || hasIntended && !bytes.Equal(intendedValue, remoteValue)) {
			return nil, fmt.Errorf("concurrent control-plane changes conflict at key %q", key)
		}
		if hasIntended {
			merged[key] = intendedValue
		} else {
			delete(merged, key)
		}
	}
	return json.Marshal(merged)
}

func backupControlPlane(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("control-plane source is unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("control-plane source must be a regular file")
	}
	db, err := openControlPlaneDB(source)
	if err != nil {
		return err
	}
	defer db.Close()
	destination, err = filepath.Abs(destination)
	if err != nil || strings.TrimSpace(destination) == "" || filepath.Clean(destination) == string(os.PathSeparator) {
		return errors.New("invalid control-plane backup destination")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return fmt.Errorf("create control-plane backup directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".stepanel-control-plane-*.db")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	defer os.Remove(temporaryPath)
	if _, err := db.Exec(`VACUUM INTO ?`, temporaryPath); err != nil {
		return fmt.Errorf("backup control-plane database: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0600); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("publish control-plane backup: %w", err)
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("open control-plane backup directory: %w", err)
	}
	syncErr := directory.Sync()
	_ = directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync control-plane backup directory: %w", syncErr)
	}
	return nil
}

func verifyControlPlaneBackup(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+abs+"?mode=ro&_pragma=foreign_keys(ON)")
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil {
		return fmt.Errorf("run control-plane integrity check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("control-plane integrity check failed: %s", result)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM control_plane_migrations`).Scan(&count); err != nil || count == 0 {
		return errors.New("control-plane backup has no recorded schema migration")
	}
	return nil
}

// restoreControlPlane publishes a verified SQLite snapshot without replacing
// the existing database until the candidate has passed integrity and schema
// checks. The caller must hold the panel and worker process locks so no open
// process can continue using the old inode.
func restoreControlPlane(source, destination string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil || source == destination {
		return errors.New("control-plane restore source and destination must differ")
	}
	if err := verifyControlPlaneBackup(source); err != nil {
		return fmt.Errorf("verify control-plane restore source: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return fmt.Errorf("create control-plane destination directory: %w", err)
	}
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("control-plane destination must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect control-plane destination: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".stepanel-restore-*.db")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	defer os.Remove(temporaryPath)
	if err := backupControlPlane(source, temporaryPath); err != nil {
		return fmt.Errorf("materialize verified control-plane restore: %w", err)
	}
	if err := verifyControlPlaneBackup(temporaryPath); err != nil {
		return fmt.Errorf("verify materialized control-plane restore: %w", err)
	}
	var previous string
	if _, err := os.Stat(destination); err == nil {
		previous = fmt.Sprintf("%s.pre-restore-%d", destination, time.Now().UTC().UnixNano())
		if err := os.Rename(destination, previous); err != nil {
			return fmt.Errorf("preserve current control-plane database: %w", err)
		}
	}
	restoreErr := os.Rename(temporaryPath, destination)
	if restoreErr == nil {
		restoreErr = verifyControlPlaneBackup(destination)
	}
	if restoreErr != nil {
		_ = os.Remove(destination)
		if previous != "" {
			if err := os.Rename(previous, destination); err != nil {
				return fmt.Errorf("restore failed (%v) and current database recovery failed: %w", restoreErr, err)
			}
		}
		return fmt.Errorf("publish control-plane restore: %w", restoreErr)
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return fmt.Errorf("open control-plane destination directory: %w", err)
	}
	syncErr := directory.Sync()
	_ = directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync control-plane destination directory: %w", syncErr)
	}
	return nil
}
