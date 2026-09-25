package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureTerminationOffsiteBackupSkippedWhenNotRequired(t *testing.T) {
	app := &App{Config: Config{RequireOffsiteBackup: false}}
	if err := app.ensureTerminationOffsiteBackup(BackupResult{Site: "customer-site", Path: "/nonexistent"}); err != nil {
		t.Fatalf("expected no-op when offsite backup is not required: %v", err)
	}
}

func TestEnsureTerminationOffsiteBackupBlocksOnUploadFailure(t *testing.T) {
	app := &App{Config: Config{RequireOffsiteBackup: true, OffsiteTarget: "s3:stepanel-test-bucket/site"}}
	backup := BackupResult{Site: "customer-site", Path: filepath.Join(t.TempDir(), "backup.tar.gz")}
	if err := app.ensureTerminationOffsiteBackup(backup); err == nil {
		t.Fatal("expected site termination to be blocked when the offsite upload cannot succeed")
	}
}

func TestSiteTerminationEnqueueIsIdempotent(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := &App{Jobs: newJobsWithDB(db, 1)}
	first, err := app.enqueueSiteTermination("customer-site", "admin")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.enqueueSiteTermination("customer-site", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("termination jobs were not idempotent: %q != %q", first.ID, second.ID)
	}
	item, ok := app.Jobs.Get(first.ID)
	if !ok || item.Kind != "site.terminate" || string(item.Payload) != `{"site":"customer-site","actor":"admin"}` {
		t.Fatalf("unexpected durable termination job: %#v, %v", item, ok)
	}
}

func TestSiteTerminationFailureInjectionStopsBeforeDestructiveWork(t *testing.T) {
	t.Setenv("STEPANEL_FAIL_AT", "terminate:init")
	t.Setenv("STEPANEL_AUDIT_KEY", strings.Repeat("k", 32))
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	siteRoot := filepath.Join(webRoot, "sites", "customer-site", "public")
	if err := os.MkdirAll(siteRoot, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(siteRoot, "index.html"), []byte("live"), 0640); err != nil {
		t.Fatal(err)
	}
	app := &App{
		Config: Config{
			WebRoot:      webRoot,
			RecoveryRoot: filepath.Join(root, "recovery"),
			AuditLog:     filepath.Join(root, "audit.jsonl"),
		},
		Auth: Auth{Username: "admin"},
		Jobs: NewJobs(),
	}
	item := Job{ID: "terminate-test", StartedAt: time.Now().UTC()}
	item.Payload, _ = json.Marshal(durableSiteTerminationRequest{Site: "customer-site", Actor: "admin"})
	if _, err := app.handleSiteTermination(context.Background(), item); err == nil || !strings.Contains(err.Error(), "failure injection") {
		t.Fatalf("termination error = %v, want injected failure", err)
	}
	if _, err := os.Stat(filepath.Join(siteRoot, "index.html")); err != nil {
		t.Fatalf("termination removed site before injected init failure: %v", err)
	}
}
