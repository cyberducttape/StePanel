package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type suspensionTestEnv struct {
	app     *App
	helpers string
	calls   string
}

// newSuspensionTestApp builds an App with a real control-plane database and
// fake site, application and web server helpers that log their arguments.
func newSuspensionTestApp(t *testing.T) suspensionTestEnv {
	t.Helper()
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	dir := t.TempDir()
	db, err := openControlPlaneDB(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := newSiteSuspensionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(dir, "www")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "shop", "public"), 0o755); err != nil {
		t.Fatal(err)
	}
	vhostRoot := filepath.Join(dir, "vhosts")
	if err := os.MkdirAll(vhostRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vhostRoot, "site-shop-shop_example_test.conf"), []byte("<VirtualHost *:80>\n    ServerName shop.example.test\n</VirtualHost>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	helpers := filepath.Join(dir, "bin")
	if err := os.MkdirAll(helpers, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls.log")
	for _, name := range []string{"stepanel-sitectl", "stepanel-appctl", "stepanel-vhostctl"} {
		script := "#!/bin/sh\nprintf '%s %s\\n' \"" + name + "\" \"$*\" >> '" + calls + "'\n[ ! -e '" + helpers + "/fail-" + name + "' ] || exit 1\n"
		if err := os.WriteFile(filepath.Join(helpers, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{
		Config: Config{
			WebRoot: webRoot, VHostRoot: vhostRoot, WebServer: "apache",
			SiteCtl:  filepath.Join(helpers, "stepanel-sitectl"),
			AppCtl:   filepath.Join(helpers, "stepanel-appctl"),
			VHostCtl: filepath.Join(helpers, "stepanel-vhostctl"),
			AuditLog: filepath.Join(dir, "audit.jsonl"),
		},
		Jobs:        newJobsWithDB(db, 1),
		Suspensions: store,
	}
	return suspensionTestEnv{app: app, helpers: helpers, calls: calls}
}

func (e suspensionTestEnv) takeCalls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(e.calls)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.calls); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func suspensionJob(t *testing.T, kind, site string) Job {
	t.Helper()
	payload, err := json.Marshal(durableSiteSuspensionRequest{Site: site, Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	return Job{ID: "job-" + kind, Kind: kind, User: site, Payload: payload}
}

func TestSiteSuspensionStoreTransitions(t *testing.T) {
	env := newSuspensionTestApp(t)
	store := env.app.Suspensions
	if err := store.requestResume("shop", "admin"); !errors.Is(err, errSiteNotSuspended) {
		t.Fatalf("resume of an active site = %v, want errSiteNotSuspended", err)
	}
	if err := store.requestSuspend("shop", "unpaid invoice", "admin"); err != nil {
		t.Fatal(err)
	}
	// A failed suspension can be requested again to re-drive it.
	if err := store.requestSuspend("shop", "unpaid invoice 2", "admin"); err != nil {
		t.Fatalf("re-requesting a suspension in progress = %v", err)
	}
	if err := store.transition("shop", siteSuspending, siteSuspended); err != nil {
		t.Fatal(err)
	}
	if err := store.requestSuspend("shop", "again", "admin"); !errors.Is(err, errSiteSuspensionConflict) {
		t.Fatalf("suspending a suspended site = %v, want conflict", err)
	}
	record, ok, err := store.get("shop")
	if err != nil || !ok || record.State != siteSuspended || record.Reason != "unpaid invoice 2" {
		t.Fatalf("record = %#v, %v, %v", record, ok, err)
	}
	if err := store.requestResume("shop", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.transition("shop", siteSuspending, siteSuspended); err == nil {
		t.Fatal("a stale suspend transition overwrote a resume request")
	}
	if err := store.finishResume("shop"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.get("shop"); ok {
		t.Fatal("resume left the suspension record behind")
	}
}

func TestSuspendedSiteRefusesMutationLocks(t *testing.T) {
	env := newSuspensionTestApp(t)
	app := env.app
	if err := app.Suspensions.requestSuspend("shop", "abuse report", "admin"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := app.acquireSiteMutationLockContext(ctx, "shop"); !errors.Is(err, errSiteSuspended) || !isNonRetryableJobError(err) {
		t.Fatalf("site lock of a suspended site = %v, want non-retryable errSiteSuspended", err)
	}
	if _, _, err := app.tryAcquireSiteMutationLockContext(ctx, "shop"); !errors.Is(err, errSiteSuspended) {
		t.Fatalf("try-lock of a suspended site = %v", err)
	}
	if _, _, err := app.acquireSiteMutationLocksContext(ctx, "shop", "vhost:site-shop-x.conf"); !errors.Is(err, errSiteSuspended) {
		t.Fatalf("compound lock of a suspended site = %v", err)
	}
	for name, attempt := range map[string]func() (func(), error){
		"exempt": func() (func(), error) {
			_, r, err := app.acquireSiteMutationLockContext(withSuspensionExempt(ctx), "shop")
			return r, err
		},
		"other site": func() (func(), error) { _, r, err := app.acquireSiteMutationLockContext(ctx, "blog"); return r, err },
		"non-site": func() (func(), error) {
			_, r, err := app.acquireSiteMutationLockContext(ctx, "database:shop_db")
			return r, err
		},
	} {
		release, err := attempt()
		if err != nil {
			t.Fatalf("%s lock refused: %v", name, err)
		}
		release()
	}
}

func TestSiteSuspendAndResumeJobsDriveHostStepsInOrder(t *testing.T) {
	env := newSuspensionTestApp(t)
	app := env.app
	if err := app.Suspensions.requestSuspend("shop", "unpaid invoice", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.handleSiteSuspendJob(context.Background(), suspensionJob(t, siteSuspendJobKind, "shop")); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"stepanel-vhostctl suspend shop shop.example.test",
		"stepanel-sitectl php-suspend shop",
		"stepanel-appctl suspend shop",
		"stepanel-sitectl login-lock shop",
	}
	if got := env.takeCalls(t); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("suspend steps = %q, want %q", got, want)
	}
	if record, _, _ := app.Suspensions.get("shop"); record.State != siteSuspended {
		t.Fatalf("state after suspend = %q", record.State)
	}

	if err := app.Suspensions.requestResume("shop", "admin"); err != nil {
		t.Fatal(err)
	}
	// A suspend job that runs after the resume request is superseded.
	if _, err := app.handleSiteSuspendJob(context.Background(), suspensionJob(t, siteSuspendJobKind, "shop")); err != nil {
		t.Fatal(err)
	}
	if calls := env.takeCalls(t); len(calls) != 0 {
		t.Fatalf("a superseded suspend job changed the host: %q", calls)
	}
	if _, err := app.handleSiteResumeJob(context.Background(), suspensionJob(t, siteResumeJobKind, "shop")); err != nil {
		t.Fatal(err)
	}
	want = []string{
		"stepanel-sitectl login-unlock shop",
		"stepanel-appctl resume shop",
		"stepanel-sitectl php-resume shop",
		"stepanel-vhostctl resume shop shop.example.test",
	}
	if got := env.takeCalls(t); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("resume steps = %q, want %q", got, want)
	}
	if _, ok, _ := app.Suspensions.get("shop"); ok {
		t.Fatal("resume left the suspension record behind")
	}
	audit, err := os.ReadFile(app.Config.AuditLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{`"site.suspended"`, `"site.resumed"`} {
		if !strings.Contains(string(audit), action) {
			t.Fatalf("audit log lacks %s:\n%s", action, audit)
		}
	}
}

func TestSiteSuspendJobRecordsFailureAndKeepsSiteBlocked(t *testing.T) {
	env := newSuspensionTestApp(t)
	app := env.app
	if err := os.WriteFile(filepath.Join(env.helpers, "fail-stepanel-appctl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.Suspensions.requestSuspend("shop", "abuse report", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.handleSiteSuspendJob(context.Background(), suspensionJob(t, siteSuspendJobKind, "shop")); err == nil {
		t.Fatal("suspend job succeeded although stopping applications failed")
	}
	record, ok, _ := app.Suspensions.get("shop")
	if !ok || record.State != siteSuspending || !strings.Contains(record.LastError, "stop managed applications") {
		t.Fatalf("record after failure = %#v", record)
	}
	if _, _, err := app.acquireSiteMutationLockContext(context.Background(), "shop"); !errors.Is(err, errSiteSuspended) {
		t.Fatalf("a partly suspended site accepted a mutation: %v", err)
	}
}

func TestReconcileSiteSuspensionsReappliesSuspendedSites(t *testing.T) {
	env := newSuspensionTestApp(t)
	app := env.app
	if err := app.Suspensions.requestSuspend("shop", "abuse report", "admin"); err != nil {
		t.Fatal(err)
	}
	if reconciled, failed := app.reconcileSiteSuspensions(context.Background()); len(reconciled) != 0 || len(failed) != 0 {
		t.Fatalf("a suspension still owned by its job was reconciled: %v %v", reconciled, failed)
	}
	if err := app.Suspensions.transition("shop", siteSuspending, siteSuspended); err != nil {
		t.Fatal(err)
	}
	reconciled, failed := app.reconcileSiteSuspensions(context.Background())
	if len(failed) != 0 || len(reconciled) != 1 || reconciled[0] != "shop" {
		t.Fatalf("reconciled = %v, failed = %v", reconciled, failed)
	}
	if calls := env.takeCalls(t); len(calls) != 4 || calls[0] != "stepanel-vhostctl suspend shop shop.example.test" {
		t.Fatalf("reconcile calls = %q", calls)
	}
}

func TestHostReconcilersIgnoreSuspendedSiteRefusals(t *testing.T) {
	failed := map[string]string{
		"shop/worker": errSiteSuspended.Error(),
		"blog/worker": "helper failed",
	}
	dropSuspendedSiteRefusals(failed)
	if _, ok := failed["shop/worker"]; ok || failed["blog/worker"] != "helper failed" || len(failed) != 1 {
		t.Fatalf("filtered failures = %v", failed)
	}
}

func TestSiteSuspensionAPIRequiresAdministratorReasonAndState(t *testing.T) {
	env := newSuspensionTestApp(t)
	auth, cookies := newCSRFTestAuth(t)
	app := env.app
	app.Auth = auth
	app.Config.AuditLog = auth.AuditLog
	post := func(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/sites/suspend", strings.NewReader(body))
		for _, cookie := range cookies {
			request.AddCookie(cookie)
		}
		request.Header.Set("X-CSRF-Token", csrfCookie(cookies))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler(response, request)
		return response
	}
	for name, body := range map[string]string{
		"no reason":       `{"site":"shop"}`,
		"multi-line":      `{"site":"shop","reason":"a\nb"}`,
		"invalid site":    `{"site":"../shop","reason":"x"}`,
		"overlong reason": `{"site":"shop","reason":"` + strings.Repeat("r", siteSuspensionReasonMax+1) + `"}`,
	} {
		if got := post(app.siteSuspend, body).Code; got != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, want 422", name, got)
		}
	}
	if got := post(app.siteSuspend, `{"site":"missing","reason":"x"}`).Code; got != http.StatusNotFound {
		t.Fatalf("suspend of a missing site = %d, want 404", got)
	}
	if got := post(app.siteResume, `{"site":"shop","reason":"paid"}`); got.Code != http.StatusConflict {
		t.Fatalf("resume of an active site = %d, want 409: %s", got.Code, got.Body)
	}
	response := post(app.siteSuspend, `{"site":"shop","reason":"unpaid invoice"}`)
	if response.Code != http.StatusAccepted {
		t.Fatalf("suspend status = %d: %s", response.Code, response.Body)
	}
	var queued map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil || queued["job_id"] == "" {
		t.Fatalf("suspend response = %s", response.Body)
	}
	if job, ok := app.Jobs.Get(queued["job_id"]); !ok || job.Kind != siteSuspendJobKind || job.User != "shop" {
		t.Fatalf("queued job = %#v, %v", job, ok)
	}
	if err := app.Suspensions.transition("shop", siteSuspending, siteSuspended); err != nil {
		t.Fatal(err)
	}
	if got := post(app.siteSuspend, `{"site":"shop","reason":"again"}`).Code; got != http.StatusConflict {
		t.Fatalf("suspend of a suspended site = %d, want 409", got)
	}
	if got := post(app.siteResume, `{"site":"shop","reason":"paid"}`).Code; got != http.StatusAccepted {
		t.Fatalf("resume status = %d", got)
	}
	audit, err := os.ReadFile(auth.AuditLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{`"site.suspend.initiated"`, `"site.suspend"`, `"site.resume.initiated"`} {
		if !strings.Contains(string(audit), action) {
			t.Fatalf("audit log lacks %s", action)
		}
	}
}
