package main

import (
	"path/filepath"
	"testing"
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
