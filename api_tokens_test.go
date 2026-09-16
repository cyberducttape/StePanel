package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCustomerAPITokenCreationRequiresCSRF(t *testing.T) {
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
		t.Fatalf("customer login status = %d, want %d", loginResponse.Code, http.StatusSeeOther)
	}
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{Auth: auth, APITokens: &apiTokenStore{db: db}}

	request := httptest.NewRequest(http.MethodPost, "/api/account/tokens", strings.NewReader(`{"name":"automation"}`))
	request.Header.Set("Content-Type", "application/json")
	for _, cookie := range loginResponse.Result().Cookies() {
		// Deliberately drop the CSRF cookie the login response issued: a
		// cross-site POST carries the session cookie automatically but
		// never the CSRF cookie/header pair, which is exactly the
		// scenario this check exists to reject.
		if cookie.Name == "stepanel_csrf" {
			continue
		}
		request.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	app.apiTokens(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("customer token creation without CSRF = %d, want %d", response.Code, http.StatusForbidden)
	}
	items, err := app.APITokens.list("customer")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("token was created despite missing CSRF proof: %#v", items)
	}
}

func TestAuthRejectsInvalidBearerInsteadOfFallingBackToCookie(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	auth.apiTokens = &apiTokenStore{}
	request := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	request.Header.Set("Authorization", "Bearer invalid")
	response := httptest.NewRecorder()
	auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("handler accepted invalid bearer") })).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestAPITokenLifecycleStoresOnlyHashAndRevokes(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &apiTokenStore{db: db}
	expires := time.Now().Add(time.Hour).Unix()
	item, secret, err := store.create("customer", "automation", &expires)
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || item.Prefix == "" {
		t.Fatal("token was not issued")
	}
	var stored string
	if err := db.QueryRow(`SELECT token_hash FROM api_tokens WHERE id = ?`, item.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == secret {
		t.Fatal("plaintext token was persisted")
	}
	if username, ok := store.authenticate(secret); !ok || username != "customer" {
		t.Fatalf("authenticate = %q, %v", username, ok)
	}
	if err := store.revoke("customer", item.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.authenticate(secret); ok {
		t.Fatal("revoked token remained valid")
	}
}

func TestAdministratorAPITokenScopes(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &apiTokenStore{db: db}
	readItem, readSecret, err := store.createScoped("admin", "read-only", nil, []string{"admin:read"}, adminAPIScopes)
	if err != nil {
		t.Fatal(err)
	}
	if len(readItem.Scopes) != 1 || readItem.Scopes[0] != "admin:read" {
		t.Fatalf("read token scopes = %#v", readItem.Scopes)
	}
	username, scopes, ok := store.authenticateWithScopes(readSecret)
	if !ok || username != "admin" || len(scopes) != 1 || scopes[0] != "admin:read" {
		t.Fatalf("authenticated scopes = %q %#v %v", username, scopes, ok)
	}

	t.Setenv("STEPANEL_ADMIN_USERNAME", "admin")
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	auth.apiTokens = store
	readRequest := httptest.NewRequest(http.MethodGet, "/api/services", nil)
	readRequest.Header.Set("Authorization", "Bearer "+readSecret)
	readResponse := httptest.NewRecorder()
	auth.RequireAdministrator(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(readResponse, readRequest)
	if readResponse.Code != http.StatusNoContent {
		t.Fatalf("read token GET status = %d", readResponse.Code)
	}
	mutateRequest := httptest.NewRequest(http.MethodPost, "/api/cloud/action", nil)
	mutateRequest.Header.Set("Authorization", "Bearer "+readSecret)
	mutateResponse := httptest.NewRecorder()
	auth.RequireAdministrator(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(mutateResponse, mutateRequest)
	if mutateResponse.Code != http.StatusForbidden {
		t.Fatalf("read token POST status = %d", mutateResponse.Code)
	}

	_, operateSecret, err := store.createScoped("admin", "operator", nil, []string{"admin:operate"}, adminAPIScopes)
	if err != nil {
		t.Fatal(err)
	}
	operateRequest := httptest.NewRequest(http.MethodPost, "/api/cloud/action", nil)
	operateRequest.Header.Set("Authorization", "Bearer "+operateSecret)
	operateResponse := httptest.NewRecorder()
	auth.RequireAdministrator(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(operateResponse, operateRequest)
	if operateResponse.Code != http.StatusNoContent {
		t.Fatalf("operate token POST status = %d", operateResponse.Code)
	}
}

// TestCustomerAPITokenCreationRequiresExplicitScopes ensures every newly
// created customer token names its own capabilities up front, so a leaked
// deploy token cannot silently carry full account authority the way an
// unscoped token would. Token management itself requires the customer's
// browser session (not another API token), so this drives the handler
// through a real login the way TestCustomerAPITokenCreationRequiresCSRF
// does.
func TestCustomerAPITokenCreationRequiresExplicitScopes(t *testing.T) {
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
		t.Fatalf("customer login status = %d, want %d", loginResponse.Code, http.StatusSeeOther)
	}
	var csrf string
	for _, cookie := range loginResponse.Result().Cookies() {
		if cookie.Name == "stepanel_csrf" {
			csrf = cookie.Value
		}
	}
	if csrf == "" {
		t.Fatal("login did not issue a CSRF cookie")
	}
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{Auth: auth, APITokens: &apiTokenStore{db: db}}

	authenticatedPost := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/api/account/tokens", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-CSRF-Token", csrf)
		for _, cookie := range loginResponse.Result().Cookies() {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		app.apiTokens(response, request)
		return response
	}

	if response := authenticatedPost(`{"name":"automation"}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("token creation without scopes = %d %s, want %d", response.Code, response.Body.String(), http.StatusUnprocessableEntity)
	}
	if response := authenticatedPost(`{"name":"automation","scopes":["admin:operate"]}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("token creation with an admin-only scope = %d %s, want %d", response.Code, response.Body.String(), http.StatusUnprocessableEntity)
	}
	response := authenticatedPost(`{"name":"automation","scopes":["backup:create"]}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("scoped token creation = %d %s, want %d", response.Code, response.Body.String(), http.StatusCreated)
	}
	if !strings.Contains(response.Body.String(), `"backup:create"`) {
		t.Fatalf("created token metadata missing its scope: %s", response.Body.String())
	}
}

// TestCustomerAPIScopeGatesDeployAction is the enforcement-side counterpart:
// a token missing deploy:write cannot deploy, one that has it can proceed
// past the scope gate, and a pre-scoping (legacy, empty-scope) token keeps
// the full access it had when issued rather than being locked out.
func TestCustomerAPIScopeGatesDeployAction(t *testing.T) {
	dir := t.TempDir()
	webRoot := filepath.Join(dir, "www")
	appRoot := filepath.Join(dir, "apps")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "owned", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	a := &App{Config: Config{WebRoot: webRoot, AppRoot: appRoot}}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"customer": {Username: "customer", Plan: "starter", Sites: []string{"owned"}},
	}}
	a.Auth = Auth{Username: "admin"}

	requestWithScopes := func(scopes []string) *http.Request {
		body := `{"site":"owned","version":"3.13","entrypoint":"app:app","port":8000,"workers":1}`
		r := httptest.NewRequest(http.MethodPost, "/api/python/deploy", strings.NewReader(body))
		ctx := context.WithValue(r.Context(), apiTokenUsernameKey{}, "customer")
		if scopes != nil {
			ctx = context.WithValue(ctx, apiTokenScopesKey{}, scopes)
		}
		return r.WithContext(ctx)
	}

	wrongScope := httptest.NewRecorder()
	a.pythonDeploy(wrongScope, requestWithScopes([]string{"backup:create"}))
	if wrongScope.Code != http.StatusForbidden || !strings.Contains(wrongScope.Body.String(), "deploy:write") {
		t.Fatalf("deploy with unrelated scope = %d %s, want a deploy:write 403", wrongScope.Code, wrongScope.Body.String())
	}

	rightScope := httptest.NewRecorder()
	a.pythonDeploy(rightScope, requestWithScopes([]string{"deploy:write"}))
	if strings.Contains(rightScope.Body.String(), "deploy:write scope") {
		t.Fatalf("deploy with deploy:write scope was still rejected for scope: %d %s", rightScope.Code, rightScope.Body.String())
	}

	legacyUnscoped := httptest.NewRecorder()
	a.pythonDeploy(legacyUnscoped, requestWithScopes(nil))
	if strings.Contains(legacyUnscoped.Body.String(), "deploy:write scope") {
		t.Fatalf("pre-scoping legacy token was rejected for scope: %d %s", legacyUnscoped.Code, legacyUnscoped.Body.String())
	}
}
