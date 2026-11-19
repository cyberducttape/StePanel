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

// convertedSiteOperations are the operations that used to replay their HTTP
// handler in the worker, with a well-formed queued body for each.
type convertedOperation struct {
	path string
	body string
	site string // the job's owning site; "shop" when empty
}

var convertedSiteOperations = map[string]convertedOperation{
	"release.pipeline":          {"/api/sites/release-pipeline", `{"site":"shop","repository":"https://github.com/acme/shop.git","ref":"main","image":"registry.example.org/build@sha256:` + strings.Repeat("a", 64) + `","commands":["make"]}`, ""},
	"git.deploy":                {"/api/sites/git-deploy", `{"site":"shop","repository":"https://github.com/acme/shop.git","ref":"main"}`, ""},
	"staging.create":            {"/api/sites/staging", `{"source":"shop","site":"shop-stg","domain":"stg.shop.example.org","files":true}`, "shop-stg"},
	"staging.promote":           {"/api/sites/staging/promote", `{"source":"shop-stg","site":"shop"}`, ""},
	"backup.restore-to-staging": {"/api/backups/restore-to-staging", `{"site":"shop","backup":"20261101-000000.000000000-shop","domain":"stg.shop.example.org"}`, ""},
	"backup.offsite-to-staging": {"/api/backups/offsite-to-staging", `{"source_site":"shop","site":"shop","backup":"20261101-000000.000000000-shop","domain":"stg.shop.example.org"}`, ""},
}

func durableOperationApp(t *testing.T) *App {
	t.Helper()
	accounts, err := OpenAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create("owner", "a sufficiently long customer password", testTOTPSecret, "professional", []string{"shop", "shop-stg"}); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create("other", "a sufficiently long customer password", testTOTPSecret, "starter", []string{"elsewhere"}); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.CreateMember("owner", "watcher", "a sufficiently long customer password", testTOTPSecret, "viewer"); err != nil {
		t.Fatal(err)
	}
	app := &App{
		Config:   Config{AuditLog: filepath.Join(t.TempDir(), "audit.jsonl"), BackupRoot: t.TempDir()},
		Auth:     Auth{Enabled: true, Username: "admin"},
		Accounts: accounts,
	}
	app.Auth.Accounts = accounts
	return app
}

func (o convertedOperation) owningSite() string {
	if o.site == "" {
		return "shop"
	}
	return o.site
}

func auditMentions(t *testing.T, app *App, action string) bool {
	t.Helper()
	data, err := os.ReadFile(app.Config.AuditLog)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Contains(string(data), action)
}

func TestConvertedSiteOperationsValidateTheirQueuedBody(t *testing.T) {
	for name, operation := range convertedSiteOperations {
		route := siteOperationRoutes[name]
		if err := route.validate([]byte(operation.body)); err != nil {
			t.Errorf("%s rejected a well-formed body: %v", name, err)
		}
		for _, malformed := range []string{
			strings.TrimSuffix(operation.body, "}") + `,"unexpected":true}`,
			operation.body + ` {}`,
			strings.TrimSuffix(operation.body, "}"),
		} {
			if err := route.validate([]byte(malformed)); err == nil {
				t.Errorf("%s accepted malformed body %s", name, malformed)
			}
		}
	}
}

// A worker re-checks ownership: an account that does not own the site (or
// has lost it since queueing) is refused before any host state changes, and
// the denial is audited.
func TestConvertedSiteOperationsRecheckOwnershipWhenTheyRun(t *testing.T) {
	for name, operation := range convertedSiteOperations {
		app := durableOperationApp(t)
		route := siteOperationRoutes[name]
		_, err := route.execute(app, context.Background(), siteOperationCommand{
			Operation: name, Path: operation.path, Site: operation.owningSite(), Body: []byte(operation.body),
		}, siteOperationPrincipal{Actor: "other"})
		var failure *siteOperationError
		if !errors.As(err, &failure) || (failure.status != http.StatusForbidden && failure.status != http.StatusUnprocessableEntity) {
			t.Errorf("%s run by a non-owner = %v, want an authorization refusal", name, err)
			continue
		}
		if !auditMentions(t, app, "tenant.access_denied") {
			t.Errorf("%s did not audit the refused non-owner", name)
		}
	}
}

// A worker re-checks the requester's role and token scopes: a viewer, or a
// token whose scopes no longer allow the operation, is refused.
func TestConvertedSiteOperationsRecheckRoleAndScopeWhenTheyRun(t *testing.T) {
	for name, operation := range convertedSiteOperations {
		for label, principal := range map[string]siteOperationPrincipal{
			"viewer role":     {Actor: "watcher"},
			"read-only token": {Actor: "owner", Token: true, Scopes: []string{"site:read"}},
		} {
			app := durableOperationApp(t)
			_, err := siteOperationRoutes[name].execute(app, context.Background(), siteOperationCommand{
				Operation: name, Path: operation.path, Site: operation.owningSite(), Body: []byte(operation.body),
			}, principal)
			var failure *siteOperationError
			if !errors.As(err, &failure) || (failure.status != http.StatusForbidden && failure.status != http.StatusUnprocessableEntity) {
				t.Errorf("%s run by %s = %v, want a refusal", name, label, err)
			}
		}
	}
}

