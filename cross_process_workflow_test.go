package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/operations"
	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

// These tests run the real workflow entry points while a second process
// (a separate SQLite connection with its own lock owner, as the panel and
// worker have) holds the lock the conflicting workflow would take. Each
// workflow must wait, give up without mutating anything when its context
// expires, and succeed once the other process releases the lock. The lock
// primitives are tested in site_lock_test.go; these prove the workflows
// actually take them before their first mutation.

const lockWaitBudget = 300 * time.Millisecond

// twoProcessLocks returns lock managers for two owners sharing one
// control-plane database through independent connections.
func twoProcessLocks(t *testing.T) (*operations.DBLocks, *operations.DBLocks) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "control-plane.sqlite") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	open := func(owner string) *operations.DBLocks {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		locks, err := operations.NewDBLocks(db, owner, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return locks
	}
	return open("panel"), open("worker")
}

// holdInOtherProcess takes keys as the other owner and returns its release.
func holdInOtherProcess(t *testing.T, locks *operations.DBLocks, keys ...string) func() {
	t.Helper()
	other := &App{dbLocks: locks}
	release, err := other.acquireSiteMutationLocks(context.Background(), keys...)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			release()
		}
	})
	return func() { released = true; release() }
}

func lockWaitContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), lockWaitBudget)
	t.Cleanup(cancel)
	return ctx
}

func TestCrossProcessLock_RestoreWaitsForSiteLock(t *testing.T) {
	f := newInterruptionFixture(t)
	f.writeSite(t, versionOne)
	backup := f.backup(t)
	f.writeSite(t, versionTwo)
	panelLocks, workerLocks := twoProcessLocks(t)
	app := &App{Config: f.cfg, Auth: Auth{Username: "admin"}, Jobs: NewJobs(), dbLocks: panelLocks}
	job := Job{ID: "restore", StartedAt: time.Now().UTC()}
	job.Payload, _ = json.Marshal(durableBackupRestoreRequest{Mode: "files", Site: f.site.Site(), Backup: backup, Actor: "admin"})

	release := holdInOtherProcess(t, workerLocks, f.site.Site())
	if _, err := app.handleBackupRestoreJob(lockWaitContext(t), job); err == nil {
		t.Fatal("restore ran while another process held the site lock")
	}
	f.assertSite(t, versionTwo)
	f.assertQuiescent(t)

	release()
	if _, err := app.handleBackupRestoreJob(context.Background(), job); err != nil {
		t.Fatalf("restore after the lock was released: %v", err)
	}
	f.assertSite(t, versionOne)
}

