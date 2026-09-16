package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonDeployRejectsUnassignedCustomerSite(t *testing.T) {
	dir := t.TempDir()
	webRoot := filepath.Join(dir, "www")
	appRoot := filepath.Join(dir, "apps")
	// The target site's document root does exist, matching a real cross-
	// tenant attempt: the site is real, it just isn't assigned to this
	// customer's account.
	if err := os.MkdirAll(filepath.Join(webRoot, "sites", "victim", "public"), 0750); err != nil {
		t.Fatal(err)
	}
	a := &App{Config: Config{WebRoot: webRoot, AppRoot: appRoot}}
	a.Accounts = &AccountStore{accounts: map[string]HostingAccount{
		"customer": {Username: "customer", Plan: "starter", Sites: []string{"owned"}},
	}}
	a.Auth = Auth{Username: "admin"}
	body, err := json.Marshal(PythonApp{Site: "victim", Version: "3.13", EntryPoint: "app:app", Port: 8000, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/python/deploy", bytes.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), apiTokenUsernameKey{}, "customer"))
	w := httptest.NewRecorder()
	a.pythonDeploy(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant Python deploy = %d, want %d", w.Code, http.StatusForbidden)
	}
	if _, err := os.Stat(pythonManifestPath(appRoot, "victim")); !os.IsNotExist(err) {
		t.Fatalf("rejected deploy should not have written desired state, stat err = %v", err)
	}
}

func TestReconcilePythonAppsRetainsPendingStateWhenHelperFails(t *testing.T) {
	dir := t.TempDir()
	appRoot := filepath.Join(dir, "apps")
	app := PythonApp{Site: "demo", Version: "3.13", EntryPoint: "app:app", Port: 8000, Workers: 2, Root: "/var/www/sites/demo/public", State: "pending"}
	data, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(appRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pythonManifestPath(appRoot, app.Site), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	service := &App{Config: Config{AppRoot: appRoot, AppCtl: helper}}
	_, failed := service.reconcilePythonApps(context.Background())
	if failed[app.Site] == "" {
		t.Fatalf("failed applications = %#v", failed)
	}
	updated, err := os.ReadFile(pythonManifestPath(appRoot, app.Site))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), `"state": "pending"`) || !strings.Contains(string(updated), "exit status") {
		t.Fatalf("pending Python manifest = %q", updated)
	}
}
