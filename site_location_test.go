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

// Every unit stepanel-appctl writes must use the placement-aware home
// protection: ProtectHome=true hides a placed site's /home document root, and
// its units then fail with 200/CHDIR.
func TestAppUnitsExposeOnlyThePlacedSitesHomePaths(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("deploy", "integrations", "stepanel-appctl"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	if got := strings.Count(script, "\n$home_protection\n"); got != 4 {
		t.Fatalf("app, worker, Python and task units use $home_protection %d times, want 4", got)
	}
	if strings.Count(script, "home_protection='ProtectHome=true'") != 1 || !strings.Contains(script, `home_protection=$'ProtectHome=tmpfs\nBindPaths='"$public_root $site_private"`) {
		t.Fatal("stepanel-appctl must default to ProtectHome=true and bind only a placed site's document root and private state")
	}
}

func TestPlacedSitesUseTheAccountQuotaNotAPerSiteOne(t *testing.T) {
	cfg := placedSiteConfig(t)
	root := t.TempDir()
	calls := filepath.Join(root, "calls")
	stub := filepath.Join(root, "helper")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+calls+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.SiteCtl, cfg.AppCtl, cfg.Sudo = stub, stub, filepath.Join(root, "sudo")
	if err := os.WriteFile(cfg.Sudo, []byte("#!/bin/sh\nwhile [ \"${1#-}\" != \"$1\" ]; do shift; done\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &App{Config: cfg, Accounts: &AccountStore{accounts: map[string]HostingAccount{"alice": {Username: "alice", Plan: "starter"}}}}
	profile := ResourceProfile{Site: "shop", CPUPercent: 100, CPUWeight: 100, MemoryHighMB: 256, MemoryMB: 512, IOWeight: 100, TasksMax: 128, PHPWorkers: 4, DiskMB: 2048, Inodes: 50000}
	if err := a.applyResourceProfile(context.Background(), profile, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "resources shop 4") || strings.Contains(string(data), "quota") {
		t.Fatalf("helper calls = %q; a placed site must not get a per-site quota or quota-clear", data)
	}
	// The account limit is the tenant's plan, not the site profile.
	if disk, inodes, ok := a.accountFilesystemQuota("alice", profile); !ok || disk != hostingPlans["starter"].DiskMB || inodes != hostingPlans["starter"].Inodes {
		t.Fatalf("tenant account quota = %d/%d/%v", disk, inodes, ok)
	}
	// An administrator-created account without a tenant uses the profile.
	if disk, inodes, ok := a.accountFilesystemQuota("solo", profile); !ok || disk != 2048 || inodes != 50000 {
		t.Fatalf("tenantless account quota = %d/%d/%v", disk, inodes, ok)
	}
}
