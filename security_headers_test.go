package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersPresent(t *testing.T) {
	cfg := Config{Production: false, TLSCertFile: ""}
	middleware := securityHeadersMiddleware(cfg)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	tests := []struct {
		name  string
		value string
	}{
		{"X-Content-Type-Options", "nosniff"},
		{"X-Frame-Options", "DENY"},
		{"X-XSS-Protection", "1; mode=block"},
		{"Referrer-Policy", "no-referrer"},
	}

	for _, test := range tests {
		if actual := w.Header().Get(test.name); actual != test.value {
			t.Errorf("header %s: expected %q, got %q", test.name, test.value, actual)
		}
	}

	// CSP header should be present and contain important directives
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Content-Security-Policy header missing")
	}
	if !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP missing strict default: %s", csp)
	}
	if !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP missing script restriction: %s", csp)
	}
}

func TestComposedMiddlewareKeepsAuthoritativeSecurityHeaders(t *testing.T) {
	cfg := Config{Production: false}
	inner := logging(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), nil, cfg.Production)
	handler := securityHeadersMiddleware(cfg)(inner)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := response.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("composed Referrer-Policy = %q, want no-referrer", got)
	}
	csp := response.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'none'", "style-src 'self'", "upgrade-insecure-requests"} {
		if !strings.Contains(csp, directive) {
			t.Fatalf("composed CSP missing %q: %s", directive, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("composed CSP permits unsafe inline content: %s", csp)
	}
}

func TestSecurityHeadersHSTSInProduction(t *testing.T) {
	cfgProd := Config{Production: true, TLSCertFile: "/test.crt", TLSKeyFile: "/test.key"}
	middleware := securityHeadersMiddleware(cfgProd)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	hsts := w.Header().Get("Strict-Transport-Security")
	if hsts == "" {
		t.Fatal("HSTS header missing in production with TLS")
	}
	if !strings.Contains(hsts, "max-age=31536000") {
		t.Errorf("HSTS max-age incorrect: %s", hsts)
	}
	if !strings.Contains(hsts, "includeSubDomains") {
		t.Errorf("HSTS missing includeSubDomains: %s", hsts)
	}
}

func TestSecurityHeadersNoHSTSWithoutTLS(t *testing.T) {
	cfgNonTLS := Config{Production: true, TLSCertFile: "", TLSKeyFile: ""}
	middleware := securityHeadersMiddleware(cfgNonTLS)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	hsts := w.Header().Get("Strict-Transport-Security")
	if hsts != "" {
		t.Fatalf("HSTS should not be set without TLS, got: %s", hsts)
	}
}

func TestSecurityHeadersPermissionsPolicy(t *testing.T) {
	cfg := Config{Production: false, TLSCertFile: ""}
	middleware := securityHeadersMiddleware(cfg)

	handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	pp := w.Header().Get("Permissions-Policy")
	if pp == "" {
		t.Fatal("Permissions-Policy header missing")
	}
	// Should disable sensitive features
	if !strings.Contains(pp, "camera=()") {
		t.Errorf("camera should be disabled: %s", pp)
	}
	if !strings.Contains(pp, "microphone=()") {
		t.Errorf("microphone should be disabled: %s", pp)
	}
	if !strings.Contains(pp, "geolocation=()") {
		t.Errorf("geolocation should be disabled: %s", pp)
	}
}

func TestSecurityHeadersHSTSBehindTLSTerminatingProxy(t *testing.T) {
	handler := securityHeadersMiddleware(Config{Production: true, TLSAlreadyTerminated: true})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if hsts := w.Header().Get("Strict-Transport-Security"); !strings.Contains(hsts, "max-age=31536000") {
		t.Fatalf("HSTS missing behind a TLS-terminating proxy, got %q", hsts)
	}
}
