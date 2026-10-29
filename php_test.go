package stepanel

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

func testPHPProfile(site, state string) PHPProfile {
	return PHPProfile{Site: site, Version: "8.3", MemoryLimit: "128M", MaxExecutionTime: 60, UploadMaxFilesize: "32M", PostMaxSize: "32M", MaxInputVars: 1000, ErrorReporting: "E_ALL", State: state}
}

func TestPHPRuntimeRequiresCurrentPreviewBeforeApplying(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "sitectl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	store := &PHPProfileStore{path: filepath.Join(dir, "profiles.json"), values: map[string]PHPProfile{"demo": testPHPProfile("demo", "applied")}}
	app := &App{Config: Config{SiteCtl: helper}, Auth: Auth{Secret: "test-session-secret"}, PHP: store}
	desired := testPHPProfile("demo", "")
	desired.Version = "8.4"
	body, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	previewResponse := httptest.NewRecorder()
	app.phpRuntime(previewResponse, httptest.NewRequest(http.MethodPost, "/api/sites/php/demo", bytes.NewReader(body)))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview status %d: %s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview ChangePreview
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil || preview.PreviewToken == "" || len(preview.RecoveryPlan) == 0 {
		t.Fatalf("preview missing token or recovery plan: %#v (%v)", preview, err)
	}

	// A profile change between preview and apply invalidates the token before
	// the helper can restart PHP-FPM.
	changed := testPHPProfile("demo", "applied")
	changed.MemoryLimit = "256M"
	if err := store.save(AuthorizedSite{site: "demo"}, changed); err != nil {
		t.Fatal(err)
	}
	desired.PreviewToken = preview.PreviewToken
	applyBody, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	applyResponse := httptest.NewRecorder()
	app.phpRuntime(applyResponse, httptest.NewRequest(http.MethodPut, "/api/sites/php/demo", bytes.NewReader(applyBody)))
	if applyResponse.Code != http.StatusConflict {
		t.Fatalf("stale preview apply status %d: %s", applyResponse.Code, applyResponse.Body.String())
	}
	if got := store.values["demo"].Version; got != changed.Version {
		t.Fatalf("stale preview mutated profile version to %q", got)
	}
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
