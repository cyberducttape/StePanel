package main

import (
	"os"
	"path/filepath"
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
