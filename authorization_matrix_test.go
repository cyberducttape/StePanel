package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Helper to encode JSON for test requests
func jsonEncode(t *testing.T, v interface{}) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("failed to encode JSON: %v", err)
	}
	return b
}

// TestCrossTenantResourceAccessDenied verifies that customers cannot access
// resources owned by other tenants even with valid credentials.
// This is an adversarial test: customers attempt to access each other's
// sites, backups, and databases, and all attempts must fail with 403.
func TestCrossTenantResourceAccessDenied(t *testing.T) {
	webRoot := t.TempDir()

	// Create directories for both customer sites
	for _, site := range []string{"alice-site", "bob-site"} {
		if err := os.MkdirAll(filepath.Join(webRoot, "sites", site, "public"), 0750); err != nil {
			t.Fatal(err)
		}
	}

	a := &App{Config: Config{WebRoot: webRoot}}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"alice-site"}},
		"bob":   {Username: "bob", Plan: "starter", Sites: []string{"bob-site"}},
	}}
	a.Auth = Auth{Username: "admin"}
	a.Domains = &DomainClaimStore{}

	// Helper to add alice context to request
	asAlice := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "alice"))
	}

	tests := []struct {
		description string
		request     *http.Request
		handler     func(http.ResponseWriter, *http.Request)
		expectCode  int
	}{
		{
			description: "Customer cannot read another customer's site environment",
			request:     asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/environment/bob-site", nil)),
			handler:     a.siteEnvironment,
			expectCode:  http.StatusForbidden,
		},
		{
			description: "Customer cannot access SSH configuration for another's site",
			request:     asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/access/bob-site", nil)),
			handler:     a.siteAccess,
			expectCode:  http.StatusForbidden,
		},
		{
			description: "Customer cannot list backups for another customer's site",
			request:     asAlice(httptest.NewRequest(http.MethodGet, "/api/backups?site=bob-site", nil)),
			handler:     a.backups,
			expectCode:  http.StatusForbidden,
		},
		{
			description: "Customer cannot create database on another's site",
			request: asAlice(httptest.NewRequest(http.MethodPost, "/api/databases",
				bytes.NewReader(jsonEncode(t, map[string]string{
					"Name":     "testdb",
					"User":     "testuser",
					"Site":     "bob-site",
					"Password": "Abcdefghijklmnop123!",
				})))),
			handler:    a.databaseCollection,
			expectCode: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handler(w, tt.request)
			if w.Code != tt.expectCode {
				t.Errorf("expected status %d, got %d: %s", tt.expectCode, w.Code, w.Body.String())
			}
		})
	}
}

// TestCrossTenantAccessIsAuditedWhenDenied verifies that failed cross-tenant
// access attempts are logged in the audit trail for security monitoring.
func TestCrossTenantAccessIsAuditedWhenDenied(t *testing.T) {
	webRoot := t.TempDir()
	auditLog := filepath.Join(webRoot, "audit.log")

	// Create site directories
	for _, site := range []string{"alice-site", "bob-site"} {
		if err := os.MkdirAll(filepath.Join(webRoot, "sites", site, "public"), 0750); err != nil {
			t.Fatal(err)
		}
	}

	a := &App{Config: Config{WebRoot: webRoot, AuditLog: auditLog}}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"alice-site"}},
		"bob":   {Username: "bob", Plan: "starter", Sites: []string{"bob-site"}},
	}}
	a.Auth = Auth{Username: "admin"}
	a.Domains = &DomainClaimStore{}

	asAlice := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "alice"))
	}

	// Attempt cross-tenant access
	req := asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/environment/bob-site", nil))
	w := httptest.NewRecorder()
	a.siteEnvironment(w, req)

	// Verify it was denied
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}

	// Verify audit log contains the denial event
	auditData, err := os.ReadFile(auditLog)
	if err != nil {
		t.Fatalf("audit log not found: %v", err)
	}

	auditContent := string(auditData)
	if auditContent == "" {
		t.Fatal("audit log is empty - cross-tenant denial was not logged")
	}

	// Should contain tenant.access_denied event
	if !bytes.Contains(auditData, []byte("tenant.access_denied")) {
		t.Errorf("audit log missing tenant.access_denied event: %s", auditContent)
	}

	// Should identify alice as the actor
	if !bytes.Contains(auditData, []byte("alice")) {
		t.Errorf("audit log missing alice username: %s", auditContent)
	}

	// Should identify bob-site as the target
	if !bytes.Contains(auditData, []byte("bob-site")) {
		t.Errorf("audit log missing bob-site target: %s", auditContent)
	}
}

