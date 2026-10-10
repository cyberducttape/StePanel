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

	siteauthority "github.com/cyberducttape/StePanel/internal/sites"
)

type siteCreationFixture struct {
	app     *App
	root    string
	webRoot string
	log     string
}

// newSiteCreationFixture builds an App whose site helper is a script that
// records each call and, like stepanel-sitectl, creates the site root on
// prepare and removes it on delete. failOn names an action that fails.
func newSiteCreationFixture(t *testing.T, failOn string) *siteCreationFixture {
	t.Helper()
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites"), 0o750); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "sitectl.log")
	script := `#!/usr/bin/env bash
echo "$@" >> "` + log + `"
[[ "$1" == "` + failOn + `" ]] && exit 1
case "$1" in
  prepare-root) mkdir -p "` + webRoot + `/sites/$2" ;;
  delete) rm -rf "` + webRoot + `/sites/$2" ;;
esac
exit 0
`
	sitectl := filepath.Join(root, "fake-sitectl")
	if err := os.WriteFile(sitectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	app := &App{
		Config: Config{
			WebRoot:      webRoot,
			RecoveryRoot: filepath.Join(root, "recovery"),
			AuditLog:     filepath.Join(root, "audit.jsonl"),
			SiteCtl:      sitectl,
		},
		Auth: Auth{Username: "admin"},
		Jobs: newJobsWithDB(db, 1),
	}
	return &siteCreationFixture{app: app, root: root, webRoot: webRoot, log: log}
}

func (f *siteCreationFixture) helperCalls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

func (f *siteCreationFixture) run(t *testing.T, site string) ([]byte, error) {
	t.Helper()
	item := Job{ID: "create-" + site, Kind: "site.create", StartedAt: time.Now().UTC()}
	item.Payload, _ = json.Marshal(durableSiteCreationRequest{Site: site, Template: "php", Actor: "admin"})
	return f.app.handleSiteCreation(context.Background(), item)
}

func pendingTransactions(t *testing.T, recoveryRoot string) []string {
	t.Helper()
	entries, err := os.ReadDir(recoveryRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var pending []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "quarantine" {
			continue
		}
		txn, err := loadSiteTransaction(filepath.Join(recoveryRoot, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if txn.State != "committed" && txn.State != "rolled-back" {
			pending = append(pending, txn.ID)
		}
	}
	return pending
}

func TestSiteCreationPublishesIsolatedBlankPHPSite(t *testing.T) {
	f := newSiteCreationFixture(t, "")
	output, err := f.run(t, "fresh-site")
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(f.webRoot, "sites", "fresh-site", "public", "index.php"))
	if err != nil {
		t.Fatal("placeholder page was not published: ", err)
	}
	if !strings.Contains(string(index), "PHP_VERSION") || strings.Contains(string(index), "example.") {
		t.Fatalf("unexpected placeholder page:\n%s", index)
	}
	if calls := f.helperCalls(t); calls != "prepare-root fresh-site\nseal fresh-site\n" {
		t.Fatalf("site helper calls = %q, want prepare then seal", calls)
	}
	if pending := pendingTransactions(t, f.app.Config.RecoveryRoot); len(pending) != 0 {
		t.Fatalf("creation left pending recovery transactions: %v", pending)
	}
	var result SiteCreationResult
	if err := json.Unmarshal(output, &result); err != nil || result.Site != "fresh-site" || len(result.NextSteps) == 0 {
		t.Fatalf("result = %s, %v", output, err)
	}
	audit, err := os.ReadFile(f.app.Config.AuditLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{`"site.created.initiated"`, `"site.created"`} {
		if !strings.Contains(string(audit), action) {
			t.Errorf("audit log has no %s event", action)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(f.webRoot, "sites", ".stepanel-create-*")); len(leftovers) != 0 {
		t.Fatalf("staging left behind: %v", leftovers)
	}
}

func TestCustomerCanQueueSiteCreationWithinAssignedPlan(t *testing.T) {
	f := newSiteCreationFixture(t, "")
	accounts, err := OpenAccountStore(filepath.Join(f.root, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", nil); err != nil {
		t.Fatal(err)
	}
	f.app.Accounts = accounts
	f.app.Auth.Accounts = accounts
	request := httptest.NewRequest(http.MethodPost, "/api/sites", strings.NewReader(`{"site":"customer-site","template":"php"}`))
	request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, "customer"))
	response := httptest.NewRecorder()
	f.app.siteCreate(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("customer site creation status = %d, body = %s", response.Code, response.Body.String())
	}
	account, ok := accounts.Get("customer")
	if !ok || len(account.Sites) != 1 || account.Sites[0] != "customer-site" {
		t.Fatalf("customer assignment = %#v, exists=%v", account, ok)
	}
	var queued map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil || queued["job_id"] == "" {
		t.Fatalf("queue response = %s, %v", response.Body, err)
	}
	job, ok := f.app.Jobs.Get(queued["job_id"])
	if !ok || job.Kind != "site.create" || job.User != "customer-site" {
		t.Fatalf("queued site job = %#v, exists=%v", job, ok)
	}
}

// A failure after the helper prepared the site removes everything this job
// created: the published tree, the account and site root (through the
// helper's delete), and the staging tree.
func TestSiteCreationFailureRemovesPreparedSite(t *testing.T) {
	f := newSiteCreationFixture(t, "seal")
	if _, err := f.run(t, "fresh-site"); err == nil {
		t.Fatal("creation succeeded although sealing failed")
	}
	if calls := f.helperCalls(t); calls != "prepare-root fresh-site\nseal fresh-site\ndelete fresh-site\n" {
		t.Fatalf("site helper calls = %q, want prepare, seal, then delete", calls)
	}
	if _, err := os.Lstat(filepath.Join(f.webRoot, "sites", "fresh-site")); !os.IsNotExist(err) {
		t.Fatalf("partially created site remains: %v", err)
	}
	if pending := pendingTransactions(t, f.app.Config.RecoveryRoot); len(pending) != 0 {
		t.Fatalf("failed creation left pending recovery transactions: %v", pending)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(f.webRoot, "sites", ".stepanel-create-*")); len(leftovers) != 0 {
		t.Fatalf("staging left behind: %v", leftovers)
	}
	audit, _ := os.ReadFile(f.app.Config.AuditLog)
	if !strings.Contains(string(audit), `"site.created.failed"`) {
		t.Fatal("failed creation has no site.created.failed audit event")
	}
}

func TestTerminalCustomerSiteCreationFailureReleasesAssignment(t *testing.T) {
	f := newSiteCreationFixture(t, "seal")
	accounts, err := OpenAccountStore(filepath.Join(f.root, "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", []string{"fresh-site"}); err != nil {
		t.Fatal(err)
	}
	f.app.Accounts = accounts
	f.app.Auth.Accounts = accounts
	item := Job{ID: "create-fresh-site", Kind: "site.create", MaxAttempts: 1, StartedAt: time.Now().UTC()}
	item.Payload, _ = json.Marshal(durableSiteCreationRequest{Site: "fresh-site", Template: "php", Actor: "customer", CustomerProvisioning: true})
	if _, err := f.app.handleSiteCreation(context.Background(), item); err == nil {
		t.Fatal("terminal site creation unexpectedly succeeded")
	}
	if account, ok := accounts.Get("customer"); !ok || len(account.Sites) != 0 {
		t.Fatalf("customer assignment after terminal failure = %#v, exists=%v", account, ok)
	}
}

func TestSiteCreationRefusesExistingSite(t *testing.T) {
	f := newSiteCreationFixture(t, "")
	existing := filepath.Join(f.webRoot, "sites", "taken", "public")
	if err := os.MkdirAll(existing, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "index.html"), []byte("live"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := f.run(t, "taken"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("creation over an existing site err = %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(existing, "index.html")); string(data) != "live" {
		t.Fatal("existing site was modified")
	}
	if calls := f.helperCalls(t); calls != "" {
		t.Fatalf("site helper ran for an existing site: %q", calls)
	}
}

// A worker killed after activation leaves a journal; startup recovery rolls
// the creation back completely, including the prepared account.
func TestStartupRecoveryRemovesInterruptedSiteCreation(t *testing.T) {
	f := newSiteCreationFixture(t, "")
	canonical := filepath.Join(f.webRoot, "sites", "half-made", "public")
	txn, err := BeginSiteTransaction(f.app.Config.RecoveryRoot, canonical, siteCreationTransactionKind, AuthorizedSite{site: "half-made"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(canonical, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := txn.persist(); err != nil {
		t.Fatal(err)
	}
	manager, err := siteauthority.NewDefaultManager(f.webRoot)
	if err != nil {
		t.Fatal(err)
	}
	if failures := recoverUncleanShutdown(f.app.Config, manager, f.app.acquireSiteMutationLockContext); len(failures) != 0 {
		t.Fatalf("recovery failures: %v", failures)
	}
	if _, err := os.Lstat(filepath.Join(f.webRoot, "sites", "half-made")); !os.IsNotExist(err) {
		t.Fatalf("interrupted site creation remains after recovery: %v", err)
	}
	if calls := f.helperCalls(t); !strings.Contains(calls, "delete half-made") {
		t.Fatalf("recovery did not remove the prepared site account: %q", calls)
	}
}

func postSiteCreate(t *testing.T, app *App, body, user string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/sites", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, user))
	response := httptest.NewRecorder()
	app.sites(response, request)
	return response
}

func TestSiteCreateEndpointValidatesAndQueues(t *testing.T) {
	f := newSiteCreationFixture(t, "")
	if err := os.MkdirAll(filepath.Join(f.webRoot, "sites", "taken"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"invalid name":         {`{"site":"../etc"}`, http.StatusUnprocessableEntity},
		"uppercase name":       {`{"site":"Site"}`, http.StatusUnprocessableEntity},
		"unsupported template": {`{"site":"new-site","template":"cobol"}`, http.StatusUnprocessableEntity},
		"existing site":        {`{"site":"taken"}`, http.StatusConflict},
		"malformed":            {`{`, http.StatusBadRequest},
	} {
		if got := postSiteCreate(t, f.app, tc.body, "admin").Code; got != tc.want {
			t.Errorf("%s: status %d, want %d", name, got, tc.want)
		}
	}
	if got := postSiteCreate(t, f.app, `{"site":"new-site"}`, "customer").Code; got != http.StatusForbidden {
		t.Errorf("non-administrator: status %d, want 403", got)
	}
	response := postSiteCreate(t, f.app, `{"site":"new-site"}`, "admin")
	if response.Code != http.StatusAccepted {
		t.Fatalf("create status %d: %s", response.Code, response.Body.String())
	}
	var queued map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	item, ok := f.app.Jobs.Get(queued["job_id"])
	if !ok || item.Kind != "site.create" || item.User != "new-site" {
		t.Fatalf("queued job = %#v, %v", item, ok)
	}
	var payload durableSiteCreationRequest
	if err := json.Unmarshal(item.Payload, &payload); err != nil || payload.Template != "php" || payload.Actor != "admin" {
		t.Fatalf("queued payload = %s, %v", item.Payload, err)
	}
}
