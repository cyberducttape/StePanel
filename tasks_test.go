package main

import (
	"path/filepath"
	"testing"
)

func TestValidTaskRuntime(t *testing.T) {
	for _, runtime := range []string{"php", "node", "python", "shell"} {
		if !validTaskRuntime(runtime) {
			t.Errorf("validTaskRuntime(%q) = false, want true", runtime)
		}
	}
	for _, runtime := range []string{"", "ruby", "PHP", "shell "} {
		if validTaskRuntime(runtime) {
			t.Errorf("validTaskRuntime(%q) = true, want false", runtime)
		}
	}
}

func TestFinalizeTaskDeletionRollsBackMemoryOnPersistFailure(t *testing.T) {
	root := t.TempDir()
	store, err := OpenTaskStore(filepath.Join(root, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.path = root // Deliberately make the file target a directory.
	task := ScheduledTask{Site: "demo", Name: "nightly", Runtime: "shell", Command: "true", State: "pending", Deleted: true}
	key := task.Site + "/" + task.Name
	store.values[key] = task
	app := &App{Tasks: store}

	store.mu.Lock()
	err = app.finalizeTaskDeletionLocked(key, task)
	store.mu.Unlock()
	if err == nil {
		t.Fatal("expected task state persistence failure")
	}
	if got, ok := store.values[key]; !ok || got != task {
		t.Fatalf("task state after failed persistence = %#v, found=%v; want %#v", got, ok, task)
	}
}

func TestTaskStoreSaveRollsBackMemoryOnPersistFailure(t *testing.T) {
	root := t.TempDir()
	store, err := OpenTaskStore(filepath.Join(root, "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	previous := ScheduledTask{Site: "demo", Name: "nightly", Runtime: "shell", Command: "true", State: "applied"}
	store.values["demo/nightly"] = previous
	store.path = root // A directory cannot be atomically replaced as state.
	if err := store.save("demo/nightly", ScheduledTask{Site: "demo", Name: "nightly", Runtime: "php", Command: "php artisan schedule:run", State: "pending"}); err == nil {
		t.Fatal("expected task state persistence failure")
	}
	if got := store.values["demo/nightly"]; got != previous {
		t.Fatalf("task state after failed save = %#v, want %#v", got, previous)
	}
}
