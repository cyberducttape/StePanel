package stepanel

import (
	"context"
	"database/sql"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	authpolicy "github.com/cyberducttape/StePanel/internal/auth"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

func TestLoginPageUsesCSPCompatibleAssets(t *testing.T) {
	page := loginPage("", false)
	if strings.Contains(page, "<style") || strings.Contains(page, "onclick=") {
		t.Fatal("login page contains inline style or script")
	}
	for _, asset := range []string{"web/static/login.css", "web/static/login.js"} {
		if _, err := webAssets.ReadFile(asset); err != nil {
			t.Fatalf("login asset %s is not embedded: %v", asset, err)
		}
	}
}

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

func TestRequireReportsSessionStoreFailureAsUnavailable(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions (id TEXT PRIMARY KEY, username TEXT NOT NULL, expiry INTEGER NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := auth.ConfigureSessionStoreDB(db, ""); err != nil {
		t.Fatal(err)
	}
	const sessionID = "durable-session"
	expiry := time.Now().Add(time.Hour).Unix()
	if err := auth.sessions.add(sessionID, "admin", expiry); err != nil {
		t.Fatal(err)
	}
	unsigned := strings.Join([]string{"admin", strconv.FormatInt(expiry, 10), sessionID, auth.credentialFingerprintFor("admin", auth.PasswordHash)}, "|")
	value := base64.RawURLEncoding.EncodeToString([]byte(unsigned + "|" + auth.sign(unsigned)))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	request.AddCookie(&http.Cookie{Name: "stepanel_session", Value: value})
	called := false
	response := httptest.NewRecorder()
	auth.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || called || !strings.Contains(response.Body.String(), "session validation is unavailable") {
		t.Fatalf("unavailable session store response = %d, called=%t, body=%q; want 503 without entering the handler", response.Code, called, response.Body.String())
	}
}

func TestAuthClientIPUsesTrustedProxyPolicyForLoginThrottling(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	auth.TrustedProxies, err = authpolicy.ParseTrustedProxyCIDRs("127.0.0.1/32, ::1/128")
	if err != nil {
		t.Fatal(err)
	}
	login := func(peer, xff string) int {
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=wrong"))
		req.RemoteAddr = peer
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Forwarded-For", xff)
		response := httptest.NewRecorder()
		auth.Login(response, req)
		return response.Code
	}
	for attempt := 0; attempt < 5; attempt++ {
		if got := login("127.0.0.1:8080", "198.51.100.10"); got != http.StatusUnauthorized {
			t.Fatalf("trusted-proxy attempt %d status = %d, want %d", attempt+1, got, http.StatusUnauthorized)
		}
	}
	if got := login("127.0.0.1:8080", "198.51.100.10"); got != http.StatusTooManyRequests {
		t.Fatalf("sixth trusted-proxy attempt status = %d, want %d", got, http.StatusTooManyRequests)
	}
	if got := login("127.0.0.1:8080", "198.51.100.11"); got != http.StatusUnauthorized {
		t.Fatalf("different forwarded client status = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := login("203.0.113.9:8080", "198.51.100.10"); got != http.StatusUnauthorized {
		t.Fatalf("untrusted forwarded client status = %d, want %d", got, http.StatusUnauthorized)
	}
	ipv6 := httptest.NewRequest(http.MethodGet, "/", nil)
	ipv6.RemoteAddr = "[::1]:8080"
	ipv6.Header.Set("X-Forwarded-For", "2001:db8::10, ::1")
	if got := auth.ClientIP(ipv6); got != "2001:db8::10" {
		t.Fatalf("IPv6 trusted proxy client IP = %q, want 2001:db8::10", got)
	}
}

// csrfTestSession logs in and returns the issued cookies and CSRF token.
func csrfTestSession(t *testing.T) (Auth, []*http.Cookie, string) {
	t.Helper()
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
	response := httptest.NewRecorder()
	auth.Login(response, login)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d", response.Code)
	}
	cookies := response.Result().Cookies()
	token := ""
	for _, cookie := range cookies {
		if cookie.Name == "stepanel_csrf" {
			token = cookie.Value
		}
	}
	if token == "" {
		t.Fatal("login did not issue a CSRF token")
	}
	return auth, cookies, token
}

func TestLoginCookieSecureAttributesMatchConfiguredTransport(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(strconv.FormatBool(secure), func(t *testing.T) {
			t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
			t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
			t.Setenv("STEPANEL_ADMIN_TOTP_SECRET", "")
			t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
			auth, err := NewAuth(secure)
			if err != nil {
				t.Fatal(err)
			}
			if err := auth.ConfigureSessionStore(filepath.Join(t.TempDir(), "sessions.json")); err != nil {
				t.Fatal(err)
			}
			login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=correct+horse+battery+staple"))
			login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			auth.Login(response, login)
			if response.Code != http.StatusSeeOther {
				t.Fatalf("login status = %d", response.Code)
			}
			seen := map[string]bool{}
			for _, cookie := range response.Result().Cookies() {
				if cookie.Name == "stepanel_session" || cookie.Name == "stepanel_csrf" {
					seen[cookie.Name] = true
					if cookie.Secure != secure {
						t.Errorf("%s Secure = %t, want %t", cookie.Name, cookie.Secure, secure)
					}
				}
			}
			if !seen["stepanel_session"] || !seen["stepanel_csrf"] {
				t.Fatalf("login omitted session or CSRF cookie: %#v", seen)
			}
		})
	}
}

func TestCSRFMultipartRequiresHeaderWithoutParsingBody(t *testing.T) {
	auth, cookies, token := csrfTestSession(t)
	req := httptest.NewRequest(http.MethodPost, "/api/upload", strings.NewReader("csrf="+token))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	if auth.CSRF(req) {
		t.Fatal("multipart body token should not be parsed for CSRF validation")
	}
	req.Header.Set("X-CSRF-Token", token)
	if !auth.CSRF(req) {
		t.Fatal("multipart header token should validate")
	}
}

func TestCSRFRejectsTokenNotBoundToSession(t *testing.T) {
	auth, cookies, token := csrfTestSession(t)
	session := func(csrfCookie *http.Cookie, value string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/mutate", strings.NewReader("csrf="+value))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, cookie := range cookies {
			if cookie.Name != "stepanel_csrf" {
				req.AddCookie(cookie)
			}
		}
		if csrfCookie != nil {
			req.AddCookie(csrfCookie)
		}
		return req
	}
	// A sibling subdomain can plant a cookie for the parent domain and submit
	// a matching form field; that pair must not satisfy CSRF.
	if auth.CSRF(session(&http.Cookie{Name: "stepanel_csrf", Value: "planted"}, "planted")) {
		t.Fatal("a planted double-submit cookie satisfied CSRF")
	}
	if auth.CSRF(session(&http.Cookie{Name: "stepanel_csrf", Value: ""}, "")) {
		t.Fatal("an empty CSRF cookie and field satisfied CSRF")
	}
	if !auth.CSRF(session(nil, token)) {
		t.Fatal("the session-bound token was rejected")
	}
	noSession := httptest.NewRequest(http.MethodPost, "/api/mutate", nil)
	noSession.AddCookie(&http.Cookie{Name: "stepanel_csrf", Value: token})
	noSession.Header.Set("X-CSRF-Token", token)
	if auth.CSRF(noSession) {
		t.Fatal("a CSRF token was accepted without its session")
	}
}

