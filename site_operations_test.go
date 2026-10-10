package stepanel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type siteOperationObservation struct {
	username string
	token    bool
	scopes   []string
	csrf     bool
	deadline time.Duration
	body     string
	path     string
}

func newSiteOperationFixture(t *testing.T, status int, response string) (*App, *siteOperationObservation) {
	t.Helper()
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	seen := &siteOperationObservation{}
	siteOperationRoutes["test.echo"] = siteOperationRoute{
		handler: func(a *App) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				seen.username = a.Auth.UsernameForRequest(r)
				seen.token = a.Auth.IsAPITokenRequest(r)
				seen.scopes, _ = r.Context().Value(apiTokenScopesKey{}).([]string)
				seen.csrf = a.Auth.CSRF(r)
				if deadline, ok := r.Context().Deadline(); ok {
					seen.deadline = time.Until(deadline)
				}
				data, _ := io.ReadAll(r.Body)
				seen.body = string(data)
				seen.path = r.URL.RequestURI()
				w.WriteHeader(status)
				_, _ = w.Write([]byte(response))
			}
		},
		site: siteFromBody,
	}
	t.Cleanup(func() { delete(siteOperationRoutes, "test.echo") })
	app := &App{
		Config: Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl")},
		Auth:   Auth{Enabled: true, Username: "admin"},
		Jobs:   newJobsWithDB(db, 1),
	}
	return app, seen
}

func tokenRequest(method, target, body, user string, scopes ...string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := context.WithValue(r.Context(), apiTokenUsernameKey{}, user)
	ctx = context.WithValue(ctx, apiTokenScopesKey{}, scopes)
	return r.WithContext(ctx)
}

func claimSiteOperation(t *testing.T, app *App) Job {
	t.Helper()
	item, ok, err := app.Jobs.ClaimNext("worker-test", siteOperationKind)
	if err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	return item
}

// A long operation is queued instead of run in the request, then replayed by
// the worker as the original requester with the original body.
func TestSiteOperationQueuesAndReplaysAsTheRequester(t *testing.T) {
	app, seen := newSiteOperationFixture(t, http.StatusOK, `{"commit":"abc123"}`)
	body := `{"site":"shop","ref":"main"}`
	response := httptest.NewRecorder()
	app.siteOperation("test.echo")(response, tokenRequest(http.MethodPost, "/api/test/echo?dry=1", body, "admin", "admin:operate"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d %s, want 202", response.Code, response.Body.String())
	}
	if seen.path != "" {
		t.Fatal("the operation ran inside the HTTP request")
	}
	var queued map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil || queued["job_id"] == "" || queued["status_url"] != "/api/jobs/"+queued["job_id"] || queued["site"] != "shop" {
		t.Fatalf("queued response = %s (%v)", response.Body.String(), err)
	}

	item := claimSiteOperation(t, app)
	if item.ID != queued["job_id"] || item.Kind != siteOperationKind || item.User != "shop" || item.MaxAttempts != 1 {
		t.Fatalf("job = %+v", item)
	}
	output, err := app.handleSiteOperationJob(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != `{"commit":"abc123"}` {
		t.Fatalf("output = %s, want the handler's response", output)
	}
	if seen.username != "admin" || !seen.token || len(seen.scopes) != 1 || seen.scopes[0] != "admin:operate" || !seen.csrf {
		t.Fatalf("replayed identity = %+v", seen)
	}
	if seen.body != body || seen.path != "/api/test/echo?dry=1" {
		t.Fatalf("replayed request = %q %q", seen.path, seen.body)
	}
	if seen.deadline < siteOperationDeadline-time.Minute || seen.deadline > siteOperationDeadline {
		t.Fatalf("operation deadline = %s, want %s", seen.deadline, siteOperationDeadline)
	}
	if job, _ := app.Jobs.Get(item.ID); job.Progress == 0 {
		t.Fatal("the operation start was not recorded")
	}
}

func TestSiteOperationAllowsAssignedCustomerGitDeployment(t *testing.T) {
	app, _ := newSiteOperationFixture(t, http.StatusOK, `{"queued":true}`)
	app.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"customer": {Username: "customer", Plan: "starter", Sites: []string{"shop"}},
	}}
	app.Auth.Accounts = app.Accounts
	response := httptest.NewRecorder()
	app.siteOperation("test.echo")(response, tokenRequest(http.MethodPost, "/api/test/echo", `{"site":"shop"}`, "customer", "deploy:write"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("assigned customer operation status = %d %s, want 202", response.Code, response.Body.String())
	}
}

func TestSiteOperationReplaysBrowserSessionIdentity(t *testing.T) {
	app, seen := newSiteOperationFixture(t, http.StatusOK, "done")
	payload, _ := json.Marshal(siteOperationPayload{Operation: "test.echo", Path: "/api/test/echo", Body: []byte(`{"site":"shop"}`), Site: "shop", Actor: "admin"})
	if _, err := app.Jobs.Enqueue(siteOperationKind, "shop", "", payload, 1); err != nil {
		t.Fatal(err)
	}
	output, err := app.handleSiteOperationJob(context.Background(), claimSiteOperation(t, app))
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != `{"message":"done"}` {
		t.Fatalf("plain-text output = %s, want it wrapped as JSON", output)
	}
	if seen.username != "admin" || seen.token || !seen.csrf || !app.Auth.IsAdministrator(httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.WithValue(context.Background(), replayedIdentityKey{}, "admin"))) {
		t.Fatalf("session replay identity = %+v", seen)
	}
}

