package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/itchyitchy123/StePanel/internal/migration"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS totp_replay (
    username TEXT PRIMARY KEY,
    last_counter INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
`

type controlPlaneStateBinding struct {
	db   *sql.DB
	name string
}

var controlPlaneStateBindings sync.Map

func openControlPlaneDB(path string) (*sql.DB, error) {
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
		if err := snapshotControlPlaneBeforeMigration(db, path); err != nil {
			return fmt.Errorf("snapshot control-plane database before migrating: %w", err)
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

// snapshotControlPlaneBeforeMigration writes a point-in-time copy of the
// database next to it before any migration runs against existing data. It
// uses the already-open handle rather than reopening the file, since the
// control-plane connection pool is limited to a single connection.
func snapshotControlPlaneBeforeMigration(db *sql.DB, path string) error {
	destination := fmt.Sprintf("%s.pre-migration-%d.bak", path, time.Now().UTC().UnixNano())
	if _, err := db.Exec(`VACUUM INTO ?`, destination); err != nil {
		return err
	}
	return os.Chmod(destination, 0600)
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
	_, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at) VALUES (?, ?, unixepoch()) ON CONFLICT(name) DO UPDATE SET payload=excluded.payload, updated_at=excluded.updated_at`, name, payload)
	return err
}

// bindControlPlaneState makes a legacy JSON-backed store use a transactional
// database blob as its live authority. The target must be a pointer to the
// store's persisted value (normally a map). Existing database state wins;
// callers persist the current value after binding to import legacy state.
func bindControlPlaneState(store any, db *sql.DB, name string, target any) (bool, error) {
	controlPlaneStateBindings.Store(store, controlPlaneStateBinding{db: db, name: name})
	payload, found, err := readControlPlaneBlob(db, name)
	if err != nil {
		return false, fmt.Errorf("read control-plane state %s: %w", name, err)
	}
	if !found {
		return false, nil
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return false, fmt.Errorf("decode control-plane state %s: %w", name, err)
	}
	return true, nil
}

func persistBoundControlPlaneState(store any, payload []byte) (bool, error) {
	binding, ok := controlPlaneStateBindings.Load(store)
	if !ok {
		return false, nil
	}
	state := binding.(controlPlaneStateBinding)
	if err := writeControlPlaneBlob(state.db, state.name, payload); err != nil {
		return true, fmt.Errorf("write control-plane state %s: %w", state.name, err)
	}
	return true, nil
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
