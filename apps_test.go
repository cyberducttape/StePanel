package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
