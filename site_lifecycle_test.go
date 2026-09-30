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
	if err := app.ensureTerminationOffsiteBackup(context.Background(), BackupResult{Site: "customer-site", Path: "/nonexistent"}); err != nil {
		t.Fatalf("expected no-op when offsite backup is not required: %v", err)
	}
}

func TestEnsureTerminationOffsiteBackupBlocksOnUploadFailure(t *testing.T) {
	app := &App{Config: Config{RequireOffsiteBackup: true, OffsiteTarget: "s3:stepanel-test-bucket/site"}}
	backup := BackupResult{Site: "customer-site", Path: filepath.Join(t.TempDir(), "backup.tar.gz")}
	if err := app.ensureTerminationOffsiteBackup(context.Background(), backup); err == nil {
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

func TestRemoveSiteStateRestoresLocalEntriesWhenPersistenceFails(t *testing.T) {
	const site = "customer"
	tests := []struct {
		name  string
		setup func(path string) (*App, func() bool)
	}{
		{
			name: "access",
			setup: func(path string) (*App, func() bool) {
				store := &SiteAccessStore{path: path, values: map[string]SiteAccess{site: {Site: site, ShellEnabled: true}}}
				return &App{Access: store}, func() bool { return store.values[site].ShellEnabled }
			},
		},
		{
			name: "environment",
			setup: func(path string) (*App, func() bool) {
				store := &EnvironmentStore{path: path, values: map[string]map[string]environmentValue{site: {"APP_ENV": {Value: "production"}}}}
				return &App{Environments: store}, func() bool { return store.values[site]["APP_ENV"].Value == "production" }
			},
		},
		{
			name: "redis",
			setup: func(path string) (*App, func() bool) {
				store := &RedisAllocationStore{path: path, values: map[string]RedisAllocation{site: {Site: site, MemoryMB: 128}}}
				return &App{Redis: store}, func() bool { return store.values[site].MemoryMB == 128 }
			},
		},
		{
			name: "resources",
			setup: func(path string) (*App, func() bool) {
				store := &ResourceStore{path: path, values: map[string]ResourceProfile{site: {Site: site, MemoryMB: 512}}}
				return &App{Resources: store}, func() bool { return store.values[site].MemoryMB == 512 }
			},
		},
		{
			name: "php",
			setup: func(path string) (*App, func() bool) {
				store := &PHPProfileStore{path: path, values: map[string]PHPProfile{site: {Site: site, Version: "8.3"}}}
				return &App{PHP: store}, func() bool { return store.values[site].Version == "8.3" }
			},
		},
		{
			name: "composer",
			setup: func(path string) (*App, func() bool) {
				store := &ComposerStore{path: path, latest: map[string]ComposerOperation{site: {Site: site, Command: "composer install"}}}
				return &App{Composer: store}, func() bool { return store.latest[site].Command == "composer install" }
			},
		},
		{
			name: "workers",
			setup: func(path string) (*App, func() bool) {
				store := &WorkerStore{path: path, values: map[string]Worker{"customer/queue": {Site: site, Name: "queue", Type: "laravel"}}}
				return &App{Workers: store}, func() bool { _, ok := store.values["customer/queue"]; return ok }
			},
		},
		{
			name: "tasks",
			setup: func(path string) (*App, func() bool) {
				store := &TaskStore{path: path, values: map[string]ScheduledTask{"customer/nightly": {Site: site, Name: "nightly", Runtime: "php"}}}
				return &App{Tasks: store}, func() bool { _, ok := store.values["customer/nightly"]; return ok }
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			blocker := filepath.Join(root, "not-a-directory")
			if err := os.WriteFile(blocker, []byte("block"), 0600); err != nil {
				t.Fatal(err)
			}
			app, retained := tt.setup(filepath.Join(blocker, "state.json"))
			if err := app.removeSiteState(context.Background(), AuthorizedSite{site: site}); err == nil {
				t.Fatal("expected durable state deletion to fail")
			}
			if !retained() {
				t.Fatal("failed persistence discarded the in-memory site state")
			}
		})
	}
}
