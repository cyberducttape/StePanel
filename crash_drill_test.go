package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// Gate 5 multi-point crash drills. Each drill re-executes this test binary as
// a child that runs a real workflow and is killed with SIGKILL at one
// instrumented boundary (STEPANEL_KILL_AT). The parent then performs what a
// restart performs (recoverUncleanShutdown, or resuming the durable job) and
// checks the same invariants as the Gate 2 interruption tests.

const crashDrillEnv = "STEPANEL_CRASH_DRILL"

// crashDrillFixture lays out a site deterministically under root so the
// parent and child agree on every path.
func crashDrillFixture(root string) interruptionFixture {
	return interruptionFixture{
		cfg: Config{
			WebRoot:      filepath.Join(root, "www"),
			BackupRoot:   filepath.Join(root, "backups"),
			ImportRoot:   filepath.Join(root, "imports"),
			RecoveryRoot: filepath.Join(root, "recovery"),
			AuditLog:     filepath.Join(root, "audit.jsonl"),
			MaxEntries:   1000,
		},
		site: AuthorizedSite{site: "example"},
		root: root,
	}
}

// TestCrashDrillChild is the child half; it does nothing in a normal run.
func TestCrashDrillChild(t *testing.T) {
	scenario := os.Getenv(crashDrillEnv)
	if scenario == "" {
		t.Skip("crash drill child only")
	}
	root := os.Getenv("STEPANEL_CRASH_DRILL_ROOT")
	f := crashDrillFixture(root)
	ctx := context.Background()
	switch scenario {
	case "backup":
		_, _ = CreateSiteBackupContext(ctx, f.cfg, f.site, false)
	case "restore":
		_, _ = backupRestoreFiles(ctx, f.cfg, os.Getenv("STEPANEL_CRASH_DRILL_BACKUP"), f.site)
	case "terminate":
		app := terminationDrillApp(t, f)
		job := Job{ID: "crash-terminate", StartedAt: time.Unix(1_800_000_000, 0).UTC()}
		job.Payload, _ = json.Marshal(durableSiteTerminationRequest{Site: f.site.Site(), Actor: "admin"})
		_, _ = app.handleSiteTermination(ctx, job)
	case "suspend":
		accounts, err := OpenAccountStore(filepath.Join(root, "accounts.json"))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = (&App{Accounts: accounts}).setAccountSuspended(ctx, "customer", true)
	}
	// Reaching here means the kill point was never hit.
	os.Exit(3)
}

// runCrashChild runs scenario in a child killed at killAt and fails the test
// unless the child died from SIGKILL.
func runCrashChild(t *testing.T, scenario, root, killAt string, extraEnv ...string) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestCrashDrillChild$")
	child.Env = append(os.Environ(), crashDrillEnv+"="+scenario, "STEPANEL_CRASH_DRILL_ROOT="+root, "STEPANEL_KILL_AT="+killAt, "STEPANEL_AUDIT_KEY="+strings.Repeat("k", 32))
	child.Env = append(child.Env, extraEnv...)
	output, err := child.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("child for %s exited %v, want SIGKILL; output: %s", killAt, err, output)
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child for %s exited %v, want SIGKILL (kill point not reached?); output: %s", killAt, err, output)
	}
}

func restartRecovery(t *testing.T, f interruptionFixture) {
	t.Helper()
	manager, err := siteauthority.NewDefaultManager(f.cfg.WebRoot)
	if err != nil {
		t.Fatal(err)
	}
	if failures := recoverUncleanShutdown(f.cfg, manager); len(failures) != 0 {
		t.Fatalf("startup recovery failures: %v", failures)
	}
}

