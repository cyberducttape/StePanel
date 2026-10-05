package stepanel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rewindControlPlaneSchema makes an existing database look like one written
// by the previous release, so reopening it applies the latest migration.
func rewindControlPlaneSchema(t *testing.T, path string) int {
	t.Helper()
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	latest := 0
	for _, m := range controlPlaneMigrations {
		latest = max(latest, m.Version())
	}
	if _, err := db.Exec(`DELETE FROM control_plane_migrations WHERE version = ?`, latest); err != nil {
		t.Fatal(err)
	}
	return latest - 1
}

func TestMigrationSnapshotIsPrivateVerifiedAndDurable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	previous := rewindControlPlaneSchema(t, path)

	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	dir := controlPlaneSnapshotDir(path)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal("no snapshot directory: ", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("snapshot directory mode = %v, want 0700", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !controlPlaneSnapshotPattern.MatchString(entries[0].Name()) {
		t.Fatalf("snapshot directory holds %v, want one published snapshot and no partial file", entries)
	}
	if !strings.HasPrefix(entries[0].Name(), fmt.Sprintf("pre-migration-v%d-", previous)) {
		t.Fatalf("snapshot %s is not named for schema v%d", entries[0].Name(), previous)
	}
	snapshot := filepath.Join(dir, entries[0].Name())
	if info, err := os.Stat(snapshot); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %v, %v; want 0600", info, err)
	}
	if err := verifyControlPlaneSnapshot(snapshot, previous); err != nil {
		t.Fatalf("published snapshot does not verify: %v", err)
	}
	// The documented rollback path accepts it.
	if err := verifyControlPlaneBackup(snapshot); err != nil {
		t.Fatalf("restore-control-plane would reject the snapshot: %v", err)
	}
	if matches, _ := filepath.Glob(path + ".pre-migration-*.bak"); len(matches) != 0 {
		t.Fatalf("snapshot written beside the database: %v", matches)
	}
}

func TestVerifyControlPlaneSnapshotRejectsDamageAndWrongVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM control_plane_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	if _, err := db.Exec(`VACUUM INTO ?`, copyPath); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := verifyControlPlaneSnapshot(copyPath, version); err != nil {
		t.Fatalf("sound snapshot rejected: %v", err)
	}
	if err := verifyControlPlaneSnapshot(copyPath, version-1); err == nil {
		t.Fatal("snapshot with the wrong schema version accepted")
	}
	data, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(t.TempDir(), "truncated.db")
	if err := os.WriteFile(truncated, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyControlPlaneSnapshot(truncated, version); err == nil {
		t.Fatal("truncated snapshot accepted")
	}
}

func TestMigrationSnapshotRetentionKeepsNewest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	dir := controlPlaneSnapshotDir(path)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	write := func(name string, age int) string {
		t.Helper()
		if err := os.WriteFile(name, []byte("snapshot"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := base.Add(time.Duration(age) * time.Minute)
		if err := os.Chtimes(name, at, at); err != nil {
			t.Fatal(err)
		}
		return name
	}
	legacyOld := write(path+".pre-migration-1.bak", 0)
	oldest := write(filepath.Join(dir, "pre-migration-v8-1.db"), 1)
	kept := []string{
		write(path+".pre-migration-2.bak", 2),
		write(filepath.Join(dir, "pre-migration-v9-2.db"), 3),
		write(filepath.Join(dir, "pre-migration-v10-3.db"), 4),
	}
	partial := write(filepath.Join(dir, ".pre-migration-v11-4.db.partial"), 5)
	unrelated := write(filepath.Join(dir, "operator-notes.txt"), 6)

	if err := pruneControlPlaneSnapshots(path, 3); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{legacyOld, oldest, partial} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived retention", filepath.Base(gone))
		}
	}
	for _, stay := range append(kept, unrelated) {
		if _, err := os.Stat(stay); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(stay), err)
		}
	}
}

// Many upgrades leave at most the retention count of snapshots.
func TestRepeatedMigrationsKeepBoundedSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	for i := 0; i < controlPlaneSnapshotRetention+2; i++ {
		rewindControlPlaneSchema(t, path)
		db, err := openControlPlaneDB(path)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	entries, err := os.ReadDir(controlPlaneSnapshotDir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != controlPlaneSnapshotRetention {
		t.Fatalf("%d snapshots kept, want %d", len(entries), controlPlaneSnapshotRetention)
	}
}
