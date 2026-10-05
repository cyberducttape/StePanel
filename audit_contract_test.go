package stepanel

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// unavailableAuditLog returns an audit path that can never be written: its
// parent is a regular file.
func unavailableAuditLog(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(blocker, "audit.jsonl")
}

func auditActions(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var actions []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode audit event: %v", err)
		}
		actions = append(actions, event.Action)
	}
	return actions
}

func TestSecurityAuditRecordsIntentThenOutcome(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")

	intent, err := BeginSecurityAudit(path, "admin", "database.deleted", "shop", "safety_backup=x")
	if err != nil {
		t.Fatalf("BeginSecurityAudit: %v", err)
	}
	intent.Completed("dropped")
	failing, err := BeginSecurityAudit(path, "admin", "database.deleted", "blog", "safety_backup=y")
	if err != nil {
		t.Fatal(err)
	}
	failing.Failed("helper refused")

	want := []string{"database.deleted.initiated", "database.deleted", "database.deleted.initiated", "database.deleted.failed"}
	if got := auditActions(t, path); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ledger = %v, want %v", got, want)
	}
	if err := VerifyAuditLog(path); err != nil {
		t.Fatalf("ledger does not verify: %v", err)
	}
}

func TestSecurityAuditFinishRecordsOutcomeOnEveryPath(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	run := func(succeed bool) {
		intent, err := BeginSecurityAudit(path, "admin", "site.deployed", "shop", "example.com")
		if err != nil {
			t.Fatal(err)
		}
		var outcome string
		defer intent.Finish(&outcome, "route was not published")
		if !succeed {
			return
		}
		outcome = "example.com"
	}
	run(false)
	run(true)
	want := "site.deployed.initiated,site.deployed.failed,site.deployed.initiated,site.deployed"
	if got := strings.Join(auditActions(t, path), ","); got != want {
		t.Fatalf("ledger = %s, want %s", got, want)
	}
}

func TestBeginSecurityAuditFailsWhenIntentCannotBeRecorded(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	// No outbox is configured and the signed log cannot be written.
	if _, err := BeginSecurityAudit(unavailableAuditLog(t), "admin", "site.deleted", "shop", ""); err == nil {
		t.Fatal("BeginSecurityAudit succeeded without a durable intent")
	}
	if _, err := BeginSecurityAudit(filepath.Join(t.TempDir(), "audit.jsonl"), "", "site.deleted", "shop", ""); err == nil {
		t.Fatal("BeginSecurityAudit accepted an empty actor")
	}
}

func TestTelemetryAndRevocationAuditNeverFailTheCaller(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	unavailable := unavailableAuditLog(t)
	// Neither returns an error to act on; both must simply return.
	TelemetryAudit(unavailable, "admin", "task.updated", "shop", "detail")
	RevocationAudit(unavailable, "admin", "auth.api_token.revoked", "token-1", "revoked")
}

// An event persisted in the outbox is durably recorded even when publishing
// it to the signed log fails; the caller must not treat it as missing.
// Previously the publication error was returned, so a caller refused the
// operation while the outbox later published an intent for something that
// never ran.
func TestOutboxEventIsDurableWhenPublicationIsDeferred(t *testing.T) {
	db, err := sql.Open("sqlite", "file:audit-contract-outbox?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	outbox, err := newAuditOutboxStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.recordDurably(context.Background(), unavailableAuditLog(t), "admin", "site.deleted.initiated", "shop", ""); err != nil {
		t.Fatalf("durable outbox event reported as unrecorded: %v", err)
	}
	if pending, err := outbox.pendingCount(context.Background()); err != nil || pending != 1 {
		t.Fatalf("pending = %d, %v; want the event retained for retry", pending, err)
	}
}

// A Class A grant must change nothing when its intent cannot be recorded.
func TestCustomerTokenCreationIsRefusedWithoutAuditLedger(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "")
	accounts, err := OpenAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.Create("customer", "a sufficiently long customer password", testTOTPSecret, "starter", []string{"site-one"}); err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	auth.Accounts = accounts
	secret, err := decodeTOTPSecret(testTOTPSecret)
	if err != nil {
		t.Fatal(err)
	}
	code := totpCode(secret, uint64(time.Now().Unix()/30))
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=customer&password=a+sufficiently+long+customer+password&totp="+code))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	auth.Login(loginResponse, login)
	if loginResponse.Code != http.StatusSeeOther {
		t.Fatalf("customer login status = %d", loginResponse.Code)
	}
	var csrf string
	for _, cookie := range loginResponse.Result().Cookies() {
		if cookie.Name == "stepanel_csrf" {
			csrf = cookie.Value
		}
	}
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tokens := &apiTokenStore{db: db}
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	app := &App{Auth: auth, APITokens: tokens, Config: Config{AuditLog: unavailableAuditLog(t)}}

	request := httptest.NewRequest(http.MethodPost, "/api/account/tokens", strings.NewReader(`{"name":"automation","scopes":["backup:create"]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	for _, cookie := range loginResponse.Result().Cookies() {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	app.apiTokens(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("token creation without audit ledger = %d %s, want 503", response.Code, response.Body.String())
	}
	if items, err := tokens.list("customer"); err != nil || len(items) != 0 {
		t.Fatalf("a token was created without a recorded intent: %v %v", items, err)
	}
}

// The panel administrator name is not the requester. Audit events must name
// the authenticated actor of the request.
func TestAuditCallsDoNotUseTheConfiguredAdminAsActor(t *testing.T) {
	call := regexp.MustCompile(`(TelemetryAudit|SecurityAuditRequired|BeginSecurityAudit|RevocationAudit)\([^)]*a\.Auth\.Username,`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if match := call.Find(source); match != nil {
			t.Errorf("%s: audit actor is the configured administrator, not the requester: %s", name, match)
		}
	}
}
