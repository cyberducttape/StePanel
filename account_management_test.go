package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func withAPIUser(username string) context.Context {
	return context.WithValue(context.Background(), apiTokenUsernameKey{}, username)
}
