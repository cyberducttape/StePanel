package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// Gate 2 acceptance: a full workflow interrupted at each of its instrumented
// boundaries, followed by the conflicting workflow, must leave the site in a
// known good state. These tests drive the real workflow functions (backup,
// file restore, release activation, site deletion) rather than only the lock
// layer, and check the same invariants after every scenario:
//
//   - the live site holds exactly the expected files;
//   - no lifecycle staging or release trees are left under the web root;
//   - no recovery journal is left in flight, so startup recovery has nothing
//     to replay and cannot resurrect or roll back the final state;
//   - no partial backup is published, and the system accepts new work.

type interruptionFixture struct {
	cfg  Config
	site AuthorizedSite
	root string
}

func newInterruptionFixture(t *testing.T) interruptionFixture {
	t.Helper()
	root := t.TempDir()
	cfg := Config{
		WebRoot:      filepath.Join(root, "www"),
		BackupRoot:   filepath.Join(root, "backups"),
		ImportRoot:   filepath.Join(root, "imports"),
		RecoveryRoot: filepath.Join(root, "recovery"),
		MaxEntries:   1000,
	}
	return interruptionFixture{cfg: cfg, site: AuthorizedSite{site: "example"}, root: root}
}

func (f interruptionFixture) public() string {
	return filepath.Join(f.cfg.WebRoot, "sites", f.site.Site(), "public")
}

func (f interruptionFixture) writeSite(t *testing.T, files map[string]string) {
	t.Helper()
	if err := os.RemoveAll(f.public()); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		writeTestFile(t, filepath.Join(f.public(), name), content)
	}
}

func (f interruptionFixture) backup(t *testing.T) string {
	t.Helper()
	result, err := CreateSiteBackupContext(context.Background(), f.cfg, f.site, false)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Base(result.Path)
}

// siteFiles returns the live site's regular files and contents.
func (f interruptionFixture) siteFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(f.public(), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(f.public(), path)
		files[rel] = string(data)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return files
}

func (f interruptionFixture) assertSite(t *testing.T, want map[string]string) {
	t.Helper()
	got := f.siteFiles(t)
	if len(got) != len(want) {
		t.Fatalf("site files = %v, want %v", got, want)
	}
	for name, content := range want {
		if got[name] != content {
			t.Fatalf("site file %s = %q, want %q (all files %v)", name, got[name], content, got)
		}
	}
}

// assertQuiescent checks that nothing is left half-done.
func (f interruptionFixture) assertQuiescent(t *testing.T) {
	t.Helper()
	sites := filepath.Join(f.cfg.WebRoot, "sites")
	entries, err := os.ReadDir(sites)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case name == ".stepanel-recovery":
		case name == ".stepanel-manager-staging":
			// The manager's permanent staging container must hold no trees.
			if staged, _ := os.ReadDir(filepath.Join(sites, name)); len(staged) != 0 {
				t.Errorf("manager staging not discarded: %d tree(s) in %s", len(staged), name)
			}
		case strings.HasPrefix(name, ".stepanel-"):
			t.Errorf("lifecycle staging left behind: %s", name)
		}
	}
	siteEntries, _ := os.ReadDir(filepath.Join(sites, f.site.Site()))
	for _, entry := range siteEntries {
		if strings.HasPrefix(entry.Name(), ".stepanel-release-") {
			t.Errorf("release staging left behind: %s", entry.Name())
		}
	}
	journals, err := os.ReadDir(f.cfg.RecoveryRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, entry := range journals {
		if entry.Name() == "quarantine" {
			continue
		}
		// Committed and rolled-back site transactions are terminal: recovery
		// skips them and retention cleanup removes them. Anything else would
		// be replayed by the next startup.
		if entry.IsDir() {
			if txn, err := loadSiteTransaction(filepath.Join(f.cfg.RecoveryRoot, entry.Name())); err == nil && (txn.State == "committed" || txn.State == "rolled-back") {
				continue
			}
		}
		t.Errorf("recovery journal left in flight: %s", entry.Name())
	}
	imports, _ := os.ReadDir(f.cfg.ImportRoot)
	for _, entry := range imports {
		t.Errorf("restore stage left behind: %s", entry.Name())
	}
}

// publishedBackups lists backups that verify, i.e. what restore could use.
func (f interruptionFixture) publishedBackups(t *testing.T) []string {
	t.Helper()
	backups, err := listBackupsPageUnscoped(f.cfg.BackupRoot, f.site.Site(), 0)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(backups))
	for _, backup := range backups {
		names = append(names, filepath.Base(backup.Path))
	}
	sort.Strings(names)
	return names
}

var (
	versionOne = map[string]string{"index.html": "v1", "assets/app.css": "body{}"}
	versionTwo = map[string]string{"index.html": "v2", "new.txt": "added in v2"}
)

