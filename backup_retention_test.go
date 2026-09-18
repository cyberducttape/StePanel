package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
