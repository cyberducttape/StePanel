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

// TestTenantIsolationMatrix is the adversarial cross-tenant check the
// project's own production-gap analysis calls for: create two customer
// accounts, each with a site the other does not own, and systematically
// attempt every customer-facing site-scoped endpoint across the tenant
// boundary. Authorization for each of these currently lives in the HTTP
// handler (a.canAccessSite), not the data-access layer, so this test exists
// to catch the exact failure mode that already happened once in this
// codebase: a handler that forgets the check entirely.
func TestTenantIsolationMatrix(t *testing.T) {
	webRoot := t.TempDir()
	// The database-creation handler validates that the target site's document
	// root exists before it reaches the ownership check, so a real cross-
	// tenant attempt needs bob-site to actually exist on disk - matching how
	// an attacker would target a real, live site rather than a made-up name.
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

	type attempt struct {
		name    string
		request func() *http.Request
		handle  func(w http.ResponseWriter, r *http.Request)
	}

	attempts := []attempt{
		{
			name: "GET environment for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/environment/bob-site", nil))
			},
			handle: a.siteEnvironment,
		},
		{
			name: "GET SSH access for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/access/bob-site", nil))
			},
			handle: a.siteAccess,
		},
		{
			name: "GET backups filtered to another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/backups?site=bob-site", nil))
			},
			handle: a.backups,
		},
		{
			name: "POST git deploy targeting another tenant's site",
			request: func() *http.Request {
				body, _ := json.Marshal(gitDeployRequest{Site: "bob-site", Repository: "https://example.com/repo.git", Ref: "main"})
				return asAlice(httptest.NewRequest(http.MethodPost, "/api/sites/git-deploy", bytes.NewReader(body)))
			},
			handle: a.gitDeploy,
		},
		{
			name: "POST database creation owned by another tenant's site",
			request: func() *http.Request {
				body, _ := json.Marshal(map[string]string{
					"Name":     "tenantdb",
					"User":     "tenantuser",
					"Site":     "bob-site",
					"Password": "Abcdefghijklmnop123!",
				})
				return asAlice(httptest.NewRequest(http.MethodPost, "/api/database", bytes.NewReader(body)))
			},
			handle: a.databaseCollection,
		},
		{
			name: "POST domain claim against another tenant's site",
			request: func() *http.Request {
				body, _ := json.Marshal(map[string]string{"site": "bob-site", "domain": "tenant-isolation-test.example"})
				return asAlice(httptest.NewRequest(http.MethodPost, "/api/domains/claim", bytes.NewReader(body)))
			},
			handle: a.domainClaim,
		},
	}

	for _, at := range attempts {
		t.Run(at.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			at.handle(w, at.request())
			if w.Code != http.StatusForbidden {
				t.Fatalf("cross-tenant access = %d %s, want %d", w.Code, w.Body.String(), http.StatusForbidden)
			}
		})
	}
}
