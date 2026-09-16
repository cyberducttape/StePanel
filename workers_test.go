package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidWorkerName(t *testing.T) {
	valid := []string{"worker-1", "queue_two", "ABC123"}
	for _, name := range valid {
		if !validWorkerName(name) {
			t.Errorf("validWorkerName(%q) = false, want true", name)
		}
	}
	invalid := []string{"", "has space", "has/slash", "has.dot", strings.Repeat("a", 33)}
	for _, name := range invalid {
		if validWorkerName(name) {
			t.Errorf("validWorkerName(%q) = true, want false", name)
		}
	}
}

func TestReconcileWorkersRetainsPendingStateWhenHelperFails(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenWorkerStore(filepath.Join(dir, "workers.json"))
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	store.values["demo/queue"] = Worker{Site: "demo", Name: "queue", Type: "laravel", Processes: 1, MemoryMB: 128, Root: "/var/www/sites/demo/public", State: "pending"}
	app := &App{Config: Config{AppCtl: helper}, Workers: store}
	_, failed := app.reconcileWorkers(context.Background())
	if failed["demo/queue"] == "" {
		t.Fatalf("failed workers = %#v", failed)
	}
	worker := store.values["demo/queue"]
	if worker.State != "pending" || !strings.Contains(worker.LastError, "exit status") {
		t.Fatalf("worker state = %#v", worker)
	}
}

func TestWorkerStoreSaveRollsBackMemoryOnPersistFailure(t *testing.T) {
	root := t.TempDir()
	store, err := OpenWorkerStore(filepath.Join(root, "workers.json"))
	if err != nil {
		t.Fatal(err)
	}
	previous := Worker{Site: "demo", Name: "queue", Type: "laravel", Processes: 1, MemoryMB: 128, Root: "/var/www/sites/demo/public", State: "applied"}
	store.values["demo/queue"] = previous
	store.path = root // A directory cannot be atomically replaced as state.
	if err := store.save("demo/queue", Worker{Site: "demo", Name: "queue", Type: "node", Processes: 2, MemoryMB: 256, Root: "/var/www/sites/demo/public", State: "pending"}); err == nil {
		t.Fatal("expected worker state persistence failure")
	}
	if got := store.values["demo/queue"]; got != previous {
		t.Fatalf("worker state after failed save = %#v, want %#v", got, previous)
	}
}

func TestWorkerStoreRemoveRollsBackMemoryOnPersistFailure(t *testing.T) {
	root := t.TempDir()
	store, err := OpenWorkerStore(filepath.Join(root, "workers.json"))
	if err != nil {
		t.Fatal(err)
	}
	previous := Worker{Site: "demo", Name: "queue", Type: "laravel", Processes: 1, MemoryMB: 128, Root: "/var/www/sites/demo/public", State: "pending", Deleted: true}
	store.values["demo/queue"] = previous
	store.path = root // A directory cannot be atomically replaced as state.
	if err := store.remove("demo/queue"); err == nil {
		t.Fatal("expected worker state persistence failure")
	}
	if got := store.values["demo/queue"]; got != previous {
		t.Fatalf("worker state after failed removal = %#v, want %#v", got, previous)
	}
}