// The job's owning site is the one the operation may change: a queued body
// naming a different site is refused.
func TestConvertedSiteOperationsRefuseABodyForAnotherSite(t *testing.T) {
	for name, operation := range convertedSiteOperations {
		app := durableOperationApp(t)
		_, err := siteOperationRoutes[name].execute(app, context.Background(), siteOperationCommand{
			Operation: name, Path: operation.path, Site: "elsewhere", Body: []byte(operation.body),
		}, siteOperationPrincipal{Actor: "admin"})
		if err == nil || !strings.HasPrefix(err.Error(), "invalid ") {
			t.Errorf("%s with a body for another site = %v, want an invalid-command refusal", name, err)
		}
	}
}

// A webhook delivery deploys only to the site its signature authorized and
// only repositories and refs in that site's current allowlists.
func TestQueuedWebhookDeploymentKeepsItsSignedSiteBinding(t *testing.T) {
	webhook := &authenticatedWebhook{Site: "shop", Repositories: []string{"https://github.com/acme/shop.git"}, AllowedRefs: []string{"main"}}
	execute := siteOperationRoutes["git.deploy"].execute
	run := func(app *App, site, body string) error {
		_, err := execute(app, context.Background(), siteOperationCommand{
			Operation: "git.deploy", Path: "/api/sites/git-deploy", Site: site, Body: []byte(body),
		}, siteOperationPrincipal{Actor: "webhook", Webhook: webhook})
		return err
	}

	app := durableOperationApp(t)
	if err := run(app, "elsewhere", `{"repository":"https://github.com/acme/shop.git","ref":"main"}`); err == nil || err.Error() != "invalid Git deployment command" {
		t.Fatalf("webhook job for a site other than the signed one = %v", err)
	}

	app = durableOperationApp(t)
	err := run(app, "shop", `{"site":"elsewhere","repository":"https://github.com/acme/shop.git","ref":"main"}`)
	var failure *siteOperationError
	if !errors.As(err, &failure) || failure.status != http.StatusForbidden || !auditMentions(t, app, "webhook.site.mismatch") {
		t.Fatalf("webhook body naming another site = %v, want an audited 403", err)
	}

	for label, body := range map[string]string{
		"repository": `{"repository":"https://github.com/acme/other.git","ref":"main"}`,
		"ref":        `{"repository":"https://github.com/acme/shop.git","ref":"feature"}`,
	} {
		app = durableOperationApp(t)
		err := run(app, "shop", body)
		if !errors.As(err, &failure) || failure.status != http.StatusForbidden || !auditMentions(t, app, "webhook.deploy.unauthorized") {
			t.Errorf("webhook with an unlisted %s = %v, want an audited 403", label, err)
		}
	}
}

func TestDurableSiteRequesterUsesCurrentAccountState(t *testing.T) {
	app := durableOperationApp(t)
	admin := durableSiteRequester{a: app, principal: siteOperationPrincipal{Actor: "admin"}}
	owner := durableSiteRequester{a: app, principal: siteOperationPrincipal{Actor: "owner"}}
	viewer := durableSiteRequester{a: app, principal: siteOperationPrincipal{Actor: "watcher"}}
	token := durableSiteRequester{a: app, principal: siteOperationPrincipal{Actor: "owner", Token: true, Scopes: []string{"site:read"}}}

	if !admin.administrator() || owner.administrator() {
		t.Fatal("only the built-in administrator is an administrator")
	}
	if _, ok := admin.siteAccess("anything"); !ok {
		t.Fatal("administrator was refused site access")
	}
	if _, ok := owner.siteAccess("shop"); !ok || !owner.canAccess("shop") {
		t.Fatal("owner was refused its own site")
	}
	if owner.canAccess("elsewhere") {
		t.Fatal("owner may access another tenant's site")
	}
	if !owner.customerScope("site:deploy") || viewer.customerScope("site:deploy") || token.customerScope("site:deploy") || !token.customerScope("site:read") {
		t.Fatal("role and token scopes were not applied")
	}
	if owner.routeStillAuthorized("shop", "shop.example.org") {
		t.Fatal("a customer route was authorized without a verified domain claim")
	}

	if _, err := app.Accounts.SetSuspended("owner", true); err != nil {
		t.Fatal(err)
	}
	if _, ok := owner.siteAccess("shop"); ok || owner.customerScope("site:deploy") {
		t.Fatal("a suspended account kept access after queueing")
	}
}

func TestSiteOperationErrorsAnswerHTTPWithTheirStatus(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		body   string
	}{
		{operationFailure(http.StatusConflict, "site is busy"), http.StatusConflict, "site is busy\n"},
		{operationFailureCause(http.StatusBadGateway, "staging clone", "shop", "could not copy staging files", errors.New("disk")), http.StatusBadGateway, "could not copy staging files; an administrator can find the cause in the panel log\n"},
		{errors.New("unexpected"), http.StatusInternalServerError, "operation failed\n"},
	} {
		recorder := httptest.NewRecorder()
		writeSiteOperationError(recorder, nil, tc.err)
		if recorder.Code != tc.status || recorder.Body.String() != tc.body {
			t.Errorf("%v answered %d %q, want %d %q", tc.err, recorder.Code, recorder.Body.String(), tc.status, tc.body)
		}
	}
	recorder := httptest.NewRecorder()
	writeSiteOperationError(recorder, httptest.NewRequest(http.MethodPost, "/api/sites/git-deploy", nil), errDeploymentHistoryUnavailable)
	var structured apiError
	if recorder.Code != http.StatusServiceUnavailable || json.Unmarshal(recorder.Body.Bytes(), &structured) != nil || structured.Error != "deployment history is unavailable; no changes were made" {
		t.Fatalf("structured failure = %d %s", recorder.Code, recorder.Body.String())
	}
}
