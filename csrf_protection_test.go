package stepanel

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newCSRFTestAuth(t *testing.T) (Auth, []*http.Cookie) {
	t.Helper()
	t.Setenv("STEPANEL_ADMIN_PASSWORD", "correct horse battery staple")
	t.Setenv("STEPANEL_ADMIN_PASSWORD_HASH", "")
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auth, err := NewAuth(true)
	if err != nil {
		t.Fatal(err)
	}
	auth.AuditLog = t.TempDir() + "/audit.jsonl"
	login := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("username=admin&password=correct+horse+battery+staple"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	auth.Login(response, login)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want %d", response.Code, http.StatusSeeOther)
	}
	return auth, response.Result().Cookies()
}

func serveLogout(t *testing.T, auth Auth, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	auth.Require(http.HandlerFunc(auth.Logout)).ServeHTTP(response, request)
	return response
}

func sessionRequest(method string, cookies []*http.Cookie) *http.Request {
	request := httptest.NewRequest(method, "/logout", nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	return request
}

func csrfCookie(cookies []*http.Cookie) string {
	for _, cookie := range cookies {
		if cookie.Name == "stepanel_csrf" {
			return cookie.Value
		}
	}
	return ""
}

func TestCSRFProtectionUsesRealAuthenticatedHandler(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*http.Request, string)
		wantStatus int
	}{
		{name: "session without token", mutate: func(*http.Request, string) {}, wantStatus: http.StatusForbidden},
		{name: "session with bad token", mutate: func(r *http.Request, _ string) { r.Header.Set("X-CSRF-Token", "wrong") }, wantStatus: http.StatusForbidden},
		{name: "session with matching header", mutate: func(r *http.Request, token string) { r.Header.Set("X-CSRF-Token", token) }, wantStatus: http.StatusSeeOther},
		{name: "multipart body token is rejected", mutate: func(r *http.Request, token string) {
			r.Header.Set("Content-Type", "multipart/form-data; boundary=test")
			r.Body = io.NopCloser(strings.NewReader("csrf=" + token))
		}, wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, cookies := newCSRFTestAuth(t)
			request := sessionRequest(http.MethodPost, cookies)
			tt.mutate(request, csrfCookie(cookies))
			if got := serveLogout(t, auth, request).Code; got != tt.wantStatus {
				t.Fatalf("logout status = %d, want %d", got, tt.wantStatus)
			}
		})
	}
}

func TestCSRFProtectionUsesRealBearerAuthentication(t *testing.T) {
	auth, _ := newCSRFTestAuth(t)
	db, err := openControlPlaneDB(t.TempDir() + "/control-plane.db")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store := &apiTokenStore{db: db}
	_, secret, err := store.createScoped("admin", "test", nil, []string{"admin:read"}, adminAPIScopes)
	if err != nil {
		t.Fatal(err)
	}
	auth.apiTokens = store

	request := httptest.NewRequest(http.MethodPost, "/logout", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	if got := serveLogout(t, auth, request).Code; got != http.StatusSeeOther {
		t.Fatalf("valid bearer logout status = %d, want %d", got, http.StatusSeeOther)
	}

	invalid := httptest.NewRequest(http.MethodPost, "/logout", nil)
	invalid.Header.Set("Authorization", "Bearer invalid-token")
	if got := serveLogout(t, auth, invalid).Code; got != http.StatusUnauthorized {
		t.Fatalf("invalid bearer status = %d, want %d", got, http.StatusUnauthorized)
	}
}
