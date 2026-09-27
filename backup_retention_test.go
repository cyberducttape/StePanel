package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupBackupStagesRemovesOnlyExpiredTemporaryDirectories(t *testing.T) {
	root := t.TempDir()
	oldStage := filepath.Join(root, ".backup-old")
	newStage := filepath.Join(root, ".backup-new")
	ordinary := filepath.Join(root, "published")
	for _, path := range []string{oldStage, newStage, ordinary} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldStage, old, old); err != nil {
		t.Fatal(err)
	}
	if err := CleanupBackupStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldStage); !os.IsNotExist(err) {
		t.Fatalf("expired staging directory still exists: %v", err)
	}
	for _, path := range []string{newStage, ordinary} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("non-expired or published directory changed: %s: %v", path, err)
		}
	}
}

func TestCleanupBackupStagesTreatsMissingRootAsNoOp(t *testing.T) {
	if err := CleanupBackupStages(filepath.Join(t.TempDir(), "missing"), time.Hour); err != nil {
		t.Fatalf("missing backup root cleanup = %v", err)
	}
}

func TestPruneSiteBackupsKeepsNewestAndOtherSites(t *testing.T) {
	root := t.TempDir()
	for _, item := range []struct{ name, site string }{{"20260101-000000.000000000-demo", "demo"}, {"20260102-000000.000000000-demo", "demo"}, {"20260103-000000.000000000-demo", "demo"}, {"20260101-000000.000000000-other", "other"}} {
		dir := filepath.Join(root, item.name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		manifest := BackupManifest{Version: 1, Site: item.site, Archive: "backup.tar.gz"}
		if err := writeBackupManifest(dir, manifest); err != nil {
			t.Fatal(err)
		}
	}
	access := AuthorizedSite{site: "demo"}
	if err := pruneSiteBackups(root, access, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "20260101-000000.000000000-demo")); !os.IsNotExist(err) {
		t.Fatal("oldest demo backup was not pruned")
	}
	for _, name := range []string{"20260102-000000.000000000-demo", "20260103-000000.000000000-demo", "20260101-000000.000000000-other"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("expected %s to remain: %v", name, err)
		}
	}
}
