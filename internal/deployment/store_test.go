package deployment

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsAndFiltersRecords(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "deployments.json"))
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC()
	if err := store.Add(Record{ID: "one", Site: "alpha", Stage: "build", State: "completed", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Record{ID: "two", Site: "beta", Stage: "activation", State: "completed", CreatedAt: created.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(filepath.Join(filepath.Dir(store.path), "deployments.json"))
	if err != nil {
		t.Fatal(err)
	}
	items := reopened.List("alpha")
	if len(items) != 1 || items[0].ID != "one" {
		t.Fatalf("unexpected filtered records: %#v", items)
	}
	all := reopened.List("")
	if len(all) != 2 || all[0].ID != "two" {
		t.Fatalf("unexpected ordered records: %#v", all)
	}
}

func TestStoreRetainsOriginalStateWhenPersistenceFails(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "deployments.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Record{ID: "one", Site: "alpha", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	store.path = t.TempDir()
	if err := store.Add(Record{ID: "two", Site: "alpha", CreatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("expected persistence failure")
	}
	items := store.List("alpha")
	if len(items) != 1 || items[0].ID != "one" {
		t.Fatalf("failed write changed in-memory state: %#v", items)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE state_blobs (name TEXT PRIMARY KEY, payload BLOB NOT NULL, updated_at INTEGER NOT NULL, revision INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(HistoryTable); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestDBStoresSharingHistoryDoNotLoseRecords(t *testing.T) {
	db := openTestDB(t)
	panel, err := OpenDB(db, "")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := OpenDB(db, "")
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC()
	if err := panel.Add(Record{ID: "panel", Site: "alpha", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	// The worker's cache predates the panel's record; its write must merge
	// with the durable history instead of replacing it.
	if err := worker.Add(Record{ID: "worker", Site: "alpha", CreatedAt: created.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	for name, store := range map[string]*Store{"panel": panel, "worker": worker} {
		items := store.List("alpha")
		if len(items) != 2 || items[0].ID != "worker" || items[1].ID != "panel" {
			t.Fatalf("%s view of shared history = %#v, want worker then panel", name, items)
		}
	}
}

func TestDBStoreRetainsHistoryWhenWriteFails(t *testing.T) {
	db := openTestDB(t)
	store, err := OpenDB(db, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Record{ID: "one", Site: "alpha", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject BEFORE INSERT ON deployment_history BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(Record{ID: "two", Site: "alpha", CreatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("expected persistence failure")
	}
	if items := store.List("alpha"); len(items) != 1 || items[0].ID != "one" {
		t.Fatalf("failed write changed history: %#v", items)
	}
}

// Migration 43 moves the newest-first blob into rows, keeping the order of
// records that share a creation time and the cap, and deletes the blob.
func TestMigrateBlobKeepsOrderAndCap(t *testing.T) {
	db := openTestDB(t)
	base := time.Date(2026, 11, 26, 14, 0, 0, 0, time.UTC)
	var records []Record
	for i := 0; i < maxRecords+5; i++ {
		// Newest first; the two newest share a timestamp.
		created := base.Add(-time.Duration(i) * time.Second)
		if i == 1 {
			created = base
		}
		records = append(records, Record{ID: fmt.Sprintf("r%04d", i), Site: "alpha", Stage: "activate", State: "succeeded", Commit: "c", CreatedAt: created})
	}
	payload, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('deployments', ?, 0, 1)`, payload); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateBlob(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenDB(db, "")
	if err != nil {
		t.Fatal(err)
	}
	items := store.List("alpha")
	if len(items) != maxRecords {
		t.Fatalf("migrated %d records, want the newest %d", len(items), maxRecords)
	}
	if items[0].ID != "r0000" || items[1].ID != "r0001" || items[maxRecords-1].ID != fmt.Sprintf("r%04d", maxRecords-1) {
		t.Fatalf("migrated order = %s, %s ... %s", items[0].ID, items[1].ID, items[maxRecords-1].ID)
	}
	if !items[2].CreatedAt.Equal(records[2].CreatedAt) || items[2].Commit != "c" {
		t.Fatalf("migrated record = %#v", items[2])
	}
	var blobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM state_blobs WHERE name = 'deployments'`).Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("deployments blob after migration: %d, %v", blobs, err)
	}
}

// A legacy JSON history is imported once into an empty table.
func TestOpenDBImportsLegacyHistoryOnce(t *testing.T) {
	db := openTestDB(t)
	path := filepath.Join(t.TempDir(), "deployments.json")
	created := time.Date(2026, 11, 26, 15, 0, 0, 0, time.UTC)
	legacy := []Record{{ID: "new", Site: "alpha", CreatedAt: created}, {ID: "old", Site: "alpha", CreatedAt: created.Add(-time.Hour)}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenDB(db, path)
	if err != nil {
		t.Fatal(err)
	}
	if items := store.List("alpha"); len(items) != 2 || items[0].ID != "new" || items[1].ID != "old" {
		t.Fatalf("imported history = %#v", items)
	}
	// A second open finds the table populated and does not import again.
	again, err := OpenDB(db, path)
	if err != nil {
		t.Fatal(err)
	}
	if items := again.List(""); len(items) != 2 {
		t.Fatalf("history after a second open = %d records, want 2", len(items))
	}
	// A missing legacy file is not an error.
	if _, err := OpenDB(openTestDB(t), filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Fatalf("open with a missing legacy file: %v", err)
	}
}
