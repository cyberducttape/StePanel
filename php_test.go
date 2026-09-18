package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPHPProfile(site, state string) PHPProfile {
	return PHPProfile{Site: site, Version: "8.3", MemoryLimit: "128M", MaxExecutionTime: 60, UploadMaxFilesize: "32M", PostMaxSize: "32M", MaxInputVars: 1000, ErrorReporting: "E_ALL", State: state}
}

func TestPHPProfileSaveRestoresMemoryOnPersistenceFailure(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	store := &PHPProfileStore{path: filepath.Join(blocked, "profiles.json"), values: map[string]PHPProfile{"demo": testPHPProfile("demo", "applied")}}
	if err := store.save(AuthorizedSite{site: "demo"}, testPHPProfile("demo", "pending")); err == nil {
		t.Fatal("expected persistence failure")
	}
	if got := store.values["demo"].State; got != "applied" {
		t.Fatalf("profile state after failed save = %q", got)
	}
}

func TestReconcilePHPProfilesRetainsPendingStateWhenHelperFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	store := &PHPProfileStore{path: path, values: map[string]PHPProfile{"demo": testPHPProfile("demo", "pending")}}
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{SiteCtl: helper}, PHP: store}
	_, failed := app.reconcilePHPProfiles(context.Background())
	if failed["demo"] == "" || store.values["demo"].State != "pending" || !strings.Contains(store.values["demo"].LastError, "exit status") {
		t.Fatalf("failed=%#v profile=%#v", failed, store.values["demo"])
	}
}
