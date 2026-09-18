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
		{
			name: "GET workers for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/workers/bob-site", nil))
			},
			handle: a.workers,
		},
		{
			name: "GET tasks for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/tasks/bob-site", nil))
			},
			handle: a.tasks,
		},
		{
			name: "GET logs for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/logs/bob-site", nil))
			},
			handle: a.siteLogs,
		},
		{
			name: "GET disk usage for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/usage/bob-site", nil))
			},
			handle: a.siteUsage,
		},
		{
			name: "GET resource profile for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/resources/bob-site", nil))
			},
			handle: a.siteResources,
		},
		{
			name: "GET deploy key for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/git-key/bob-site", nil))
			},
			handle: a.siteGitKey,
		},
		{
			name: "GET composer state for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/composer/bob-site", nil))
			},
			handle: a.composer,
		},
		{
			name: "GET PHP runtime for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/php/bob-site", nil))
			},
			handle: a.phpRuntime,
		},
		{
			name: "GET Redis config for another tenant's site",
			request: func() *http.Request {
				return asAlice(httptest.NewRequest(http.MethodGet, "/api/sites/redis/bob-site", nil))
			},
			handle: a.siteRedis,
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

// TestCrossTenantDenialIsAudited confirms that a refused cross-tenant attempt
// leaves a tamper-evident trail. Before this, a.canAccessSite denials were
// silent: an attacker probing every other tenant's site through any of the
// endpoints above left no record for an operator to notice or investigate.
func TestCrossTenantDenialIsAudited(t *testing.T) {
	t.Setenv("STEPANEL_SESSION_SECRET", "12345678901234567890123456789012")
	auditPath := filepath.Join(t.TempDir(), "audit.log")

	a := &App{Config: Config{AuditLog: auditPath}}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"alice-site"}},
		"bob":   {Username: "bob", Plan: "starter", Sites: []string{"bob-site"}},
	}}
	a.Auth = Auth{Username: "admin"}

	r := httptest.NewRequest(http.MethodGet, "/api/sites/environment/bob-site", nil)
	r = r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "alice"))
	w := httptest.NewRecorder()
	a.siteEnvironment(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant access = %d %s, want %d", w.Code, w.Body.String(), http.StatusForbidden)
	}

	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	var event AuditEvent
	found := false
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("decode audit event: %v", err)
		}
		if event.Action == "tenant.access_denied" && event.Actor == "alice" && event.Target == "bob-site" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a tenant.access_denied audit event for alice/bob-site, got: %s", data)
	}
}

// TestSiteManageDeniesCrossTenantRouteDeletion locks in behavior for
// siteManage, which had no prior test coverage even though it authorizes a
// destructive action (route deletion) against a route's tracked desired
// state. Migrating it to requireSiteAccess dropped a redundant
// `!a.Auth.IsAdministrator(r) &&` guard that a.canAccessSite already
// subsumes (it returns true for an administrator unconditionally); this test
// exists so that simplification, and any future change to this function,
// has a regression guard.
func TestSiteManageDeniesCrossTenantRouteDeletion(t *testing.T) {
	vhostRoot := t.TempDir()
	name := "site-bob-site-example_com.caddy"
	if err := os.WriteFile(filepath.Join(vhostRoot, name), []byte("# placeholder"), 0640); err != nil {
		t.Fatal(err)
	}
	routes := &RouteStore{path: filepath.Join(t.TempDir(), "routes.json"), values: map[string]RouteDesired{
		name: {Name: name, Site: "bob-site", Domain: "example.com", State: "applied"},
	}}

	a := &App{Config: Config{VHostRoot: vhostRoot}, Routes: routes}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"alice": {Username: "alice", Plan: "starter", Sites: []string{"alice-site"}},
		"bob":   {Username: "bob", Plan: "starter", Sites: []string{"bob-site"}},
	}}
	a.Auth = Auth{Username: "admin"}

	asUser := func(username string) *http.Request {
		r := httptest.NewRequest(http.MethodDelete, "/api/sites/"+name, nil)
		return r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, username))
	}

	w := httptest.NewRecorder()
	a.siteManage(w, asUser("alice"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("alice deleting bob's route = %d %s, want %d", w.Code, w.Body.String(), http.StatusForbidden)
	}

	w = httptest.NewRecorder()
	a.siteManage(w, asUser("bob"))
	if w.Code == http.StatusForbidden {
		t.Fatalf("bob deleting his own route was denied: %s", w.Body.String())
	}
}
