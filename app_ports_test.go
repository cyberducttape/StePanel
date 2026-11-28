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
	if _, err := os.Stat(filepath.Join(app.Config.AppRoot, pythonManifestDir, "shop.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused deployment left a manifest: %v", err)
	}
}

func newPythonPortLockFixture(t *testing.T, helperScript string) (*App, AuthorizedDurableSite) {
	t.Helper()
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	app, _ := newSiteOperationFixture(t, http.StatusOK, `{}`)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sites", "shop", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "appctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"+helperScript+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	app.Config.WebRoot, app.Config.AppRoot, app.Config.AppCtl = root, filepath.Join(root, "apps"), helper
	access, err := app.authorizeDurableSiteJob("shop", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	return app, access
}

// TestAppPortClaimsAreSerializedAcrossSites holds the host-wide port lock as
// another site's deployment would between its port check and its manifest
// write. The deployment must wait, then see the other site's claim.
func TestAppPortClaimsAreSerializedAcrossSites(t *testing.T) {
	app, access := newPythonPortLockFixture(t, "exit 0")
	_, releasePorts, err := app.acquireAppPortLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := app.applyPythonDeployment(context.Background(), "admin", access, PythonApp{Site: "shop", Version: "3.13", EntryPoint: "app:app", Port: 8000, Workers: 1})
		done <- err
	}()
	select {
	case err := <-done:
		releasePorts()
		t.Fatalf("deployment finished while another site held the port lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	writeTestAppManifest(t, app.Config, "victim", ".json", 8000)
	releasePorts()
	select {
	case err := <-done:
		if pythonDeploymentStatus(err) != http.StatusConflict {
			t.Fatalf("deploy after another site claimed the port = %v, want a conflict", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("deployment did not resume after the port lock was released")
	}
}

// TestPythonDeploymentReleasesThePortLockBeforeInstalling keeps a slow
// dependency install from blocking every other site's application deploy.
func TestPythonDeploymentReleasesThePortLockBeforeInstalling(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "continue")
	started := filepath.Join(t.TempDir(), "started")
	app, access := newPythonPortLockFixture(t, "touch "+shellQuote(started)+"\nwhile [ ! -e "+shellQuote(gate)+" ]; do sleep 0.05; done")
	done := make(chan error, 1)
	go func() {
		_, err := app.applyPythonDeployment(context.Background(), "admin", access, PythonApp{Site: "shop", Version: "3.13", EntryPoint: "app:app", Port: 8000, Workers: 1})
		done <- err
	}()
	defer func() {
		if err := os.WriteFile(gate, nil, 0600); err != nil {
			t.Error(err)
		}
		<-done
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the install helper never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, releasePorts, err := app.acquireAppPortLock(ctx)
	if err != nil {
		t.Fatalf("port lock still held during the install: %v", err)
	}
	releasePorts()
	if err := appPortConflict(app.Config, "other", appPortKindNode, 8000); !errors.Is(err, errAppPortTaken) {
		t.Fatalf("pending Python manifest did not claim its port during the install: %v", err)
	}
}

func writeLegacyPythonManifest(t *testing.T, cfg Config, site string, port int, state string) {
	t.Helper()
	data, err := json.Marshal(PythonApp{Site: site, Version: "3.12", EntryPoint: "app:app", Port: port, Workers: 1, State: state})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.AppRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.AppRoot, site+"-python.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestPythonManifestCannotReplaceAnotherSitesNodeManifest covers the old
// layout's collision: site foo's Python manifest was foo-python.json, the
// Node manifest path of a site named foo-python.
func TestPythonManifestCannotReplaceAnotherSitesNodeManifest(t *testing.T) {
	cfg := Config{AppRoot: filepath.Join(t.TempDir(), "apps")}
	writeTestAppManifest(t, cfg, "foo-python", ".json", 4300)
	before, err := os.ReadFile(filepath.Join(cfg.AppRoot, "foo-python.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := savePythonApp(cfg.AppRoot, PythonApp{Site: "foo", Version: "3.12", EntryPoint: "app:app", Port: 4400, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(cfg.AppRoot, "foo-python.json"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("site foo's Python manifest changed foo-python's Node manifest: %s (%v)", after, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.AppRoot, pythonManifestDir, "foo.json")); err != nil {
		t.Fatalf("Python manifest not in its own directory: %v", err)
	}
	for _, site := range []string{"../foo", "foo/bar", ""} {
		if _, err := pythonManifestPath(cfg.AppRoot, site); err == nil {
			t.Errorf("pythonManifestPath accepted site %q", site)
		}
	}
}

func TestLegacyPythonManifestsMoveToTheirDirectory(t *testing.T) {
	cfg := Config{AppRoot: filepath.Join(t.TempDir(), "apps")}
	writeLegacyPythonManifest(t, cfg, "foo", 4400, "running")
	writeTestAppManifest(t, cfg, "bar-python", ".json", 4500) // a Node manifest, not legacy Python
	// A newer manifest at the new path wins over a stale legacy one.
	writeLegacyPythonManifest(t, cfg, "baz", 4600, "running")
	if err := savePythonApp(cfg.AppRoot, PythonApp{Site: "baz", Version: "3.13", EntryPoint: "app:app", Port: 4700, Workers: 1, State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := appPortConflict(cfg, "other", appPortKindNode, 4400); !errors.Is(err, errAppPortTaken) {
		t.Fatalf("an unmigrated legacy Python manifest did not claim its port: %v", err)
	}
	_, failed := (&App{Config: cfg}).reconcilePythonApps(context.Background())
	if len(failed) != 0 {
		t.Fatalf("reconcile failures: %#v", failed)
	}
	for _, gone := range []string{"foo-python.json", "baz-python.json"} {
		if _, err := os.Stat(filepath.Join(cfg.AppRoot, gone)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("legacy manifest %s was not moved: %v", gone, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.AppRoot, "bar-python.json")); err != nil {
		t.Fatalf("site bar-python's Node manifest was moved or removed: %v", err)
	}
	for site, port := range map[string]int{"foo": 4400, "baz": 4700} {
		path, err := pythonManifestPath(cfg.AppRoot, site)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		var app PythonApp
		if err != nil || json.Unmarshal(data, &app) != nil || app.Site != site || app.Port != port {
			t.Errorf("Python manifest for %s = %s (%v), want port %d", site, data, err, port)
		}
	}
	if err := appPortConflict(cfg, "other", appPortKindNode, 4600); err != nil {
		t.Errorf("the stale legacy manifest's port is still claimed: %v", err)
	}
}

func TestManagedAppsListsOnlyNodeManifests(t *testing.T) {
	cfg := Config{AppRoot: filepath.Join(t.TempDir(), "apps")}
	writeTestAppManifest(t, cfg, "shop", ".json", 4100)
	writeTestAppManifest(t, cfg, "shop", ".json.bak", 4000)
	writeTestAppManifest(t, cfg, "foo-python", ".json", 4200)
	writeLegacyPythonManifest(t, cfg, "bar", 4300, "running")
	if err := savePythonApp(cfg.AppRoot, PythonApp{Site: "baz", Version: "3.12", EntryPoint: "app:app", Port: 4400, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.AppRoot, ".activation-0123.json"), []byte(`{"site":"shop","state":"runtime_applied"}`), 0600); err != nil {
		t.Fatal(err)
	}
	apps, err := managedAppsWithError(cfg.AppRoot)
	if err != nil {
		t.Fatal(err)
	}
	var sites []string
	for _, app := range apps {
		sites = append(sites, app.Site)
	}
	if strings.Join(sites, ",") != "foo-python,shop" {
		t.Fatalf("managed Node apps = %v, want foo-python and shop only", sites)
	}
}

func TestRemoveSiteAppStateForgetsOnlyThatSite(t *testing.T) {
	cfg := Config{AppRoot: filepath.Join(t.TempDir(), "apps")}
	writeTestAppManifest(t, cfg, "shop", ".json", 4100)
	writeTestAppManifest(t, cfg, "shop", ".json.bak", 4000)
	writeLegacyPythonManifest(t, cfg, "shop", 4200, "running")
	if err := savePythonApp(cfg.AppRoot, PythonApp{Site: "shop", Version: "3.12", EntryPoint: "app:app", Port: 4300, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	writeTestAppManifest(t, cfg, "shopping", ".json", 4400)
	if err := removeSiteAppState(cfg, "shop"); err != nil {
		t.Fatal(err)
	}
	owners, err := appPortOwners(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0].site != "shopping" {
		t.Fatalf("port owners after removing shop = %+v, want only shopping", owners)
	}
	// A legacy-path file that is another site's Node manifest stays.
	writeTestAppManifest(t, cfg, "shop-python", ".json", 4500)
	if err := removeSiteAppState(cfg, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.AppRoot, "shop-python.json")); err != nil {
		t.Fatalf("removing shop's state removed site shop-python's Node manifest: %v", err)
	}
	if err := removeSiteAppState(Config{AppRoot: filepath.Join(t.TempDir(), "missing")}, "shop"); err != nil {
		t.Fatalf("removing state from a missing application directory: %v", err)
	}
}

// TestSiteServiceRemovalForgetsApplicationState: a deleted site's manifests
// would otherwise keep its ports claimed and hand a later site of the same
// name the old release as its rollback target.
func TestSiteServiceRemovalForgetsApplicationState(t *testing.T) {
	app, _ := newSiteOperationFixture(t, http.StatusOK, `{}`)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sites", "shop", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	ok := filepath.Join(root, "ok")
	if err := os.WriteFile(ok, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	app.Config.WebRoot, app.Config.AppRoot, app.Config.AppCtl, app.Config.SiteCtl, app.Config.GitCtl = root, filepath.Join(root, "apps"), ok, ok, ""
	writeTestAppManifest(t, app.Config, "shop", ".json", 4100)
	writeTestAppManifest(t, app.Config, "shop", ".json.bak", 4000)
	if err := savePythonApp(app.Config.AppRoot, PythonApp{Site: "shop", Version: "3.12", EntryPoint: "app:app", Port: 4200, Workers: 1}); err != nil {
		t.Fatal(err)
	}
	writeTestAppManifest(t, app.Config, "other", ".json", 4300)
	access, err := app.authorizeDurableSiteJob("shop", "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	app.siteManager = nil
	if err := app.removeSiteServices(context.Background(), access); err != nil {
		t.Fatal(err)
	}
	owners, err := appPortOwners(app.Config)
	if err != nil {
		t.Fatal(err)
	}
	if len(owners) != 1 || owners[0].site != "other" {
		t.Fatalf("application state after deleting shop = %+v, want only other's", owners)
	}
}
