package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestControlPlaneStateBlobIsTransactionalAndPersistent(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
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
	if _, err := raw.Exec(controlPlaneMigrationsSchema); err != nil {
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
