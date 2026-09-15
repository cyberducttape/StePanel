package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestRecoveryActionAllowedThrottlesPerActorAfterFiveAttempts(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if !auth.RecoveryActionAllowed("admin") {
			t.Fatalf("attempt %d unexpectedly throttled", i+1)
		}
	}
	if auth.RecoveryActionAllowed("admin") {
		t.Fatal("sixth account-recovery action in the window should be throttled")
	}
	if !auth.RecoveryActionAllowed("other-admin") {
		t.Fatal("a distinct actor should not share the throttled actor's window")
	}
}

func TestAuthRequiresSessionSecret(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "")
	if _, err := NewAuth(true); err == nil {
		t.Fatal("expected missing session secret to fail")
	}
}

func TestHashPassword(t *testing.T) {
	hash, err := hashPassword("hosting-grade-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("hosting-grade-password")); err != nil {
		t.Fatalf("generated hash did not verify: %v", err)
	}
}

func TestAuthRejectsInvalidPasswordHash(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "not-a-bcrypt-hash")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	if _, err := NewAuth(true); err == nil {
		t.Fatal("invalid bcrypt hash was accepted")
	}
}

func TestAuthRejectsOversizedLogin(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	body := "username=admin&password=" + strings.Repeat("x", 65<<10)
	request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	auth.Login(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("login status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestAuthSessionAndCSRF(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/login", strings.NewReader("username=admin&password=correct+horse+battery+staple"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	auth.Login(response, request)
	if response.Code != 303 {
		t.Fatalf("login status = %d", response.Code)
	}
	cookies := response.Result().Cookies()
	if len(cookies) < 2 {
		t.Fatal("expected session and csrf cookies")
	}
	sessionRequest := httptest.NewRequest("GET", "/", nil)
	for _, cookie := range cookies {
		sessionRequest.AddCookie(cookie)
	}
	if !auth.validSession(sessionRequest) {
		t.Fatal("expected valid session")
	}
	csrfRequest := httptest.NewRequest("POST", "/api/change", strings.NewReader("csrf="+cookies[1].Value))
	csrfRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		csrfRequest.AddCookie(cookie)
	}
	if !auth.CSRF(csrfRequest) {
		t.Fatal("expected valid csrf token")
	}
}

func TestCSRFMultipartRequiresHeaderWithoutParsingBody(t *testing.T) {
	auth := Auth{Enabled: true}
	req := httptest.NewRequest(http.MethodPost, "/api/upload", strings.NewReader("csrf=body-token"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	cookie := &http.Cookie{Name: "stepanel_csrf", Value: "body-token"}
	req.AddCookie(cookie)
	if auth.CSRF(req) {
		t.Fatal("multipart body token should not be parsed for CSRF validation")
	}
	req.Header.Set("X-CSRF-Token", "body-token")
	if !auth.CSRF(req) {
		t.Fatal("multipart header token should validate")
	}
}

func TestLogoutRevokesPersistedSession(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.ConfigureSessionStore(filepath.Join(t.TempDir(), "sessions.json")); err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=correct+horse+battery+staple"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	auth.Login(loginResponse, login)
	cookies := loginResponse.Result().Cookies()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	if !auth.validSession(request) {
		t.Fatal("new session is invalid")
	}
	logout := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader("csrf="+cookies[1].Value))
	logout.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		logout.AddCookie(cookie)
	}
	auth.Logout(httptest.NewRecorder(), logout)
	if auth.validSession(request) {
		t.Fatal("logged-out session remained valid")
	}
}

func TestPasswordRotationInvalidatesSession(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=correct+horse+battery+staple"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	auth.Login(response, login)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range response.Result().Cookies() {
		request.AddCookie(cookie)
	}
	if !auth.validSession(request) {
		t.Fatal("new session is invalid")
	}
	auth.PasswordHash = "$2a$10$rotated-password-hash-invalidates-existing-session"
	if auth.validSession(request) {
		t.Fatal("session survived password rotation")
	}
}

func TestTOTPValidationRejectsReplay(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1234567890, 0)
	code := totpCode([]byte("12345678901234567890"), uint64(now.Unix()/30))
	if !auth.consumeTOTP(code, now) {
		t.Fatal("valid TOTP was rejected")
	}
	if auth.consumeTOTP(code, now) {
		t.Fatal("replayed TOTP was accepted")
	}
}

func TestTOTPReplayPersistsAcrossAuthInstances(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ConfigureTOTPReplayDB(db); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1234567890, 0)
	code := totpCode([]byte("12345678901234567890"), uint64(now.Unix()/30))
	if !first.consumeTOTP(code, now) {
		t.Fatal("valid TOTP was rejected")
	}
	second, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.ConfigureTOTPReplayDB(db); err != nil {
		t.Fatal(err)
	}
	if second.consumeTOTP(code, now) {
		t.Fatal("TOTP replay survived a new Auth instance")
	}
}

func TestAuthRejectsInvalidTOTPSecret(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "not-base32")
	if _, err := NewAuth(true); err == nil {
		t.Fatal("invalid TOTP secret was accepted")
	}
}

func TestAuthenticatedMutationFailsClosedWhenAuditIsUnavailable(t *testing.T) {
	t.Cleanup(func() {
		auditMu.Lock()
		auditPersistenceErr = nil
		auditMu.Unlock()
	})
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=correct+horse+battery+staple"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	auth.Login(loginResponse, login)
	var session *http.Cookie
	for _, cookie := range loginResponse.Result().Cookies() {
		if cookie.Name == "stepanel_session" {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("login did not issue a session")
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	auth.AuditLog = filepath.Join(blocked, "audit.jsonl")
	request := httptest.NewRequest(http.MethodPost, "/api/change", nil)
	request.AddCookie(session)
	called := false
	handler := auth.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("status = %d, handler called = %v", response.Code, called)
	}
}
