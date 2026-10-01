package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAppDeployPersistsRunningManifest(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "web")
	publicRoot := filepath.Join(webRoot, "sites", "demo", "public")
	if err := os.MkdirAll(publicRoot, 0750); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "appctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	appRoot := filepath.Join(root, "apps")
	a := &App{Config: Config{WebRoot: webRoot, AppRoot: appRoot, AppCtl: helper}, Auth: Auth{}}
	body := `{"site":"demo","domain":"demo.example.test","node_version":"v22.1.0","port":3000}`
	request := httptest.NewRequest(http.MethodPost, "/api/apps/deploy", strings.NewReader(body))
	response := httptest.NewRecorder()
	a.appDeploy(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(appRoot, "demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest AppManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.State != "running" || manifest.Root != publicRoot {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestAppDeployRestoresManifestWhenHelperFails(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "demo", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "appctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	appRoot := filepath.Join(root, "apps")
	if err := os.MkdirAll(appRoot, 0750); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(appRoot, "demo.json")
	original := []byte("previous manifest\n")
	if err := os.WriteFile(manifestPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{Config: Config{WebRoot: webRoot, AppRoot: appRoot, AppCtl: helper}, Auth: Auth{}}
	body := `{"site":"demo","domain":"demo.example.test","node_version":"v22.1.0","port":3000}`
	request := httptest.NewRequest(http.MethodPost, "/api/apps/deploy", strings.NewReader(body))
	response := httptest.NewRecorder()
	a.appDeploy(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil || string(data) != string(original) {
		t.Fatalf("manifest = %q, error = %v", data, err)
	}
}

func TestValidAppManifestRejectsUnexpectedRoot(t *testing.T) {
	cfg := Config{WebRoot: t.TempDir()}
	manifest := AppManifest{Site: "demo", Domain: "demo.example.test", Version: "v22.1.0", Port: 3000, Root: "/tmp/unmanaged"}
	if validAppManifest(cfg, manifest, "demo") {
		t.Fatal("manifest outside the managed site root was accepted")
	}
}

func TestAppListReportsManifestDirectoryReadFailure(t *testing.T) {
	root := t.TempDir()
	appRoot := filepath.Join(root, "apps")
	if err := os.WriteFile(appRoot, []byte("not-a-directory"), 0600); err != nil {
		t.Fatal(err)
	}

	app := &App{Config: Config{AppRoot: appRoot}}
	response := httptest.NewRecorder()
	app.appList(response, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s; want manifest read failure", response.Code, response.Body.String())
	}
}

func TestAppListReportsCorruptManifest(t *testing.T) {
	appRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(appRoot, "broken.json"), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}

	app := &App{Config: Config{AppRoot: appRoot}}
	response := httptest.NewRecorder()
	app.appList(response, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s; want corrupt manifest failure", response.Code, response.Body.String())
	}
}

func TestValidAppManifestRejectsSymlinkedSiteRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	webRoot := filepath.Join(root, "web")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(webRoot, "sites", "demo")); err != nil {
		t.Fatal(err)
	}
	manifest := AppManifest{Site: "demo", Domain: "demo.example.test", Version: "v22.1.0", Port: 3000, Root: filepath.Join(webRoot, "sites", "demo", "public")}
	if validAppManifest(Config{WebRoot: webRoot}, manifest, "demo") {
		t.Fatal("manifest under a symlinked site root was accepted")
	}
}

// newAppStateFixture writes a valid running manifest for site demo and a fake
// appctl that records its arguments and exits with exitCode.
func newAppStateFixture(t *testing.T, exitCode int) (*App, string, string) {
	t.Helper()
	root := t.TempDir()
	webRoot := filepath.Join(root, "web")
	publicRoot := filepath.Join(webRoot, "sites", "demo", "public")
	if err := os.MkdirAll(publicRoot, 0750); err != nil {
		t.Fatal(err)
	}
	argsLog := filepath.Join(root, "args")
	helper := filepath.Join(root, "appctl")
	script := "#!/bin/sh\necho \"$@\" > '" + argsLog + "'\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(helper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	appRoot := filepath.Join(root, "apps")
	if err := os.MkdirAll(appRoot, 0750); err != nil {
		t.Fatal(err)
	}
	manifest := AppManifest{Site: "demo", Domain: "demo.example.test", Version: "22.1.0", Port: 3000, Root: publicRoot, State: "running"}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(appRoot, "demo.json")
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return &App{Config: Config{WebRoot: webRoot, AppRoot: appRoot, AppCtl: helper}}, manifestPath, argsLog
}

func readAppState(t *testing.T, manifestPath string) string {
	t.Helper()
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest AppManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest.State
}

func TestAppStateActionPersistsState(t *testing.T) {
	a, manifestPath, argsLog := newAppStateFixture(t, 0)
	for _, step := range []struct{ action, state string }{{"stop", "stopped"}, {"start", "running"}, {"stop", "stopped"}, {"restart", "running"}} {
		if status, message, err := a.runAppStateAction(context.Background(), "demo", step.action); err != nil {
			t.Fatalf("%s: status = %d, message = %q, err = %v", step.action, status, message, err)
		}
		if got := readAppState(t, manifestPath); got != step.state {
			t.Fatalf("after %s state = %q, want %q", step.action, got, step.state)
		}
		if args, err := os.ReadFile(argsLog); err != nil || strings.TrimSpace(string(args)) != step.action+" demo" {
			t.Fatalf("after %s helper args = %q, err = %v", step.action, args, err)
		}
	}
}

func TestAppStateActionRestoresManifestWhenHelperFails(t *testing.T) {
	a, manifestPath, _ := newAppStateFixture(t, 1)
	original, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	status, _, err := a.runAppStateAction(context.Background(), "demo", "stop")
	if err == nil || status != http.StatusBadGateway {
		t.Fatalf("status = %d, err = %v, want 502 failure", status, err)
	}
	if data, err := os.ReadFile(manifestPath); err != nil || string(data) != string(original) {
		t.Fatalf("manifest = %q, error = %v, want original", data, err)
	}
}

func TestAppStateActionRequiresDeployedApp(t *testing.T) {
	a, manifestPath, argsLog := newAppStateFixture(t, 0)
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	status, _, err := a.runAppStateAction(context.Background(), "demo", "start")
	if err == nil || status != http.StatusConflict {
		t.Fatalf("status = %d, err = %v, want 409 failure", status, err)
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("helper ran for an undeployed app (stat err = %v)", err)
	}
}

func TestMetadataCacheInvalidateApps(t *testing.T) {
	cache := NewMetadataCache(time.Minute)
	cache.SetApps([]AppManifest{{Site: "demo", State: "running"}})
	if _, ok := cache.GetApps(); !ok {
		t.Fatal("apps were not cached")
	}
	cache.InvalidateApps()
	if _, ok := cache.GetApps(); ok {
		t.Fatal("apps remained cached after invalidation")
	}
}