func TestCrossProcessLock_TerminationWaitsForSiteLock(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	f := newInterruptionFixture(t)
	f.writeSite(t, versionOne)
	vhostRoot := filepath.Join(f.root, "vhosts")
	helpers := terminationHelpers(t, f.root, vhostRoot)
	f.cfg.WebServer, f.cfg.VHostRoot, f.cfg.AuditLog = "caddy", vhostRoot, filepath.Join(f.root, "audit.jsonl")
	f.cfg.VHostCtl, f.cfg.ProxyCtl, f.cfg.AppCtl, f.cfg.SiteCtl, f.cfg.DBCtl = helpers["vhostctl"], helpers["proxyctl"], helpers["appctl"], helpers["sitectl"], helpers["dbctl"]
	routes, err := OpenRouteStore(filepath.Join(f.root, "routes.json"))
	if err != nil {
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
	panelLocks, workerLocks := twoProcessLocks(t)
	app := &App{Config: f.cfg, Auth: Auth{Username: "admin"}, Jobs: NewJobs(), Routes: routes, Accounts: accounts, siteManager: manager, dbLocks: panelLocks}
	job := Job{ID: "terminate", StartedAt: time.Now().UTC()}
	job.Payload, _ = json.Marshal(durableSiteTerminationRequest{Site: f.site.Site(), Actor: "admin"})

	release := holdInOtherProcess(t, workerLocks, f.site.Site())
	if _, err := app.handleSiteTermination(lockWaitContext(t), job); err == nil {
		t.Fatal("termination ran while another process held the site lock")
	}
	f.assertSite(t, versionOne)
	if account, _ := accounts.Get("customer"); len(account.Sites) != 1 {
		t.Fatalf("termination changed ownership while blocked: %+v", account.Sites)
	}

	release()
	if _, err := app.handleSiteTermination(context.Background(), job); err != nil {
		t.Fatalf("termination after the lock was released: %v", err)
	}
	if _, err := os.Stat(f.public()); !os.IsNotExist(err) {
		t.Fatalf("terminated site still has files: %v", err)
	}
}

// Suspension and a resource update both take the account lock, so whichever
// runs second waits for the first, in either direction.
func TestCrossProcessLock_SuspensionAndResourceUpdateSerialize(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	root := t.TempDir()
	accounts, err := OpenAccountStore(filepath.Join(root, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	resources, err := OpenResourceStore(filepath.Join(root, "resources.json"))
	if err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "appctl.log")
	appctl := filepath.Join(root, "fake-appctl")
	if err := os.WriteFile(appctl, []byte("#!/usr/bin/env bash\necho \"$1\" >> \""+calls+"\"\n[[ $1 == resource-status ]] && echo ActiveState=active\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	sitectl := filepath.Join(root, "fake-sitectl")
	if err := os.WriteFile(sitectl, []byte("#!/usr/bin/env bash\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "www", "sites", "demo", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	panelLocks, workerLocks := twoProcessLocks(t)
	app := &App{
		Auth: Auth{Username: "admin"}, Accounts: accounts, Resources: resources, dbLocks: panelLocks,
		Config: Config{AppCtl: appctl, SiteCtl: sitectl, WebRoot: filepath.Join(root, "www"), AuditLog: filepath.Join(root, "audit.jsonl")},
	}
	resourceUpdate := func(ctx context.Context) int {
		request := httptest.NewRequest(http.MethodPut, "/api/sites/resources/demo", strings.NewReader(`{"cpu_percent":50,"memory_mb":256,"tasks_max":64,"php_workers":4}`))
		request = request.WithContext(context.WithValue(ctx, apiTokenUsernameKey{}, "admin"))
		response := httptest.NewRecorder()
		app.siteResources(response, request)
		return response.Code
	}
	helperCalls := func() string {
		data, err := os.ReadFile(calls)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(data)
	}

	// A suspension in progress elsewhere blocks the resource update.
	release := holdInOtherProcess(t, workerLocks, "account:customer")
	if code := resourceUpdate(lockWaitContext(t)); code != http.StatusConflict {
		t.Fatalf("resource update while another process held the account lock = %d, want 409", code)
	}
	if strings.Contains(helperCalls(), "resource-apply") {
		t.Fatal("resource update reached the host while blocked")
	}
	if _, stored := resources.values["demo"]; stored {
		t.Fatal("resource update changed desired state while blocked")
	}
	release()
	if code := resourceUpdate(context.Background()); code != http.StatusAccepted {
		t.Fatalf("resource update after the lock was released = %d", code)
	}

	// A resource update in progress elsewhere blocks the suspension.
	suspend := func(ctx context.Context) int {
		request := httptest.NewRequest(http.MethodPatch, "/api/accounts/customer", strings.NewReader(`{"suspended":true}`))
		request = request.WithContext(context.WithValue(ctx, apiTokenUsernameKey{}, "admin"))
		response := httptest.NewRecorder()
		app.accounts(response, request)
		return response.Code
	}
	release = holdInOtherProcess(t, workerLocks, resourceMutationLockKeys("demo", "customer")...)
	if code := suspend(lockWaitContext(t)); code != http.StatusConflict {
		t.Fatalf("suspension while another process held the account lock = %d, want 409", code)
	}
	if account, _ := accounts.Get("customer"); account.Suspended {
		t.Fatal("account was suspended while blocked")
	}
	release()
	if code := suspend(context.Background()); code != http.StatusOK {
		t.Fatalf("suspension after the lock was released = %d", code)
	}
	if account, _ := accounts.Get("customer"); !account.Suspended {
		t.Fatal("account was not suspended")
	}
}
