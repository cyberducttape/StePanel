package stepanel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanceledStagingDatabaseRestoreStillRunsCompensation(t *testing.T) {
	root := t.TempDir()
	argsPath := filepath.Join(root, "database-cleanup.args")
	stage := filepath.Join(root, "stage")
	marker := filepath.Join(root, "restore-started")
	if err := os.MkdirAll(filepath.Join(stage, "databases"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "databases", "source.sql"), []byte("CREATE TABLE fixture (id INT);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "dbctl")
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"provision) exit 0 ;;\n" +
		"restore-dump) touch \"" + marker + "\"; exec sleep 30 ;;\n" +
		"drop-managed) printf '%s\\n' \"$@\" > \"" + argsPath + "\" ;;\n" +
		"*) exit 64 ;;\nesac\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	type restoreResult struct {
		created bool
		err     error
	}
	finished := make(chan restoreResult, 1)
	go func() {
		created, err := restoreDatabaseIntoStagingContext(ctx, Config{DBCtl: helper}, stage, RestoreToStagingRequest{
			Database: "source", TargetDatabase: "staging_db", TargetUser: "staging_user",
			TargetPassword: "staging-password-1234567890", Site: "staging",
		})
		finished <- restoreResult{created: created, err: err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if time.Now().After(deadline) {
			cancel()
			t.Fatalf("database import helper did not start: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case result := <-finished:
		if !result.created || result.err == nil {
			t.Fatalf("canceled staged restore = created %t, err %v; want provisioned database and failed import", result.created, result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("staged database restore did not stop after cancellation")
	}
	app := &App{Config: Config{DBCtl: helper}}
	if err := app.cleanupStagingRestoreDatabase("staging_db", "staging_user"); err != nil {
		t.Fatalf("cleanupStagingRestoreDatabase: %v", err)
	}
	got, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read cleanup helper arguments: %v", err)
	}
	if strings.TrimSpace(string(got)) != "drop-managed\nstaging_db\nstaging_user" {
		t.Fatalf("cleanup helper arguments = %q", got)
	}
}

func TestFailedStagingDatabaseCompensationMarksRecoveryRequired(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "dbctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{DBCtl: helper}}
	cleanupErr := app.cleanupStagingRestoreDatabase("staging_db", "staging_user")
	if cleanupErr == nil {
		t.Fatal("database compensation failure was not returned")
	}
	app.markStagingRestoreRecoveryRequired("staging_restore_compensation", cleanupErr, "staging restore compensation is incomplete; startup reconciliation is required")
	if got := app.recovery.get(); got == nil || !strings.Contains(got.Error(), "staging_restore_compensation") {
		t.Fatalf("recovery state = %v, want staged database intervention", got)
	}
	response := httptest.NewRecorder()
	app.operationalHealth(response, httptest.NewRequest(http.MethodGet, "/api/health/operational", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("operational health status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode operational health: %v", err)
	}
	var checks map[string]ReadinessCheck
	if err := json.Unmarshal(body["checks"], &checks); err != nil {
		t.Fatalf("decode operational checks: %v", err)
	}
	if check := checks["recovery_state"]; check.Ready || !strings.Contains(check.Detail, "staging_restore_compensation") {
		t.Fatalf("recovery health check = %+v, want an actionable failed check", check)
	}
}

// stagingRestoreApp builds an App with fake helpers. The vhost helper records
// its arguments and fails when failVhost is set, standing in for a route that
// cannot be activated.
func stagingRestoreApp(t *testing.T, f interruptionFixture, failVhost bool) (*App, string) {
	t.Helper()
	log := filepath.Join(f.root, "vhostctl.log")
	exit := "0"
	if failVhost {
		exit = "1"
	}
	vhostctl := filepath.Join(f.root, "fake-vhostctl")
	if err := os.WriteFile(vhostctl, []byte("#!/usr/bin/env bash\necho \"$@\" >> \""+log+"\"\nexit "+exit+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	sitectl := filepath.Join(f.root, "fake-sitectl")
	if err := os.WriteFile(sitectl, []byte("#!/usr/bin/env bash\necho \"$@\" >> \""+filepath.Join(f.root, "sitectl.log")+"\"\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := f.cfg
	cfg.VHostCtl, cfg.SiteCtl = vhostctl, sitectl
	return &App{Config: cfg, Auth: Auth{Username: "admin"}}, log
}

func postStagingRestore(t *testing.T, app *App, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/backups/restore-to-staging", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, "admin"))
	response := httptest.NewRecorder()
	app.backupRestoreToStaging(response, request)
	return response
}

func TestRestoreToStagingPathRejectsUnsafeSiteBeforeFilesystemLookup(t *testing.T) {
	app := &App{}
	request := httptest.NewRequest(http.MethodPost, "/api/backups/restore-to-staging", nil)
	response := httptest.NewRecorder()
	app.backupRestoreToStagingPath(response, request, RestoreToStagingRequest{
		Site:   "../outside",
		Backup: "backup",
		Domain: "staging.example.org",
	}, filepath.Join(t.TempDir(), "backup"))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unsafe staging site returned HTTP %d, want %d", response.Code, http.StatusUnprocessableEntity)
	}
}

// Restore-to-staging publishes the backup as a separate, non-indexed site with
// its own route, and never touches the production site.
func TestRestoreToStagingPublishesIsolatedCopy(t *testing.T) {
	f := newInterruptionFixture(t)
	f.writeSite(t, versionOne)
	backup := f.backup(t)
	f.writeSite(t, versionTwo)
	app, vhostLog := stagingRestoreApp(t, f, false)

	response := postStagingRestore(t, app, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("staging restore = %d %s", response.Code, response.Body.String())
	}
	f.assertSite(t, versionTwo)
	staging := interruptionFixture{cfg: f.cfg, site: AuthorizedSite{site: "example-staging"}, root: f.root}
	staging.assertSite(t, versionOne)
	if _, err := os.Stat(filepath.Join(f.cfg.WebRoot, "sites", "example-staging", ".stepanel-staging-noindex")); err != nil {
		t.Fatalf("staging site is missing its noindex marker: %v", err)
	}
	calls, _ := os.ReadFile(vhostLog)
	if strings.TrimSpace(string(calls)) != "apply example-staging staging.example.org" {
		t.Fatalf("route helper calls = %q", calls)
	}
	f.assertQuiescent(t)

	// The same staging site cannot be overwritten by a second restore.
	if again := postStagingRestore(t, app, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`); again.Code != http.StatusConflict {
		t.Fatalf("second restore into an existing staging site = %d, want 409", again.Code)
	}
}

// A staging restore whose route cannot be activated leaves no staging site
// behind and does not touch production, so it can simply be retried.
func TestRestoreToStagingRouteFailureLeavesNoHalfSite(t *testing.T) {
	f := newInterruptionFixture(t)
	f.writeSite(t, versionOne)
	backup := f.backup(t)
	f.writeSite(t, versionTwo)
	app, _ := stagingRestoreApp(t, f, true)

	response := postStagingRestore(t, app, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`)
	if response.Code == http.StatusAccepted {
		t.Fatal("staging restore with a failed route reported success")
	}
	f.assertSite(t, versionTwo)
	if _, err := os.Stat(filepath.Join(f.cfg.WebRoot, "sites", "example-staging")); !os.IsNotExist(err) {
		t.Fatalf("staging site directory remains after a failed restore: %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(f.root, "sitectl.log"))
	if !strings.Contains(string(calls), "prepare-root example-staging") || !strings.Contains(string(calls), "delete example-staging") {
		t.Fatalf("site helper calls = %q, want prepare then delete of the staging identity", calls)
	}
	f.assertQuiescent(t)

	retry, _ := stagingRestoreApp(t, f, false)
	if again := postStagingRestore(t, retry, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`); again.Code != http.StatusAccepted {
		t.Fatalf("retry after failed staging restore = %d %s", again.Code, again.Body.String())
	}
}

func TestRestoreToStagingCommitFailureRemovesPublishedRoute(t *testing.T) {
	f := newInterruptionFixture(t)
	f.writeSite(t, versionOne)
	backup := f.backup(t)
	f.writeSite(t, versionTwo)
	app, vhostLog := stagingRestoreApp(t, f, false)
	t.Setenv("STEPANEL_FAIL_AT", "backup:commit")

	response := postStagingRestore(t, app, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`)
	if response.Code == http.StatusAccepted {
		t.Fatal("staging restore with an injected commit failure reported success")
	}
	calls, err := os.ReadFile(vhostLog)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := "apply example-staging staging.example.org\ndelete site-example-staging-staging_example_org.conf\n"
	if string(calls) != wantCalls {
		t.Fatalf("vhost calls = %q, want route publication then journaled deletion %q", calls, wantCalls)
	}
	if got := app.recovery.get(); got != nil {
		t.Fatalf("successful route compensation left recovery intervention: %v", got)
	}
	f.assertSite(t, versionTwo)
}

func TestRestoreToStagingRouteCleanupFailurePersistsIntervention(t *testing.T) {
	f := newInterruptionFixture(t)
	f.writeSite(t, versionOne)
	backup := f.backup(t)
	f.writeSite(t, versionTwo)
	app, _ := stagingRestoreApp(t, f, false)
	vhostctl := filepath.Join(f.root, "failing-route-delete")
	logPath := filepath.Join(f.root, "vhost-failure.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\ncase \"$1\" in\n" +
		"apply) exit 0 ;;\n" +
		"delete) exit 1 ;;\n" +
		"*) exit 64 ;;\nesac\n"
	if err := os.WriteFile(vhostctl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	app.Config.VHostCtl = vhostctl
	t.Setenv("STEPANEL_FAIL_AT", "backup:commit")

	response := postStagingRestore(t, app, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`)
	if response.Code == http.StatusAccepted {
		t.Fatal("staging restore with a failed route cleanup reported success")
	}
	if got := app.recovery.get(); got == nil || !strings.Contains(got.Error(), "route cleanup") {
		t.Fatalf("recovery state = %v, want failed route cleanup", got)
	}
	entries, err := os.ReadDir(f.cfg.RecoveryRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("recovery journal entries = %d, err=%v; want one retained transaction", len(entries), err)
	}
	txn, err := loadSiteTransaction(filepath.Join(f.cfg.RecoveryRoot, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if txn.RecoveryRequired == "" || len(txn.Routes) != 1 || !txn.Routes[0].Applied {
		t.Fatalf("route recovery journal = %+v; want unresolved applied route", txn)
	}
	if _, err := RecoverSiteTransactions(f.cfg.RecoveryRoot, f.cfg.WebRoot, f.cfg.MailRoot); err == nil || !strings.Contains(err.Error(), "requires operator intervention") {
		t.Fatalf("startup recovery error = %v, want durable route intervention", err)
	}
	f.assertSite(t, versionTwo)
}

func TestRestoreToStagingCleanupFailureRequiresIntervention(t *testing.T) {
	f := newInterruptionFixture(t)
	f.writeSite(t, versionOne)
	backup := f.backup(t)
	f.writeSite(t, versionTwo)
	app, _ := stagingRestoreApp(t, f, true)

	// The route activation fails first, then the site helper refuses to remove
	// the staging identity. Exercise the actual handler's compensation defer:
	// a failed request must not leave the control plane reporting healthy when
	// host cleanup could not be verified.
	sitectl := filepath.Join(f.root, "failing-sitectl")
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"prepare-root) exit 0 ;;\n" +
		"delete) exit 1 ;;\n" +
		"*) exit 0 ;;\nesac\n"
	if err := os.WriteFile(sitectl, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	app.Config.SiteCtl = sitectl

	response := postStagingRestore(t, app, `{"site":"example-staging","backup":"`+backup+`","domain":"staging.example.org"}`)
	if response.Code == http.StatusAccepted {
		t.Fatal("staging restore with failed cleanup reported success")
	}
	recoveryState := app.recovery.get()
	if recoveryState == nil || !strings.Contains(recoveryState.Error(), "staging_restore_compensation") {
		t.Fatalf("recovery state = %v, want failed staging compensation recorded", recoveryState)
	}
	entries, err := os.ReadDir(f.cfg.RecoveryRoot)
	if err != nil || len(entries) != 1 {
		t.Fatalf("recovery journal entries = %d, err=%v; want one retained transaction", len(entries), err)
	}
	txn, err := loadSiteTransaction(filepath.Join(f.cfg.RecoveryRoot, entries[0].Name()))
	if err != nil {
		t.Fatalf("load failed-compensation journal: %v", err)
	}
	if !strings.Contains(txn.RecoveryRequired, "site teardown") {
		t.Fatalf("durable intervention = %q, want site teardown failure", txn.RecoveryRequired)
	}
	if _, err := RecoverSiteTransactions(f.cfg.RecoveryRoot, f.cfg.WebRoot, f.cfg.MailRoot); err == nil || !strings.Contains(err.Error(), "requires operator intervention") {
		t.Fatalf("restart recovery scan error = %v, want retained intervention", err)
	}
	if err := CleanupSiteTransactions(f.cfg.RecoveryRoot, time.Nanosecond, f.cfg.WebRoot, f.cfg.MailRoot); err != nil {
		t.Fatalf("cleanup recovery journals: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.cfg.RecoveryRoot, entries[0].Name(), "transaction.json")); err != nil {
		t.Fatalf("cleanup removed unresolved intervention journal: %v", err)
	}
	readiness := httptest.NewRecorder()
	app.operationalHealth(readiness, httptest.NewRequest(http.MethodGet, "/api/health/operational", nil))
	if readiness.Code != http.StatusServiceUnavailable {
		t.Fatalf("operational health = %d, want %d after failed compensation", readiness.Code, http.StatusServiceUnavailable)
	}
	f.assertSite(t, versionTwo)
}
