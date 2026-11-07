package stepanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFlatBackup writes a backup where releases before per-site
// directories kept it: directly in the backup root.
func writeFlatBackup(t *testing.T, root, name, site string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeBackupManifest(dir, BackupManifest{Version: 1, Site: site, Archive: "backup.tar.gz", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBackupPathResolvesTheNamedSitesDirectory(t *testing.T) {
	root := t.TempDir()
	path, err := backupPath(root, "20260101-000000.000000000-my-shop")
	if err != nil || path != filepath.Join(root, "sites", "my-shop", "20260101-000000.000000000-my-shop") {
		t.Fatalf("backupPath = %q, %v", path, err)
	}
	for _, name := range []string{"", "renamed-new", "../20260101-000000.000000000-demo", "20260101-000000.000000000-demo/../x", ".pruning-20260101-000000.000000000-demo"} {
		if _, err := backupPath(root, name); err == nil {
			t.Errorf("backupPath accepted %q", name)
		}
	}
	downloaded := filepath.Join(t.TempDir(), "offsite-restore-1")
	if got, err := (Config{BackupRoot: root}).withDownloadedBackup(downloaded).backupDirectory("20260101-000000.000000000-demo"); err != nil || got != downloaded {
		t.Fatalf("downloaded backup resolved to %q, %v", got, err)
	}
}

// Backups an earlier release left in the root move into their site's
// directory when their manifest proves the owner; anything else stays and
// is reported. A second run has nothing left to move.
func TestMigrateBackupLayoutMovesProvenBackupsOnly(t *testing.T) {
	root := t.TempDir()
	writeFlatBackup(t, root, "20260101-000000.000000000-demo", "demo")
	writeFlatBackup(t, root, "20260102-000000.000000000-demo", "demo")
	writeFlatBackup(t, root, "20260101-000000.000000000-other", "other")
	writeFlatBackup(t, root, "20260103-000000.000000000-demo", "other") // name and manifest disagree
	writeFlatBackup(t, root, "renamed-backup", "demo")                  // not a published name
	damaged := filepath.Join(root, "20260104-000000.000000000-demo")
	if err := os.MkdirAll(damaged, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(damaged, "manifest.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	// A name already taken in the site's directory is not overwritten.
	writeTestBackup(t, root, "20260105-000000.000000000-demo", "demo", time.Now())
	writeFlatBackup(t, root, "20260105-000000.000000000-demo", "demo")
	for _, keep := range []string{".backup-stage", ".database-deletions", ".pruning-20250101-000000.000000000-demo"} {
		if err := os.Mkdir(filepath.Join(root, keep), 0700); err != nil {
			t.Fatal(err)
		}
	}

	report, err := migrateBackupLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Moved != 3 || report.Unattributed != 4 {
		t.Fatalf("migration = %+v, want 3 moved and 4 left", report)
	}
	for _, name := range []string{"20260101-000000.000000000-demo", "20260102-000000.000000000-demo", "20260101-000000.000000000-other"} {
		if _, err := os.Stat(filepath.Join(root, "sites", backupNameSite(name), name, "manifest.json")); err != nil {
			t.Errorf("%s was not moved into its site's directory: %v", name, err)
		}
		if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Errorf("%s is still in the root: %v", name, err)
		}
	}
	for _, name := range []string{"20260103-000000.000000000-demo", "renamed-backup", "20260104-000000.000000000-demo", "20260105-000000.000000000-demo", ".backup-stage", ".database-deletions", ".pruning-20250101-000000.000000000-demo"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Errorf("%s was moved or removed: %v", name, err)
		}
	}
	if again, err := migrateBackupLayout(root); err != nil || again.Moved != 0 || again.Unattributed != 4 {
		t.Fatalf("second migration = %+v, %v", again, err)
	}
}

// Flattening, the downgrade step, puts every backup back in the root and
// finishes interrupted deletions; the result migrates forward again.
func TestFlattenBackupLayoutReversesMigration(t *testing.T) {
	root := t.TempDir()
	writeFlatBackup(t, root, "20260101-000000.000000000-demo", "demo")
	writeFlatBackup(t, root, "20260101-000000.000000000-other", "other")
	if _, err := migrateBackupLayout(root); err != nil {
		t.Fatal(err)
	}
	tombstone := filepath.Join(root, "sites", "demo", backupPruneTombstonePrefix+"20250101-000000.000000000-demo")
	if err := os.MkdirAll(tombstone, 0700); err != nil {
		t.Fatal(err)
	}
	misplaced := filepath.Join(root, "sites", "other", "20260102-000000.000000000-demo")
	if err := os.MkdirAll(misplaced, 0700); err != nil {
		t.Fatal(err)
	}
	report, err := flattenBackupLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Moved != 2 || report.Unattributed != 1 || !strings.Contains(report.Names[0], "20260102-000000.000000000-demo") {
		t.Fatalf("flatten = %+v", report)
	}
	for _, name := range []string{"20260101-000000.000000000-demo", "20260101-000000.000000000-other"} {
		if _, err := os.Stat(filepath.Join(root, name, "manifest.json")); err != nil {
			t.Errorf("%s was not moved back: %v", name, err)
		}
	}
	if _, err := os.Lstat(tombstone); !os.IsNotExist(err) {
		t.Errorf("interrupted deletion was not finished: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "sites", "demo")); !os.IsNotExist(err) {
		t.Errorf("emptied site directory remains: %v", err)
	}
	if _, err := os.Lstat(misplaced); err != nil {
		t.Errorf("misplaced entry was moved or removed: %v", err)
	}
	if again, err := migrateBackupLayout(root); err != nil || again.Moved != 2 {
		t.Fatalf("migrating forward again = %+v, %v", again, err)
	}
}

// A backup is published in its site's directory, and listing one site reads
// only that directory: another site's unreadable entry is never opened, and
// an entry whose manifest names another site is listed for neither.
func TestBackupsArePublishedAndListedPerSite(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	cfg := Config{WebRoot: webRoot, BackupRoot: filepath.Join(root, "backups"), MaxEntries: 1000}
	for _, site := range []string{"demo", "other"} {
		public := filepath.Join(webRoot, "sites", site, "public")
		if err := os.MkdirAll(public, 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(public, "index.html"), []byte(site), 0640); err != nil {
			t.Fatal(err)
		}
	}
	demo, err := CreateSiteBackupContext(context.Background(), cfg, AuthorizedSite{site: "demo"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(demo.Path) != filepath.Join(cfg.BackupRoot, "sites", "demo") {
		t.Fatalf("demo backup published at %s", demo.Path)
	}
	other, err := CreateSiteBackupContext(context.Background(), cfg, AuthorizedSite{site: "other"}, false)
	if err != nil {
		t.Fatal(err)
	}
	// A copy of other's backup placed in demo's directory is not demo's.
	misplaced := filepath.Join(cfg.BackupRoot, "sites", "demo", "20200101-000000.000000000-demo")
	if err := os.Rename(other.Path, misplaced); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() != 0 {
		unreadable := filepath.Join(cfg.BackupRoot, "sites", "third", "20260101-000000.000000000-third")
		if err := os.MkdirAll(unreadable, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(unreadable, "manifest.json"), []byte("{}"), 0); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := listBackupsForSite(cfg.BackupRoot, "demo", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Path != demo.Path {
		t.Fatalf("demo listing = %+v, want only its own backup", listed)
	}
	if listed, err := listBackupsForSite(cfg.BackupRoot, "other", 0); err != nil || len(listed) != 0 {
		t.Fatalf("other listing = %+v, %v; its backup sits in demo's directory", listed, err)
	}
	if all, err := listBackupsForSite(cfg.BackupRoot, "", 0); err != nil || len(all) != 1 {
		t.Fatalf("listing every site = %+v, %v", all, err)
	}
	if resolved, err := cfg.backupDirectory(filepath.Base(demo.Path)); err != nil || resolved != demo.Path {
		t.Fatalf("demo backup resolves to %q, %v", resolved, err)
	}
}

// The flatten command fails while anything an older release could not see
// remains below the per-site directory.
func TestFlattenCommandFailsWhenBackupsRemain(t *testing.T) {
	root := t.TempDir()
	writeFlatBackup(t, root, "20260101-000000.000000000-demo", "demo")
	if _, err := migrateBackupLayout(root); err != nil {
		t.Fatal(err)
	}
	// A backup whose name is already taken in the root cannot move back.
	writeFlatBackup(t, root, "20260101-000000.000000000-demo", "demo")
	if err := runBackupLayoutFlatten(root); err == nil {
		t.Fatal("flatten reported success with a backup left in its site directory")
	}
	if err := os.RemoveAll(filepath.Join(root, "20260101-000000.000000000-demo")); err != nil {
		t.Fatal(err)
	}
	if err := runBackupLayoutFlatten(root); err != nil {
		t.Fatalf("flatten after resolving the conflict: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "sites")); !os.IsNotExist(err) {
		t.Fatalf("per-site directory remains after a successful flatten: %v", err)
	}
}
