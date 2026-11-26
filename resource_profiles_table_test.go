package stepanel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func resourceTestProfile(site string, cpu int) ResourceProfile {
	return ResourceProfile{Site: site, Account: "alice", CPUPercent: cpu, CPUWeight: 100, MemoryHighMB: 460, MemoryMB: 512, IOWeight: 100, TasksMax: 128, PHPWorkers: 8, DiskMB: 1024, Inodes: 10000, FilesystemQuotaState: "enforced", State: "applied", AppliedAt: time.Date(2026, 11, 26, 9, 0, 0, 123, time.UTC)}
}

// Migration 33 moves the whole-object "resources" blob into one row per
// site, keeping every field, and deletes the blob.
func TestResourceProfilesMigrateFromTheStateBlob(t *testing.T) {
	db := openTestControlPlane(t)
	if _, err := db.Exec(`DELETE FROM resource_profiles`); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]ResourceProfile{"shop": resourceTestProfile("shop", 100), "blog": resourceTestProfile("", 200)}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('resources', ?, unixepoch(), 1)`, payload); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateResourceProfilesBlob(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var blobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM state_blobs WHERE name = 'resources'`).Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("resources blob after migration: %d rows, %v", blobs, err)
	}
	store := &ResourceStore{values: map[string]ResourceProfile{}}
	if err := bindResourceProfiles(store, db); err != nil {
		t.Fatal(err)
	}
	if got := store.values["shop"]; got != resourceTestProfile("shop", 100) {
		t.Fatalf("migrated shop profile = %#v", got)
	}
	if got := store.values["blog"]; got.Site != "blog" || got.CPUPercent != 200 || !got.AppliedAt.Equal(legacy["blog"].AppliedAt) {
		t.Fatalf("migrated blog profile (empty site field) = %#v", got)
	}
}

// Two processes writing different sites both succeed; only a write to a row
// the other process changed conflicts, and it refreshes this process's view.
func TestResourceProfileWritesConflictOnlyOnTheSameSite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control-plane.db")
	panelDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer panelDB.Close()
	workerDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer workerDB.Close()
	panel := &ResourceStore{values: map[string]ResourceProfile{}}
	worker := &ResourceStore{values: map[string]ResourceProfile{}}
	if err := bindResourceProfiles(panel, panelDB); err != nil {
		t.Fatal(err)
	}
	if err := bindResourceProfiles(worker, workerDB); err != nil {
		t.Fatal(err)
	}
	if err := panel.saveProfileLocked(resourceTestProfile("shop", 100)); err != nil {
		t.Fatal(err)
	}
	if err := worker.saveProfileLocked(resourceTestProfile("blog", 100)); err != nil {
		t.Fatalf("a write to another site conflicted: %v", err)
	}
	// The worker has not seen the panel's shop row; deleting the site it
	// does know about still succeeds.
	if err := worker.removeRecoveredSiteProfile("blog"); err != nil {
		t.Fatalf("removing the worker's own site: %v", err)
	}
	// The panel changes shop; the worker, which never read that row, then
	// writes shop and must be refused with a refreshed view.
	if err := panel.saveProfileLocked(resourceTestProfile("shop", 300)); err != nil {
		t.Fatal(err)
	}
	err = worker.saveProfileLocked(resourceTestProfile("shop", 999))
	if !errors.Is(err, errControlPlaneStateRefreshed) {
		t.Fatalf("a write over a row another process created = %v, want a refreshed-state conflict", err)
	}
	if got := worker.values["shop"]; got.CPUPercent != 300 {
		t.Fatalf("the worker's refreshed shop profile = %#v, want the panel's", got)
	}
	if _, ok := worker.values["blog"]; ok {
		t.Fatal("the refreshed view still holds the deleted blog profile")
	}
	// After the refresh the worker's retried write succeeds.
	if err := worker.saveProfileLocked(resourceTestProfile("shop", 400)); err != nil {
		t.Fatalf("retry after refresh: %v", err)
	}
	// A delete of a row the other process changed also conflicts.
	err = panel.removeRecoveredSiteProfile("shop")
	if !errors.Is(err, errControlPlaneStateRefreshed) {
		t.Fatalf("deleting a row another process changed = %v, want a refreshed-state conflict", err)
	}
	if got := panel.values["shop"]; got.CPUPercent != 400 {
		t.Fatalf("the panel's refreshed shop profile = %#v", got)
	}
}

// A legacy pre-database JSON file is imported into an empty table.
func TestResourceProfilesImportTheLegacyFileOnce(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "resource-profiles.json")
	data, err := json.Marshal(map[string]ResourceProfile{"shop": resourceTestProfile("shop", 100)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenResourceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	db := openTestControlPlane(t)
	if err := bindResourceProfiles(store, db); err != nil {
		t.Fatal(err)
	}
	reopened := &ResourceStore{values: map[string]ResourceProfile{}}
	if err := bindResourceProfiles(reopened, db); err != nil {
		t.Fatal(err)
	}
	if got := reopened.values["shop"]; got != resourceTestProfile("shop", 100) {
		t.Fatalf("imported profile = %#v", got)
	}
	// The table now wins over the file.
	if err := reopened.saveProfileLocked(resourceTestProfile("shop", 200)); err != nil {
		t.Fatal(err)
	}
	again, err := OpenResourceStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := bindResourceProfiles(again, db); err != nil {
		t.Fatal(err)
	}
	if got := again.values["shop"]; got.CPUPercent != 200 {
		t.Fatalf("the legacy file overrode the table: %#v", got)
	}
}

// A write queued behind a long transaction fails at its deadline as a
// storage error, not as a conflict, and leaves the persisted view intact.
func TestResourceProfileWriteFailsFastWhenTheConnectionIsHeld(t *testing.T) {
	db := openTestControlPlane(t)
	store := &ResourceStore{values: map[string]ResourceProfile{}}
	if err := bindResourceProfiles(store, db); err != nil {
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
	start := time.Now()
	err = store.saveProfileLocked(resourceTestProfile("shop", 100))
	if err == nil || errors.Is(err, errControlPlaneStateRefreshed) {
		t.Fatalf("write while the connection was held = %v, want a storage error", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the write waited instead of failing at its deadline")
	}
	if _, ok := store.values["shop"]; ok {
		t.Fatal("a failed write left the profile in memory")
	}
	if err := held.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := store.saveProfileLocked(resourceTestProfile("shop", 100)); err != nil {
		t.Fatalf("write after release: %v", err)
	}
}