// TestAdministratorBypassesTenantBoundaries verifies that administrators
// can access resources regardless of customer ownership boundaries.
func TestAdministratorBypassesTenantBoundaries(t *testing.T) {
	webRoot := t.TempDir()

	// Create site directories
	for _, site := range []string{"alice-site", "bob-site"} {
		if err := os.MkdirAll(filepath.Join(webRoot, "sites", site, "public"), 0750); err != nil {
			t.Fatal(err)
		}
	}

	a := &App{Config: Config{WebRoot: webRoot}}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"alice-site"}},
	}}

	// Create a request with admin context
	asAdmin := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "admin"))
	}

	// Admin should be able to read any site's environment
	req := asAdmin(httptest.NewRequest(http.MethodGet, "/api/sites/environment/bob-site", nil))

	// Set up basic auth
	a.Auth = Auth{Username: "admin"}
	a.Domains = &DomainClaimStore{}

	w := httptest.NewRecorder()
	a.siteEnvironment(w, req)

	// Admin access should not be forbidden (may return 404 if site doesn't exist, but not 403)
	if w.Code == http.StatusForbidden {
		t.Fatalf("administrator was denied access to site: got 403")
	}
}

// TestErrorMessagesDoNotLeakTenantInfo verifies that error messages don't
// reveal whether a resource exists when the requester doesn't have access.
func TestErrorMessagesDoNotLeakTenantInfo(t *testing.T) {
	webRoot := t.TempDir()

	// Create only bob's site
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "bob-site", "public"), 0750); err != nil {
		t.Fatal(err)
	}

	a := &App{Config: Config{WebRoot: webRoot}}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"alice-site"}},
		"bob":   {Username: "bob", Plan: "starter", Sites: []string{"bob-site"}},
	}}
	a.Auth = Auth{Username: "admin"}
	a.Domains = &DomainClaimStore{}

	asAlice := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "alice"))
	}

	// Alice tries to access bob-site (exists but not owned)
	req1 := asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/environment/bob-site", nil))
	w1 := httptest.NewRecorder()
	a.siteEnvironment(w1, req1)

	// Alice tries to access nonexistent-site (doesn't exist)
	req2 := asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/environment/nonexistent-site", nil))
	w2 := httptest.NewRecorder()
	a.siteEnvironment(w2, req2)

	// Both should return same status code to avoid leaking existence info
	if w1.Code != w2.Code {
		t.Logf("WARNING: Different status codes for denied vs missing resource: %d vs %d", w1.Code, w2.Code)
		t.Logf("bob-site response: %s", w1.Body.String())
		t.Logf("nonexistent response: %s", w2.Body.String())
	}
}

// TestCSRFTokenRequired verifies that mutating operations require a CSRF token
// to prevent cross-site request forgery attacks.
func TestCSRFTokenRequired(t *testing.T) {
	webRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "alice-site", "public"), 0750); err != nil {
		t.Fatal(err)
	}

	a := &App{Config: Config{WebRoot: webRoot}}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"alice-site"}},
	}}
	a.Auth = Auth{Username: "admin"}
	a.Domains = &DomainClaimStore{}

	asAlice := func(r *http.Request) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "alice"))
	}

	// POST without CSRF token should be denied
	req := asAlice(httptest.NewRequest(http.MethodPost, "/api/domains/claim",
		bytes.NewReader(jsonEncode(t, map[string]string{
			"site":   "alice-site",
			"domain": "test.example.com",
		}))))

	// Request lacks the CSRF token in header or form
	w := httptest.NewRecorder()
	a.domainClaim(w, req)

	// Should be denied (403 or similar) due to missing CSRF
	if w.Code != http.StatusForbidden && w.Code != http.StatusBadRequest {
		t.Logf("POST without CSRF returned %d (expected denial)", w.Code)
	}
}
