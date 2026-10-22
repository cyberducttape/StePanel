package stepanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

func placedSiteConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	registerSitePlacementDB(db)
	t.Cleanup(func() { registerSitePlacementDB(nil) })
	if _, err := sitelayout.AllocateAccount(db, "alice"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []sitelayout.Placement{
		{Site: "alice", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindMain, Directory: sitelayout.MainDirectory},
		{Site: "shop", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindAddon, Directory: "shop.example.org"},
	} {
		if err := sitelayout.Place(db, p); err != nil {
			t.Fatal(err)
		}
	}
	return Config{WebRoot: filepath.Join(root, "www"), HomeRoot: filepath.Join(root, "home"), GitReleaseRetention: 1}
}

// The Go mapping must match deploy/integrations/stepanel-site-location.sh.
func TestSiteLocationMatchesTheHelperLibrary(t *testing.T) {
	cfg := placedSiteConfig(t)
	shop, err := siteLocationFor(cfg, "shop")
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(cfg.HomeRoot, "alice")
	if !shop.Placed || shop.Home != home || shop.DocRoot != filepath.Join(home, "shop.example.org") ||
		shop.Private != filepath.Join(home, ".stepanel", "sites", "shop") || shop.PreviousPrefix != ".stepanel-previous-shop.example.org-" {
		t.Fatalf("placed location = %+v", shop)
	}
	legacy, err := siteLocationFor(cfg, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(cfg.WebRoot, "sites", "legacy")
	if legacy.Placed || legacy.Home != site || legacy.Private != site || legacy.DocRoot != filepath.Join(site, "public") || legacy.PreviousPrefix != ".stepanel-previous-" {
		t.Fatalf("legacy location = %+v", legacy)
	}
	names, err := allManagedSiteNames(cfg)
	if err != nil || len(names) != 2 {
		t.Fatalf("managed site names = %v, %v; want both placed sites", names, err)
	}
}

func TestPruningAPlacedSiteKeepsOtherSitesReleases(t *testing.T) {
	cfg := placedSiteConfig(t)
	home := filepath.Join(cfg.HomeRoot, "alice")
	old := time.Now().Add(-time.Hour)
	for i, name := range []string{
		".stepanel-previous-shop.example.org-1", ".stepanel-previous-shop.example.org-2",
		".stepanel-previous-public_html-1", ".stepanel-previous-public_html-2",
	} {
		dir := filepath.Join(home, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		stamp := old.Add(time.Duration(i%2) * time.Minute)
		if err := os.Chtimes(dir, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneSiteReleases(cfg, "shop"); err != nil {
		t.Fatal(err)
	}
	for name, kept := range map[string]bool{
		".stepanel-previous-shop.example.org-1": false,
		".stepanel-previous-shop.example.org-2": true,
		".stepanel-previous-public_html-1":      true,
		".stepanel-previous-public_html-2":      true,
	} {
		if _, err := os.Stat(filepath.Join(home, name)); (err == nil) != kept {
			t.Errorf("%s kept=%v, want %v", name, err == nil, kept)
		}
	}
	previous, err := latestPreviousRelease(home, ".stepanel-previous-public_html-")
	if err != nil || filepath.Base(previous) != ".stepanel-previous-public_html-2" {
		t.Fatalf("main site rollback candidate = %s, %v", previous, err)
	}
}

func TestGitReleaseMetricsCountPlacedAndLegacySites(t *testing.T) {
	cfg := placedSiteConfig(t)
	home := filepath.Join(cfg.HomeRoot, "alice")
	for _, dir := range []string{
		filepath.Join(home, ".stepanel-previous-public_html-1"),
		filepath.Join(home, ".stepanel-previous-shop.example.org-1"),
		filepath.Join(home, "public_html"),
		filepath.Join(cfg.WebRoot, "sites", "legacy", ".stepanel-previous-1"),
	} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("release"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	var output strings.Builder
	writeGitReleaseMetrics(&output, cfg)
	for _, want := range []string{"stepanel_git_releases_total 3\n", "stepanel_git_release_bytes 21\n", "stepanel_git_release_inventory_errors 0\n"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, output.String())
		}
	}
}

func TestSiteLocationRejectsUnsafeNames(t *testing.T) {
	cfg := placedSiteConfig(t)
	for _, site := range []string{"", " alice", "alice ", "../alice", "alice/x", "Alice", "a\nb", strings.Repeat("a", 33)} {
		if location, err := siteLocationFor(cfg, site); err == nil {
			t.Errorf("siteLocationFor(%q) = %+v, want an error", site, location)
		}
	}
}

// Restoring a backup into a site placed in a hosting account stages on the
// home filesystem; the manager refuses a legacy-root stage for it.
func TestBackupRestoreFilesIntoAPlacedSite(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	cfg := placedSiteConfig(t)
	root := filepath.Dir(cfg.WebRoot)
	cfg.BackupRoot, cfg.ImportRoot = filepath.Join(root, "backups"), filepath.Join(root, "imports")
	cfg.RecoveryRoot, cfg.AuditLog, cfg.MaxEntries = filepath.Join(root, "recovery"), filepath.Join(root, "audit.jsonl"), 1000
	docRoot := filepath.Join(cfg.HomeRoot, "alice", "public_html")
	writeTestFile(t, filepath.Join(docRoot, "index.html"), "version one")
	site := AuthorizedSite{site: "alice"}
	backup, err := CreateSiteBackup(cfg, site, false)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(docRoot, "index.html"), "version two")
	if _, err := backupRestoreFiles(context.Background(), cfg, filepath.Base(backup.Path), site); err != nil {
		t.Fatalf("restore into placed site: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(docRoot, "index.html")); err != nil || string(data) != "version one" {
		t.Fatalf("restored index.html = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebRoot, "sites", "alice")); !os.IsNotExist(err) {
		t.Fatalf("restore wrote the legacy site tree: %v", err)
	}
}
