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

func TestAccountMeIsTenantScopedAndDoesNotExposeCredentials(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "")
	store, err := OpenAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("alice", "a sufficiently long customer password", testTOTPSecret, "professional", []string{"alice-site"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("bob", "a sufficiently long customer password", testTOTPSecret, "starter", []string{"bob-site"}); err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	auth.Accounts = store
	a := &App{Auth: auth, Accounts: store, Config: Config{WebRoot: t.TempDir()}}

	secret, err := decodeTOTPSecret(testTOTPSecret)
	if err != nil {
		t.Fatal(err)
	}
	code := totpCode(secret, uint64(time.Now().Unix()/30))
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=alice&password=a+sufficiently+long+customer+password&totp="+code))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	auth.Login(loginResponse, login)
	if loginResponse.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d", loginResponse.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/account/me", nil)
	for _, cookie := range loginResponse.Result().Cookies() {
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	a.accountMe(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("account status = %d: %s", response.Code, response.Body.String())
	}
	var view CustomerAccountView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Username != "alice" || len(view.Sites) != 1 || view.Sites[0] != "alice-site" {
		t.Fatalf("unexpected customer view: %#v", view)
	}
	if view.Plan != "professional" || view.SiteLimit != 5 {
		t.Fatalf("unexpected plan data: %#v", view)
	}
	if strings.Contains(response.Body.String(), "password_hash") || strings.Contains(response.Body.String(), "totp_secret") {
		t.Fatalf("account response exposed credential fields: %s", response.Body.String())
	}

	apiRequest := httptest.NewRequest(http.MethodGet, "/api/account/me", nil).WithContext(withAPIUser("alice"))
	apiResponse := httptest.NewRecorder()
	a.accountMe(apiResponse, apiRequest)
	if apiResponse.Code != http.StatusForbidden {
		t.Fatalf("API token account view = %d, want 403", apiResponse.Code)
	}
}

func TestAdminAccountInventoryDoesNotExposeCredentialMaterial(t *testing.T) {
	store := &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", PasswordHash: "bcrypt-hash", TOTPSecret: testTOTPSecret, RecoveryCodeHashes: []string{"hashed-code"}, Plan: "starter"},
	}}
	a := &App{Auth: Auth{Username: "admin"}, Accounts: store}
	w := httptest.NewRecorder()
	a.accounts(w, httptest.NewRequest(http.MethodGet, "/api/accounts", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("account inventory status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{"password_hash", "totp_secret", "recovery_code_hashes", testTOTPSecret, "bcrypt-hash"} {
		if strings.Contains(body, secret) {
			t.Fatalf("account inventory exposed %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"mfa_enabled":true`) {
		t.Fatalf("account inventory omitted MFA status: %s", body)
	}
}

func withAPIUser(username string) context.Context {
	return context.WithValue(context.Background(), apiTokenUsernameKey{}, username)
}

func TestCustomerLogoutIsAuditedAsTheCustomer(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "")
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("l", 32))
	store, err := OpenAccountStore(filepath.Join(t.TempDir(), "accounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("alice", "a sufficiently long customer password", testTOTPSecret, "professional", []string{"alice-site"}); err != nil {
		t.Fatal(err)
	}
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	auth.Accounts = store
	auth.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	if err := auth.ConfigureSessionStore(filepath.Join(t.TempDir(), "sessions.json")); err != nil {
		t.Fatal(err)
	}
	secret, err := decodeTOTPSecret(testTOTPSecret)
	if err != nil {
		t.Fatal(err)
	}
	code := totpCode(secret, uint64(time.Now().Unix()/30))
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=alice&password=a+sufficiently+long+customer+password&totp="+code))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	auth.Login(loginResponse, login)
	if loginResponse.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d", loginResponse.Code)
	}
	cookies := loginResponse.Result().Cookies()
	logout := sessionRequest(http.MethodPost, cookies)
	logout.Header.Set("X-CSRF-Token", csrfCookie(cookies))
	if got := serveLogout(t, auth, logout).Code; got != http.StatusSeeOther {
		t.Fatalf("logout status = %d", got)
	}
	events, err := os.ReadFile(auth.AuditLog)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, line := range strings.Split(strings.TrimSpace(string(events)), "\n") {
		var event AuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Action == "auth.logout" {
			found = true
			if event.Actor != "alice" {
				t.Fatalf("logout audited as %q, want alice", event.Actor)
			}
		}
	}
	if !found {
		t.Fatal("logout was not audited")
	}
}

func TestAccountSuspendRejectsMalformedJSON(t *testing.T) {
	auth, cookies := newCSRFTestAuth(t)
	a := &App{Auth: auth, Config: Config{AuditLog: auth.AuditLog}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/accounts/suspend", strings.NewReader("{not json"))
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	request.Header.Set("X-CSRF-Token", csrfCookie(cookies))
	response := httptest.NewRecorder()
	a.accountSuspend(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed suspension status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
