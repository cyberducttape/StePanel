package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeploymentStorePersistsNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deployments.json")
	store, err := OpenDeploymentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.add(Deployment{ID: "one", Site: "site", Stage: "build", State: "completed", CreatedAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.add(Deployment{ID: "two", Site: "site", Stage: "activation", State: "completed", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDeploymentStore(path)
	if err != nil {
		t.Fatal(err)
	}
	items := reopened.list("site")
	if len(items) != 2 || items[0].ID != "two" {
		t.Fatalf("deployment order = %#v", items)
	}
}

func TestRecordDeploymentFailureIsReturnedAndMarksReadiness(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenDeploymentStore(filepath.Join(dir, "deployments.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	app := &App{Deployments: store}
	err = app.recordDeployment("site", "activation", "running", "started", gitDeployResult{DeploymentID: "deployment-1"}, "")
	if err == nil {
		t.Skip("state directory remained writable (running as root?)")
	}
	if app.recovery.get() == nil {
		t.Fatal("deployment history failure did not mark readiness unhealthy")
	}
}

func TestDeploymentHistorySpoolsAndReplaysAfterStoreFailure(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenDeploymentStore(filepath.Join(dir, "deployments.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{Config: Config{RecoveryRoot: filepath.Join(root, "recovery")}, Deployments: store}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	err = app.recordDeployment("site", "activation", "completed", "release live", gitDeployResult{DeploymentID: "deployment-1", Commit: "abc"}, "")
	if err == nil {
		t.Skip("state directory remained writable (running as root?)")
	}
	if !strings.Contains(err.Error(), "spooled") {
		t.Fatalf("post-activation failure was not spooled: %v", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	replayed, err := app.replaySpooledDeployments()
	if err != nil || replayed != 1 {
		t.Fatalf("replayed = %d, err = %v; want 1", replayed, err)
	}
	items := store.list("site")
	if len(items) != 1 || items[0].ID != "deployment-1" || items[0].State != "completed" {
		t.Fatalf("replayed history = %#v", items)
	}
	if replayed, err := app.replaySpooledDeployments(); err != nil || replayed != 0 {
		t.Fatalf("second replay = %d, %v; spool was not cleared", replayed, err)
	}
}