func TestSiteOperationFailureCarriesTheHandlerMessage(t *testing.T) {
	for response, want := range map[string]string{
		"invalid release pipeline\n":                         "invalid release pipeline",
		`{"error":"container image not allowed","code":403}`: "container image not allowed",
		"": "Unprocessable Entity",
	} {
		app, _ := newSiteOperationFixture(t, http.StatusUnprocessableEntity, response)
		payload, _ := json.Marshal(siteOperationPayload{Operation: "test.echo", Path: "/api/test/echo", Site: "shop", Actor: "admin"})
		if _, err := app.Jobs.Enqueue(siteOperationKind, "shop", "", payload, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := app.handleSiteOperationJob(context.Background(), claimSiteOperation(t, app)); err == nil || err.Error() != want {
			t.Fatalf("failure for %q = %v, want %q", response, err, want)
		}
	}
}

// A worker that died mid-operation must not silently repeat a deployment.
func TestInterruptedSiteOperationIsNotReplayed(t *testing.T) {
	app, seen := newSiteOperationFixture(t, http.StatusOK, "{}")
	payload, _ := json.Marshal(siteOperationPayload{Operation: "test.echo", Path: "/api/test/echo", Site: "shop", Actor: "admin"})
	if _, err := app.Jobs.Enqueue(siteOperationKind, "shop", "", payload, 1); err != nil {
		t.Fatal(err)
	}
	item := claimSiteOperation(t, app)
	item.Progress = 1 // as persisted by the interrupted attempt
	if _, err := app.handleSiteOperationJob(context.Background(), item); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("interrupted replay = %v", err)
	}
	if seen.path != "" {
		t.Fatal("an interrupted operation was replayed")
	}
}

func TestSiteOperationRefusesBeforeQueueing(t *testing.T) {
	app, _ := newSiteOperationFixture(t, http.StatusOK, "{}")
	for name, request := range map[string]*http.Request{
		"other tenant":   tokenRequest(http.MethodPost, "/api/test/echo", `{"site":"shop"}`, "mallory", "site:deploy"),
		"missing site":   tokenRequest(http.MethodPost, "/api/test/echo", `{"ref":"main"}`, "admin", "admin:operate"),
		"oversized body": tokenRequest(http.MethodPost, "/api/test/echo", `{"site":"shop","pad":"`+strings.Repeat("x", siteOperationBodyLimit)+`"}`, "admin", "admin:operate"),
		"no CSRF token":  httptest.NewRequest(http.MethodPost, "/api/test/echo", strings.NewReader(`{"site":"shop"}`)),
	} {
		response := httptest.NewRecorder()
		app.siteOperation("test.echo")(response, request)
		if response.Code < 400 {
			t.Fatalf("%s: status = %d, want refusal", name, response.Code)
		}
	}
	if stats, err := app.Jobs.QueueStats(); err != nil || stats.Queued != 0 {
		t.Fatalf("refused requests queued jobs: %+v %v", stats, err)
	}
}

func TestSiteOperationServesReadsSynchronously(t *testing.T) {
	app, seen := newSiteOperationFixture(t, http.StatusOK, `{"status":"ok"}`)
	response := httptest.NewRecorder()
	app.siteOperation("test.echo")(response, tokenRequest(http.MethodGet, "/api/test/echo", "", "admin", "admin:read"))
	if response.Code != http.StatusOK || seen.path == "" {
		t.Fatalf("GET status = %d, ran = %v; reads must not be queued", response.Code, seen.path != "")
	}
}

