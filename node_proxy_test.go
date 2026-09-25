package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSelectNodeDoesNotCreateUnmanagedSiteRoot(t *testing.T) {
	root := t.TempDir()
	nvmRoot := filepath.Join(root, "nvm")
	if err := os.MkdirAll(filepath.Join(nvmRoot, "versions", "node", "v20.1.0"), 0750); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(root, "www")
	app := &App{Config: Config{NVMDir: nvmRoot, WebRoot: webRoot}, Auth: Auth{Username: "admin"}}
	req := httptest.NewRequest(http.MethodPost, "/api/node/select", bytes.NewBufferString(`{"site":"unmanaged","version":"20.1.0"}`))
	response := httptest.NewRecorder()
	app.selectNode(response, req)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s; want unmanaged site rejection", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(webRoot, "sites", "unmanaged")); !os.IsNotExist(err) {
		t.Fatalf("Node selection created an unmanaged site root: %v", err)
	}
}

func TestLocalBackendValidation(t *testing.T) {
	valid := []string{"http://127.0.0.1:3000", "http://localhost:8080", "http://LOCALHOST:8080", "http://192.168.1.20:9000", "http://[::1]:3000"}
	for _, value := range valid {
		if _, err := localBackend(value); err != nil {
			t.Errorf("localBackend(%q) failed: %v", value, err)
		}
	}
	invalid := []string{"https://127.0.0.1:3000", "http://example.com:3000", "http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:not-a-port", "http://127.0.0.1:3000/admin", "http://user:pass@127.0.0.1:3000", "http://169.254.169.254:80", "http://100.100.100.200:80"}
	for _, value := range invalid {
		if _, err := localBackend(value); err == nil {
			t.Errorf("localBackend(%q) unexpectedly passed", value)
		}
	}
}

func TestLocalBackendCanonicalizesHost(t *testing.T) {
	backend, err := localBackend("http://LOCALHOST:8080/")
	if err != nil {
		t.Fatal(err)
	}
	if backend != "localhost:8080" {
		t.Fatalf("backend = %q, want localhost:8080", backend)
	}
}

func TestProxyDeleteRestoresConfigWhenReloadFails(t *testing.T) {
	root := t.TempDir()
	proxyRoot := filepath.Join(root, "proxy")
	if err := os.MkdirAll(proxyRoot, 0750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(proxyRoot, "account-example_com.conf")
	original := []byte("original config\n")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	reloader := filepath.Join(root, "reload-fails")
	if err := os.WriteFile(reloader, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{ProxyRoot: proxyRoot, ProxyCtl: reloader}, Auth: Auth{}}
	req := httptest.NewRequest(http.MethodDelete, "/api/proxy/account-example_com.conf", nil)
	response := httptest.NewRecorder()
	app.proxyManage(response, req)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	restored, err := os.ReadFile(path)
	if err != nil || string(restored) != string(original) {
		t.Fatalf("proxy helper failure changed the config: %q, %v", restored, err)
	}
}

func TestProxyConfigNameUsesWebserverExtension(t *testing.T) {
	if got := proxyConfigName("caddy", "demo", "App.Example.com"); got != "demo-app_example_com.caddy" {
		t.Fatalf("caddy proxy name = %q", got)
	}
	if got := proxyConfigName("apache", "demo", "App.Example.com"); got != "demo-app_example_com.conf" {
		t.Fatalf("apache proxy name = %q", got)
	}
}