// Backup interrupted by restore: a failed backup at any boundary publishes
// nothing, and the restore that follows produces exactly the restored tree.
func TestWorkflowInterruption_BackupThenRestore(t *testing.T) {
	for _, point := range []string{"init", "archive", "verify", "commit"} {
		t.Run(point, func(t *testing.T) {
			f := newInterruptionFixture(t)
			f.writeSite(t, versionOne)
			good := f.backup(t)
			// Backup names are second-resolution; keep the interrupted attempt
			// distinct from the good one.
			time.Sleep(1100 * time.Millisecond)
			f.writeSite(t, versionTwo)

			t.Setenv("STEPANEL_FAIL_AT", "backup:"+point)
			if _, err := CreateSiteBackupContext(context.Background(), f.cfg, f.site, false); err == nil {
				t.Fatalf("backup interrupted at %s reported success", point)
			}
			t.Setenv("STEPANEL_FAIL_AT", "")

			if _, err := backupRestoreFiles(context.Background(), f.cfg, good, f.site); err != nil {
				t.Fatalf("restore after interrupted backup: %v", err)
			}
			f.assertSite(t, versionOne)
			if got := f.publishedBackups(t); len(got) != 1 || got[0] != good {
				t.Fatalf("published backups = %v, want only %s", got, good)
			}
			time.Sleep(10 * time.Millisecond)
			if err := CleanupBackupStages(f.cfg.BackupRoot, time.Millisecond); err != nil {
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
				t.Fatalf("backup after recovery: %v", err)
			}
		})
	}
}

// Restore interrupted by delete: a failed restore at any boundary leaves the
// live site untouched, and a following delete removes it for good: startup
// recovery has no journal that could bring it back.
func TestWorkflowInterruption_RestoreThenDelete(t *testing.T) {
	for _, point := range []string{"verify", "extract", "activate", "commit"} {
		t.Run(point, func(t *testing.T) {
			f := newInterruptionFixture(t)
			f.writeSite(t, versionOne)
			backup := f.backup(t)
			f.writeSite(t, versionTwo)

			t.Setenv("STEPANEL_FAIL_AT", "restore:"+point)
			if _, err := backupRestoreFiles(context.Background(), f.cfg, backup, f.site); err == nil {
				t.Fatalf("restore interrupted at %s reported success", point)
			}
			t.Setenv("STEPANEL_FAIL_AT", "")
			f.assertSite(t, versionTwo)
			f.assertQuiescent(t)

			manager, err := siteauthority.NewDefaultManager(f.cfg.WebRoot)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Delete(context.Background(), f.site.Site()); err != nil {
				t.Fatalf("delete after interrupted restore: %v", err)
			}
			if recovered, err := RecoverSiteTransactions(f.cfg.RecoveryRoot, f.cfg.WebRoot); err != nil || len(recovered) != 0 {
				t.Fatalf("startup recovery after delete: recovered=%v err=%v", recovered, err)
			}
			if _, err := os.Stat(filepath.Join(f.cfg.WebRoot, "sites", f.site.Site())); !os.IsNotExist(err) {
				t.Fatalf("deleted site exists after recovery: %v", err)
			}
			f.assertQuiescent(t)
		})
	}
}

// Deploy interrupted by restore: a release that fails to activate leaves the
// previous release live and its staging discarded, and the restore that
// follows wins cleanly with no activation journal left to replay.
func TestWorkflowInterruption_DeployThenRestore(t *testing.T) {
	f := newInterruptionFixture(t)
	f.writeSite(t, versionOne)
	backup := f.backup(t)
	f.writeSite(t, versionTwo)
	app := &App{Config: f.cfg}

	release, err := app.createSiteReleaseStaging(context.Background(), f.site.Site(), ".stepanel-release-")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(release, "index.html"), "v3")

	t.Setenv("STEPANEL_FAIL_AT", "deploy:activate")
	if _, err := app.activatePipelineRelease(context.Background(), f.site.Site(), release); err == nil {
		t.Fatal("interrupted release activation reported success")
	}
	t.Setenv("STEPANEL_FAIL_AT", "")
	// The deploy handler discards its staging on every failure path.
	if err := app.discardSiteReleaseStaging(context.Background(), f.site.Site(), release); err != nil {
		t.Fatal(err)
	}
	f.assertSite(t, versionTwo)
	f.assertQuiescent(t)

	if _, err := backupRestoreFiles(context.Background(), f.cfg, backup, f.site); err != nil {
		t.Fatalf("restore after interrupted deploy: %v", err)
	}
	f.assertSite(t, versionOne)
	if recovered, err := recoverReleaseActivationJournals(f.cfg); err != nil || len(recovered) != 0 {
		t.Fatalf("release recovery after restore: recovered=%v err=%v", recovered, err)
	}
	f.assertSite(t, versionOne)
	f.assertQuiescent(t)
}