func TestRequireReissuesUnboundCSRFCookie(t *testing.T) {
	auth, cookies, token := csrfTestSession(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, cookie := range cookies {
		if cookie.Name != "stepanel_csrf" {
			req.AddCookie(cookie)
		}
	}
	req.AddCookie(&http.Cookie{Name: "stepanel_csrf", Value: "random-pre-upgrade-token"})
	response := httptest.NewRecorder()
	auth.Require(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(response, req)
	reissued := ""
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "stepanel_csrf" {
			reissued = cookie.Value
		}
	}
	if reissued != token {
		t.Fatalf("Require reissued CSRF cookie %q, want the session-bound token", reissued)
	}
}

func TestCSRFUsesAuthenticatedAPITokenContext(t *testing.T) {
	auth := Auth{Enabled: true, apiTokens: &apiTokenStore{}, apiTokenLimiter: newAPITokenRateLimiter()}
	request := httptest.NewRequest(http.MethodPost, "/api/mutate", nil)
	request = request.WithContext(context.WithValue(request.Context(), apiTokenUsernameKey{}, "admin"))
	if !auth.CSRF(request) {
		t.Fatal("CSRF rejected an API request already authenticated by Require")
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
	resetAuditHealthAfter(t)
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

// TestLoginSuccessDoesNotLaunderFailuresAgainstOtherAccounts is the
// regression test for the shared-IP reset bug: authenticating to one's own
// account must not clear failed guesses made against a different account,
// nor the client-wide abuse history.
func TestLoginSuccessDoesNotLaunderFailuresAgainstOtherAccounts(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	login := func(username, password string) int {
		form := "username=" + username + "&password=" + password
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
		req.RemoteAddr = "192.0.2.50:4444"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		auth.Login(response, req)
		return response.Code
	}
	for attempt := 0; attempt < loginAccountAttemptLimit; attempt++ {
		if got := login("victim", "guess"); got != http.StatusUnauthorized {
			t.Fatalf("victim guess %d status = %d, want %d", attempt+1, got, http.StatusUnauthorized)
		}
		if got := login("admin", "correct+horse+battery+staple"); got != http.StatusSeeOther {
			t.Fatalf("own-account login %d status = %d, want %d", attempt+1, got, http.StatusSeeOther)
		}
	}
	if got := login("victim", "guess"); got != http.StatusTooManyRequests {
		t.Fatalf("victim guess after interleaved successes status = %d, want %d", got, http.StatusTooManyRequests)
	}
	if got := login("VICTIM", "guess"); got != http.StatusTooManyRequests {
		t.Fatalf("case-varied victim guess status = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestLoginClientWideBucketSurvivesSuccessfulLogin(t *testing.T) {
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	login := func(username, password string) int {
		form := "username=" + username + "&password=" + password
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form))
		req.RemoteAddr = "192.0.2.51:4444"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response := httptest.NewRecorder()
		auth.Login(response, req)
		return response.Code
	}
	// Spread guesses across usernames so no account bucket fills, while
	// interleaving successful logins that previously reset the IP bucket.
	for attempt := 0; attempt < loginClientAttemptLimit; attempt++ {
		if got := login("user"+strconv.Itoa(attempt), "guess"); got != http.StatusUnauthorized {
			t.Fatalf("spray guess %d status = %d, want %d", attempt+1, got, http.StatusUnauthorized)
		}
		if attempt < loginClientAttemptLimit-1 {
			if got := login("admin", "correct+horse+battery+staple"); got != http.StatusSeeOther {
				t.Fatalf("own-account login %d status = %d, want %d", attempt+1, got, http.StatusSeeOther)
			}
		}
	}
	if got := login("another", "guess"); got != http.StatusTooManyRequests {
		t.Fatalf("spray guess past client-wide limit status = %d, want %d", got, http.StatusTooManyRequests)
	}
}

func TestLoginPageEscapesMessage(t *testing.T) {
	page := loginPage(`<script>alert(1)</script>`, false)
	if strings.Contains(page, "<script>alert(1)") {
		t.Fatal("login page rendered message without HTML escaping")
	}
}
