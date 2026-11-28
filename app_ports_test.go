package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCustomerNodeBackendIsTheApplicationOnLocalhost(t *testing.T) {
	for _, tc := range []struct {
		backend string
		ok      bool
	}{
		{"http://127.0.0.1:3000", true},
		{"http://localhost:3000", true},
		{"http://[::1]:3000", true},
		{"http://127.0.0.2:3000", true},
		{"http://127.0.0.1:3001", false},
		{"http://127.0.0.1:8090", false},
		{"http://10.0.0.5:3000", false},
		{"http://192.168.1.10:3000", false},
		{"http://169.254.169.254:3000", false},
		{"https://127.0.0.1:3000", false},
	} {
		err := customerNodeBackendAllowed(tc.backend, 3000)
		if (err == nil) != tc.ok {
			t.Errorf("customerNodeBackendAllowed(%q, 3000) = %v, want ok=%v", tc.backend, err, tc.ok)
		}
	}
}

func writeTestAppManifest(t *testing.T, cfg Config, site, suffix string, port int) {
	t.Helper()
	data, err := json.Marshal(AppManifest{Site: site, Domain: site + ".example.test", Version: "22.1.0", Port: port, State: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.AppRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.AppRoot, site+suffix), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAppPortConflict(t *testing.T) {
	cfg := Config{AppRoot: filepath.Join(t.TempDir(), "apps")}
	if err := appPortConflict(cfg, "demo", appPortKindNode, 3000); err != nil {
		t.Fatalf("empty app root: %v", err)
	}
	writeTestAppManifest(t, cfg, "other", ".json", 4000)
	writeTestAppManifest(t, cfg, "other", ".json.bak", 4001)
	writeTestAppManifest(t, cfg, "demo", ".json", 3000)
	writeTestAppManifest(t, cfg, "demo", ".json.bak", 3001)
	if err := os.WriteFile(filepath.Join(cfg.AppRoot, "broken.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{4000, 4001} {
		if err := appPortConflict(cfg, "demo", appPortKindNode, port); !errors.Is(err, errAppPortTaken) {
			t.Errorf("port %d of another site = %v, want errAppPortTaken", port, err)
		}
	}
	for _, port := range []int{3000, 3001, 5000} {
		if err := appPortConflict(cfg, "demo", appPortKindNode, port); err != nil {
			t.Errorf("port %d = %v, want allowed", port, err)
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	defer listener.Close()
	busy := listener.Addr().(*net.TCPAddr).Port
	previousProbe := appPortListening
	t.Cleanup(func() { appPortListening = previousProbe })
	appPortListening = realAppPortListening
	if err := appPortConflict(cfg, "demo", appPortKindNode, busy); !errors.Is(err, errAppPortTaken) {
		t.Fatalf("listening port %d = %v, want errAppPortTaken", busy, err)
	}
	writeTestAppManifest(t, cfg, "demo", ".json", busy)
	if err := appPortConflict(cfg, "demo", appPortKindNode, busy); err != nil {
		t.Fatalf("the site's own running port %d = %v, want allowed", busy, err)
	}
}

func TestAppPortListeningProbe(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	probe := realAppPortListening
	if !probe(port) {
		t.Fatalf("probe missed the listener on %d", port)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if probe(port) {
		t.Fatalf("probe still reports %d after the listener closed", port)
	}
}

func TestCustomerNodeDeploymentCannotProxyAnotherBackend(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"panel port", `{"site":"shop","domain":"shop.example.com","node_version":"20.1.0","port":3000,"backend":"http://127.0.0.1:8090"}`, http.StatusUnprocessableEntity},
		{"private network", `{"site":"shop","domain":"shop.example.com","node_version":"20.1.0","port":3000,"backend":"http://10.0.0.5:3000"}`, http.StatusUnprocessableEntity},
		{"another site's port", `{"site":"shop","domain":"shop.example.com","node_version":"20.1.0","port":4000,"backend":"http://127.0.0.1:4000"}`, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := newSiteOperationFixture(t, http.StatusOK, `{}`)
			accounts := &AccountStore{accounts: map[string]HostingAccount{
				"customer": {Username: "customer", Role: "owner", Sites: []string{"shop"}},
			}}
			app.Accounts = accounts
			app.Auth.Accounts = accounts
			app.Config.AppRoot = filepath.Join(t.TempDir(), "apps")
			verifyTestDomain(t, app, "shop", "shop.example.com", accountTenantID(accounts.accounts["customer"]))
			writeTestAppManifest(t, app.Config, "other", ".json", 4000)
			request := tokenRequest(http.MethodPost, "/api/node/deploy", tc.body, "customer", "site:deploy")
			response := httptest.NewRecorder()
			app.startNodeDeployment(response, request)
			if response.Code != tc.want {
				t.Fatalf("status = %d, body %s; want %d", response.Code, response.Body.String(), tc.want)
			}
			if _, claimed, err := app.Jobs.ClaimNext("node-worker", "node.deployment"); err != nil || claimed {
				t.Fatalf("a refused deployment was queued: claimed=%v err=%v", claimed, err)
			}
		})
	}
}

func TestAppRollbackRefusesAPortAnotherSiteNowUses(t *testing.T) {
	a, manifestPath, argsLog := newAppStateFixture(t, 0)
	current, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	writeTestAppManifest(t, a.Config, "demo", ".json.bak", 4000)
	backup, err := os.ReadFile(manifestPath + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	var previous AppManifest
	if err := json.Unmarshal(backup, &previous); err != nil {
		t.Fatal(err)
	}
	previous.Root = filepath.Join(a.Config.WebRoot, "sites", "demo", "public")
	previous.Domain = "demo.example.test"
	data, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath+".bak", data, 0600); err != nil {
		t.Fatal(err)
	}
	writeTestAppManifest(t, a.Config, "other", ".json", 4000)
	a.Auth = Auth{Username: "admin"}
	request := appDeployTestRequest(`{}`)
	request.URL.Path = "/api/apps/demo/rollback"
	response := httptest.NewRecorder()
	a.appAction(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "no longer available") {
		t.Fatalf("status = %d, body %s; want a port conflict", response.Code, response.Body.String())
	}
	if got, err := os.ReadFile(manifestPath); err != nil || string(got) != string(current) {
		t.Fatalf("manifest after refused rollback = %q, %v", got, err)
	}
	if _, err := os.Stat(argsLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the helper ran for a refused rollback: %v", err)
	}
}

// verifyTestDomain records domain as a verified claim of site for tenant.
func verifyTestDomain(t *testing.T, app *App, site, domain, tenant string) {
	t.Helper()
	if app.Domains == nil {
		app.Domains = &DomainClaimStore{values: map[string]DomainClaim{}}
	}
	app.Domains.mu.Lock()
	defer app.Domains.mu.Unlock()
	app.Domains.values[domain] = DomainClaim{Domain: domain, Site: site, State: "verified", VerifiedAt: time.Now().UTC(), Tenant: tenant}
}

func TestCustomerNodeDeploymentRequiresAVerifiedDomain(t *testing.T) {
	app, _ := newSiteOperationFixture(t, http.StatusOK, `{}`)
	account := HostingAccount{Username: "customer", Role: "owner", Sites: []string{"shop"}}
	accounts := &AccountStore{accounts: map[string]HostingAccount{"customer": account}}
	app.Accounts = accounts
	app.Auth.Accounts = accounts
	app.Config.AppRoot = filepath.Join(t.TempDir(), "apps")
	body := `{"site":"shop","domain":"shop.example.com","node_version":"20.1.0","port":3000,"backend":"http://127.0.0.1:3000"}`
	for name, setup := range map[string]func(){
		"unclaimed":      func() {},
		"another site":   func() { verifyTestDomain(t, app, "other", "shop.example.com", accountTenantID(account)) },
		"another tenant": func() { verifyTestDomain(t, app, "shop", "shop.example.com", "tenant-elsewhere") },
	} {
		t.Run(name, func(t *testing.T) {
			app.Domains = nil
			setup()
			response := httptest.NewRecorder()
			app.startNodeDeployment(response, tokenRequest(http.MethodPost, "/api/node/deploy", body, "customer", "site:deploy"))
			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body %s; want 403", response.Code, response.Body.String())
			}
		})
	}

	// A queued customer deployment whose claim disappears is refused by
	// the worker before it changes anything.
	app.Domains = nil
	verifyTestDomain(t, app, "shop", "shop.example.com", accountTenantID(account))
	token, _, err := app.APITokens.createScoped("customer", "deployment", nil, []string{"site:deploy"}, map[string]bool{"site:deploy": true})
	if err != nil {
		t.Fatal(err)
	}
	request := tokenRequest(http.MethodPost, "/api/node/deploy", body, "customer", "site:deploy")
	request = request.WithContext(context.WithValue(request.Context(), apiTokenIDKey{}, token.ID))
	response := httptest.NewRecorder()
	app.startNodeDeployment(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("verified domain: status = %d, body %s", response.Code, response.Body.String())
	}
	app.Domains = &DomainClaimStore{values: map[string]DomainClaim{}}
	item, claimed, err := app.Jobs.ClaimNext("node-worker", "node.deployment")
	if err != nil || !claimed {
		t.Fatalf("claim Node deployment: claimed=%v err=%v", claimed, err)
	}
	if _, err := app.handleNodeDeploymentJob(context.Background(), item); err == nil || !strings.Contains(err.Error(), "no longer a verified claim") {
		t.Fatalf("worker error = %v, want the removed claim refused", err)
	}
}

func TestAppPortConflictCoversPythonApplications(t *testing.T) {
	cfg := Config{AppRoot: filepath.Join(t.TempDir(), "apps")}
	if err := savePythonApp(cfg.AppRoot, PythonApp{Site: "other", Version: "3.12", EntryPoint: "app:app", Port: 4100, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	if err := savePythonApp(cfg.AppRoot, PythonApp{Site: "demo", Version: "3.12", EntryPoint: "app:app", Port: 4200, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	// A site whose name ends -python keeps a Node manifest at the path a
	// Python manifest of site "shop" would use; its site field tells them apart.
	writeTestAppManifest(t, cfg, "shop-python", ".json", 4300)
	for _, tc := range []struct {
		site, kind string
		port       int
		taken      bool
	}{
		{"demo", appPortKindNode, 4100, true},    // another site's Python app
		{"demo", appPortKindPython, 4100, true},  // another site's Python app
		{"demo", appPortKindNode, 4200, true},    // the site's own Python app
		{"demo", appPortKindPython, 4200, false}, // redeploying the Python app
		{"shop", appPortKindPython, 4300, true},  // the Node app of shop-python
		{"shop-python", appPortKindNode, 4300, false},
		{"other", appPortKindPython, 4100, false},
	} {
		err := appPortConflict(cfg, tc.site, tc.kind, tc.port)
		if taken := errors.Is(err, errAppPortTaken); taken != tc.taken || (err != nil && !taken) {
			t.Errorf("appPortConflict(%s, %s, %d) = %v, want taken=%v", tc.site, tc.kind, tc.port, err, tc.taken)
		}
	}
}

func TestPythonDeploymentRefusesAnotherApplicationsPort(t *testing.T) {
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	app, _ := newSiteOperationFixture(t, http.StatusOK, `{}`)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sites", "shop", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "helper-ran")
	helper := filepath.Join(root, "appctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\ntouch "+shellQuote(marker)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	app.Config.WebRoot, app.Config.AppRoot, app.Config.AppCtl = root, filepath.Join(root, "apps"), helper
	writeTestAppManifest(t, app.Config, "victim", ".json", 8000)
	access, err := app.authorizeDurableSiteJob("shop", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.applyPythonDeployment(context.Background(), "admin", access, PythonApp{Site: "shop", Version: "3.13", EntryPoint: "app:app", Port: 8000, Workers: 1})
	if pythonDeploymentStatus(err) != http.StatusConflict {
		t.Fatalf("deploy onto another site's port = %v, want a conflict", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the helper ran for a refused deployment: %v", err)
	}
	if _, err := os.Stat(filepath.Join(app.Config.AppRoot, "shop-python.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused deployment left a manifest: %v", err)
	}
}