// fakeResourceHelpers installs stand-ins for stepanel-appctl and
// stepanel-sitectl. Until the "repaired" marker exists, resource-apply fails
// partway through host enforcement, which is how an interrupted resource
// update reaches the host.
func fakeResourceHelpers(t *testing.T, root string) (appctl, sitectl, repaired string) {
	t.Helper()
	repaired = filepath.Join(root, "helpers-repaired")
	appctl = filepath.Join(root, "fake-appctl")
	sitectl = filepath.Join(root, "fake-sitectl")
	script := `#!/usr/bin/env bash
case "$1" in
  resource-apply) [[ -e "` + repaired + `" ]] || { echo "interrupted during slice update" >&2; exit 1; } ;;
  resource-status) echo "ActiveState=active" ;;
esac
exit 0
`
	if err := os.WriteFile(appctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sitectl, []byte("#!/usr/bin/env bash\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return appctl, sitectl, repaired
}

// Resource update interrupted by suspension: an update whose host
// enforcement is cut short stays durably pending (never reported applied),
// the account suspension that follows succeeds, and reconciliation later
// applies the profile without undoing the suspension.
func TestWorkflowInterruption_ResourceUpdateThenSuspension(t *testing.T) {
	root := t.TempDir()
	accounts, err := OpenAccountStore(filepath.Join(root, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	resourcesPath := filepath.Join(root, "resources.json")
	resources, err := OpenResourceStore(resourcesPath)
	if err != nil {
		t.Fatal(err)
	}
	appctl, sitectl, repaired := fakeResourceHelpers(t, root)
	app := &App{
		Auth:      Auth{Username: "admin"},
		Accounts:  accounts,
		Resources: resources,
		Config:    Config{AppCtl: appctl, SiteCtl: sitectl, WebRoot: filepath.Join(root, "www")},
	}
	if err := os.MkdirAll(filepath.Join(root, "www", "sites", "demo", "public"), 0750); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPut, "/api/sites/resources/demo", strings.NewReader(`{"cpu_percent":50,"memory_mb":256,"tasks_max":64,"php_workers":4}`))
	request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, "admin"))
	response := httptest.NewRecorder()
	app.siteResources(response, request)
	if response.Code == http.StatusOK {
		t.Fatalf("interrupted resource update reported success: %s", response.Body.String())
	}

	// The durable desired state says pending, never applied.
	reopened, err := OpenResourceStore(resourcesPath)
	if err != nil {
		t.Fatal(err)
	}
	if profile := reopened.values["demo"]; profile.State != "pending" || profile.MemoryMB != 256 {
		t.Fatalf("durable profile after interruption = %+v, want pending 256 MB", profile)
	}

	if _, err := app.setAccountSuspended(context.Background(), "customer", true); err != nil {
		t.Fatalf("suspension after interrupted resource update: %v", err)
	}
	if account, _ := accounts.Get("customer"); !account.Suspended {
		t.Fatal("account was not suspended")
	}
	if profile := resources.values["demo"]; profile.State != "pending" {
		t.Fatalf("suspension changed the pending profile to %q", profile.State)
	}

	// Reconciliation applies the pending profile once the host recovers.
	if err := os.WriteFile(repaired, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, failed := app.reconcileResourceProfiles(context.Background()); len(failed) != 0 {
		t.Fatalf("reconciliation failures: %v", failed)
	}
	reopened, err = OpenResourceStore(resourcesPath)
	if err != nil {
		t.Fatal(err)
	}
	if profile := reopened.values["demo"]; profile.State != "applied" || profile.MemoryMB != 256 {
		t.Fatalf("durable profile after reconciliation = %+v, want applied 256 MB", profile)
	}
	if account, _ := accounts.Get("customer"); !account.Suspended {
		t.Fatal("reconciliation undid the suspension")
	}
}

// terminationHelpers installs stand-ins for every helper termination calls.
// The vhost helper deletes the named route file like the real one; the site
// helper leaves the tree for SiteManager.Delete to finalize.
func terminationHelpers(t *testing.T, root, vhostRoot string) map[string]string {
	t.Helper()
	write := func(name, body string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return map[string]string{
		"vhostctl": write("fake-vhostctl", `[[ $1 == delete ]] && rm -f "`+vhostRoot+`/$2"; exit 0`),
		"proxyctl": write("fake-proxyctl", "exit 0"),
		"appctl":   write("fake-appctl", "exit 0"),
		"sitectl":  write("fake-sitectl", "exit 0"),
		"dbctl":    write("fake-dbctl", "exit 0"),
	}
}

// Route update interrupted by termination, and termination itself
// interrupted at every journaled step: a route left pending by a failed
// update never outlives the site, and resuming the same termination job
// always completes it.
func TestWorkflowInterruption_RouteUpdateThenTermination(t *testing.T) {
	steps := []string{"init", "backup", "database", "routes", "proxies", "tasks", "services", "site-state", "ownership"}
	for _, step := range steps {
		t.Run(step, func(t *testing.T) {
			t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
			f := newInterruptionFixture(t)
			f.writeSite(t, versionOne)
			vhostRoot := filepath.Join(f.root, "vhosts")
			helpers := terminationHelpers(t, f.root, vhostRoot)
			f.cfg.WebServer = "caddy"
			f.cfg.VHostRoot = vhostRoot
			f.cfg.AuditLog = filepath.Join(f.root, "audit.jsonl")
			f.cfg.VHostCtl, f.cfg.ProxyCtl, f.cfg.AppCtl, f.cfg.SiteCtl, f.cfg.DBCtl = helpers["vhostctl"], helpers["proxyctl"], helpers["appctl"], helpers["sitectl"], helpers["dbctl"]

			// A route update whose helper failed: the live file exists and
			// the desired state is pending with an error.
			routeName := siteVHostConfigName(f.cfg.WebServer, f.site.Site(), "www.example.com")
			writeTestFile(t, filepath.Join(vhostRoot, routeName), "managed")
			routes, err := OpenRouteStore(filepath.Join(f.root, "routes.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := routes.save(RouteDesired{Name: routeName, Site: f.site.Site(), Domain: "www.example.com", State: "pending", LastError: "helper interrupted", UpdatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			accounts, err := OpenAccountStore(filepath.Join(f.root, "accounts.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := accounts.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", []string{f.site.Site()}); err != nil {
				t.Fatal(err)
			}
			manager, err := siteauthority.NewDefaultManager(f.cfg.WebRoot)
			if err != nil {
				t.Fatal(err)
			}
			app := &App{Config: f.cfg, Auth: Auth{Username: "admin"}, Jobs: NewJobs(), Routes: routes, Accounts: accounts, siteManager: manager}
			job := Job{ID: "terminate-" + step, StartedAt: time.Now().UTC()}
			job.Payload, _ = json.Marshal(durableSiteTerminationRequest{Site: f.site.Site(), Actor: "admin"})

			t.Setenv("STEPANEL_FAIL_AT", "terminate:"+step)
			if _, err := app.handleSiteTermination(context.Background(), job); err == nil {
				t.Fatalf("termination interrupted at %s reported success", step)
			}
			t.Setenv("STEPANEL_FAIL_AT", "")

			// Safety invariant at the interruption point: once the site's
			// files are gone, no desired route may remain for the startup
			// reconciler to recreate.
			if _, err := os.Stat(f.public()); os.IsNotExist(err) {
				for _, route := range routes.list() {
					if route.Site == f.site.Site() {
						t.Fatalf("route %s outlived the site's files", route.Name)
					}
				}
			}

			// Resuming the same job completes the termination.
			if _, err := app.handleSiteTermination(context.Background(), job); err != nil {
				t.Fatalf("resumed termination: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.cfg.WebRoot, "sites", f.site.Site())); !os.IsNotExist(err) {
				t.Fatalf("site tree exists after termination: %v", err)
			}
			if _, err := os.Stat(filepath.Join(vhostRoot, routeName)); !os.IsNotExist(err) {
				t.Fatalf("live route exists after termination: %v", err)
			}
			for _, route := range routes.list() {
				if route.Site == f.site.Site() {
					t.Fatalf("desired route %s survived termination", route.Name)
				}
			}
			if owner, owned, _ := accounts.OwnerOfSiteWithError(f.site.Site()); owned {
				t.Fatalf("site still owned by %s", owner)
			}
			if _, reconcileFailures := app.reconcileRoutes(context.Background()); len(reconcileFailures) != 0 {
				t.Fatalf("route reconciliation after termination: %v", reconcileFailures)
			}
			if _, err := os.Stat(filepath.Join(vhostRoot, routeName)); !os.IsNotExist(err) {
				t.Fatal("route reconciliation recreated the terminated site's route")
			}
			if got := f.publishedBackups(t); len(got) != 1 {
				t.Fatalf("published backups = %v, want the one verified termination backup", got)
			}
			audit, _ := os.ReadFile(f.cfg.AuditLog)
			if !strings.Contains(string(audit), "site.terminated") {
				t.Fatal("termination completion was not audited")
			}
			f.assertQuiescent(t)
		})
	}
}