// A queued webhook deployment re-reads the site's webhook policy; removing
// the webhook before the job runs stops it.
func TestQueuedWebhookDeploymentRequiresTheWebhookStillConfigured(t *testing.T) {
	app, seen := newSiteOperationFixture(t, http.StatusOK, "{}")
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "webhooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	app.Webhooks = NewWebhookConfigStore(db)
	if err := app.Webhooks.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(siteOperationPayload{Operation: "test.echo", Path: "/api/test/echo", Body: []byte(`{}`), Site: "shop", Actor: "webhook", Webhook: true})
	if _, err := app.Jobs.Enqueue(siteOperationKind, "shop", "", payload, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := app.handleSiteOperationJob(context.Background(), claimSiteOperation(t, app)); err == nil || !strings.Contains(err.Error(), "webhook") {
		t.Fatalf("deployment for a removed webhook = %v", err)
	}
	if seen.path != "" {
		t.Fatal("a deployment ran after its webhook was removed")
	}
}

// Every routed long operation must be registered with a handler, and its
// owner must be derived from the request.
func TestSiteOperationRoutesNameTheirSite(t *testing.T) {
	for name, route := range siteOperationRoutes {
		if route.handler == nil || route.site == nil {
			t.Fatalf("%s is incomplete", name)
		}
	}
	if site := siteOperationRoutes["composer.install"].site("/api/composer/shop/install", nil); site != "shop" {
		t.Fatalf("composer site = %q", site)
	}
	if site := siteOperationRoutes["git.deploy"].site("/api/sites/git-deploy", []byte(`{"site":"shop"}`)); site != "shop" {
		t.Fatalf("git deploy site = %q", site)
	}
	if site := siteFromBody("", []byte(`{"site":"../etc"}`)); site != "" {
		t.Fatalf("unsafe site accepted: %q", site)
	}
}

func signedWebhook(t *testing.T, site, secret, body, deliveryID string) *http.Request {
	t.Helper()
	timestamp := time.Now().UTC().Format(time.RFC3339)
	digest := hmac.New(sha256.New, []byte(secret))
	_, _ = digest.Write(webhookSignatureMessage(timestamp, deliveryID, []byte(body)))
	r := httptest.NewRequest(http.MethodPost, "/api/sites/git-webhook/"+site, strings.NewReader(body))
	r.Header.Set("X-StePanel-Delivery-ID", deliveryID)
	r.Header.Set("X-StePanel-Delivery-Timestamp", timestamp)
	r.Header.Set("X-StePanel-Signature", "sha256="+hex.EncodeToString(digest.Sum(nil)))
	return r
}

// A signed webhook delivery answers with a queued deployment instead of
// running a checkout inside the sender's request; a body naming another site
// is still refused before anything is queued.
func TestGitWebhookQueuesTheDeployment(t *testing.T) {
	app, _ := newSiteOperationFixture(t, http.StatusOK, "{}")
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "webhooks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	app.Webhooks = NewWebhookConfigStore(db)
	if err := app.Webhooks.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	const secret = "per-site-webhook-secret-0123456789"
	if err := app.Webhooks.SetWebhookConfig("shop", secret, []string{"https://github.com/acme/shop.git"}, []string{"main"}); err != nil {
		t.Fatal(err)
	}
	app.webhookReplayCache = NewWebhookReplayCache(5 * time.Minute)

	response := httptest.NewRecorder()
	app.gitWebhook(response, signedWebhook(t, "shop", secret, `{"site":"other","repository":"https://github.com/acme/shop.git","ref":"main"}`, "delivery-1"))
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-site webhook status = %d %s", response.Code, response.Body.String())
	}

	body := `{"repository":"https://github.com/acme/shop.git","ref":"main"}`
	response = httptest.NewRecorder()
	app.gitWebhook(response, signedWebhook(t, "shop", secret, body, "delivery-2"))
	if response.Code != http.StatusAccepted {
		t.Fatalf("webhook status = %d %s, want 202", response.Code, response.Body.String())
	}
	item := claimSiteOperation(t, app)
	var payload siteOperationPayload
	if err := json.Unmarshal(item.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if item.User != "shop" || payload.Operation != "git.deploy" || !payload.Webhook || payload.Site != "shop" || string(payload.Body) != body {
		t.Fatalf("queued webhook deployment = %+v", payload)
	}
}

// Every registered operation is wired to its route.
func TestSiteOperationsAreRouted(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for name := range siteOperationRoutes {
		if name == "test.echo" {
			continue
		}
		if !strings.Contains(string(source), `app.siteOperation("`+name+`")`) {
			t.Errorf("site operation %s is not routed", name)
		}
	}
	if !strings.Contains(string(source), `app.Auth.Require(app.siteOperation("git.deploy"))`) {
		t.Fatal("Git deployment must use the site-scoped customer authorization wrapper")
	}
	if strings.Contains(string(source), `app.Auth.RequireAdministrator(app.siteOperation("git.deploy"))`) {
		t.Fatal("Git deployment must not be administrator-only")
	}
}