// ageStagingBeyondLiveOperations backdates every staging tree so the
// age-guarded orphan cleanup treats it as abandoned, as it would be once the
// longest possible operation has timed out.
func ageStagingBeyondLiveOperations(t *testing.T, f interruptionFixture) {
	t.Helper()
	old := time.Now().Add(-orphanedStagingMinAge - time.Hour)
	for _, pattern := range []string{
		filepath.Join(f.cfg.WebRoot, "sites", ".stepanel-manager-staging", "*"),
		filepath.Join(f.cfg.WebRoot, "sites", "*", ".stepanel-release-*"),
		filepath.Join(f.cfg.ImportRoot, "*"),
		filepath.Join(f.cfg.BackupRoot, ".backup-*"),
	} {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			if err := os.Chtimes(match, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCrashDrill_Backup(t *testing.T) {
	for _, point := range []string{"init", "archive", "verify", "commit"} {
		t.Run(point, func(t *testing.T) {
			f := crashDrillFixture(t.TempDir())
			f.writeSite(t, versionOne)
			good := f.backup(t)
			time.Sleep(1100 * time.Millisecond)
			f.writeSite(t, versionTwo)

			runCrashChild(t, "backup", f.root, "backup:"+point)
			restartRecovery(t, f)

			f.assertSite(t, versionTwo)
			if got := f.publishedBackups(t); len(got) != 1 || got[0] != good {
				t.Fatalf("published backups after crash = %v, want only %s", got, good)
			}
			ageStagingBeyondLiveOperations(t, f)
			if err := CleanupBackupStages(f.cfg.BackupRoot, orphanedStagingMinAge); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(f.cfg.BackupRoot)
			for _, entry := range entries {
				if entry.Name() != good {
					t.Errorf("backup root holds %s after cleanup", entry.Name())
				}
			}
			f.assertQuiescent(t)
			if _, err := CreateSiteBackupContext(context.Background(), f.cfg, f.site, false); err != nil {
				t.Fatalf("backup after crash recovery: %v", err)
			}
		})
	}
}

func TestCrashDrill_Restore(t *testing.T) {
	for _, point := range []string{"verify", "extract", "activate", "commit"} {
		t.Run(point, func(t *testing.T) {
			t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
			f := crashDrillFixture(t.TempDir())
			f.writeSite(t, versionOne)
			backup := f.backup(t)
			f.writeSite(t, versionTwo)

			runCrashChild(t, "restore", f.root, "restore:"+point, "STEPANEL_CRASH_DRILL_BACKUP="+backup)
			restartRecovery(t, f)

			// A restore killed before its commit is rolled back: the site is
			// exactly what it was before the restore started.
			f.assertSite(t, versionTwo)
			ageStagingBeyondLiveOperations(t, f)
			restartRecovery(t, f)
			if err := CleanupImportStages(f.cfg.ImportRoot, orphanedStagingMinAge); err != nil {
				t.Fatal(err)
			}
			f.assertQuiescent(t)
			if _, err := backupRestoreFiles(context.Background(), f.cfg, backup, f.site); err != nil {
				t.Fatalf("restore after crash recovery: %v", err)
			}
			f.assertSite(t, versionOne)
		})
	}
}

func terminationDrillApp(t *testing.T, f interruptionFixture) *App {
	t.Helper()
	vhostRoot := filepath.Join(f.root, "vhosts")
	helpers := map[string]string{}
	for _, name := range []string{"vhostctl", "proxyctl", "appctl", "sitectl", "dbctl"} {
		helpers[name] = filepath.Join(f.root, "fake-"+name)
	}
	f.cfg.WebServer = "caddy"
	f.cfg.VHostRoot = vhostRoot
	f.cfg.VHostCtl, f.cfg.ProxyCtl, f.cfg.AppCtl, f.cfg.SiteCtl, f.cfg.DBCtl = helpers["vhostctl"], helpers["proxyctl"], helpers["appctl"], helpers["sitectl"], helpers["dbctl"]
	routes, err := OpenRouteStore(filepath.Join(f.root, "routes.json"))
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := OpenAccountStore(filepath.Join(f.root, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := siteauthority.NewDefaultManager(f.cfg.WebRoot)
	if err != nil {
		t.Fatal(err)
	}
	return &App{Config: f.cfg, Auth: Auth{Username: "admin"}, Jobs: NewJobs(), Routes: routes, Accounts: accounts, siteManager: manager}
}

func TestCrashDrill_Termination(t *testing.T) {
	for _, point := range []string{"init", "backup", "database", "routes", "proxies", "tasks", "services", "site-state", "ownership"} {
		t.Run(point, func(t *testing.T) {
			t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
			f := crashDrillFixture(t.TempDir())
			f.writeSite(t, versionOne)
			vhostRoot := filepath.Join(f.root, "vhosts")
			terminationHelpers(t, f.root, vhostRoot)
			routeName := siteVHostConfigName("caddy", f.site.Site(), "www.example.com")
			writeTestFile(t, filepath.Join(vhostRoot, routeName), "managed")
			routes, err := OpenRouteStore(filepath.Join(f.root, "routes.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := routes.save(RouteDesired{Name: routeName, Site: f.site.Site(), Domain: "www.example.com", State: "applied", UpdatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			accounts, err := OpenAccountStore(filepath.Join(f.root, "accounts.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := accounts.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", []string{f.site.Site()}); err != nil {
				t.Fatal(err)
			}

			runCrashChild(t, "terminate", f.root, "terminate:"+point)
			restartRecovery(t, f)

			// The durable job is requeued after a crash; resuming it must
			// finish the termination from its journal.
			app := terminationDrillApp(t, f)
			job := Job{ID: "crash-terminate", StartedAt: time.Unix(1_800_000_000, 0).UTC()}
			job.Payload, _ = json.Marshal(durableSiteTerminationRequest{Site: f.site.Site(), Actor: "admin"})
			if _, err := app.handleSiteTermination(context.Background(), job); err != nil {
				t.Fatalf("resumed termination after crash at %s: %v", point, err)
			}
			if _, err := os.Stat(filepath.Join(f.cfg.WebRoot, "sites", f.site.Site())); !os.IsNotExist(err) {
				t.Fatalf("site tree exists after resumed termination: %v", err)
			}
			if _, err := os.Stat(filepath.Join(vhostRoot, routeName)); !os.IsNotExist(err) {
				t.Fatalf("live route exists after resumed termination: %v", err)
			}
			for _, route := range app.Routes.list() {
				if route.Site == f.site.Site() {
					t.Fatalf("desired route %s survived termination", route.Name)
				}
			}
			if got := f.publishedBackups(t); len(got) != 1 {
				t.Fatalf("published backups = %v, want exactly one termination backup", got)
			}
			f.assertQuiescent(t)
		})
	}
}

func TestCrashDrill_AccountSuspension(t *testing.T) {
	for point, wantSuspended := range map[string]bool{"before-persist": false, "persisted": true} {
		t.Run(point, func(t *testing.T) {
			root := t.TempDir()
			accounts, err := OpenAccountStore(filepath.Join(root, "accounts.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := accounts.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", nil); err != nil {
				t.Fatal(err)
			}
			runCrashChild(t, "suspend", root, "suspend:"+point)
			reopened, err := OpenAccountStore(filepath.Join(root, "accounts.json"))
			if err != nil {
				t.Fatalf("account state unreadable after crash: %v", err)
			}
			account, ok := reopened.Get("customer")
			if !ok || account.Suspended != wantSuspended {
				t.Fatalf("after crash at %s: account=%+v, want suspended=%v", point, account, wantSuspended)
			}
		})
	}
}
