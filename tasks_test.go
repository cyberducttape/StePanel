package main

import (
	"context"
	"os"
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

func TestNormalizeScheduledTaskSafeguards(t *testing.T) {
	task := ScheduledTask{}
	if err := normalizeScheduledTask(&task); err != nil {
		t.Fatal(err)
	}
	if task.MinIntervalSeconds != 60 || task.MaxConcurrentRuns != 1 || task.MissedRunPolicy != "run_once" || task.CPUPercent != 100 || task.MemoryMB != 1024 || task.TasksMax != 256 {
		t.Fatalf("safeguard defaults = %+v", task)
	}
	for _, invalid := range []ScheduledTask{
		{MaxConcurrentRuns: 2},
		{MinIntervalSeconds: 30},
		{MissedRunPolicy: "replay-all"},
		{NotifyWebhook: "http://localhost/hook"},
		{CPUPercent: 10},
	} {
		if err := normalizeScheduledTask(&invalid); err == nil {
			t.Errorf("unsafe task safeguards accepted: %+v", invalid)
		}
	}
}

func TestTaskCalendarMinimumInterval(t *testing.T) {
	calendar := "*-*-* *:00:00"
	if err := validateTaskCalendarInterval(context.Background(), calendar, 3600); err != nil {
		t.Fatalf("hourly schedule rejected at 60-minute minimum: %v", err)
	}
	if err := validateTaskCalendarInterval(context.Background(), calendar, 3601); err == nil {
		t.Fatal("hourly schedule accepted with a longer than hourly minimum")
	}
	if err := validateTaskCalendarInterval(context.Background(), "*-*-* *:00/1:00", 61); err == nil {
		t.Fatal("minute schedule accepted at one-minute minimum")
	}
}

func TestTaskStoreMigratesSafeguardDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.json")
	legacy := `{"demo/nightly":{"site":"demo","name":"nightly","runtime":"shell","command":"true","on_calendar":"*-*-* *:00:00","timeout_sec":300,"enabled":true,"state":"applied"}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenTaskStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.normalizeSafeguards(); err != nil {
		t.Fatal(err)
	}
	got := store.values["demo/nightly"]
	if got.MinIntervalSeconds != 60 || got.MaxConcurrentRuns != 1 || got.State != "pending" {
		t.Fatalf("upgraded task = %+v", got)
	}
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenTaskStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.values["demo/nightly"]; got.MissedRunPolicy != "run_once" || got.CPUPercent != 100 {
		t.Fatalf("persisted safeguards = %+v", got)
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
	if got, ok := store.values[key]; !ok || !tasksEqual(got, task) {
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
	if got := store.values["demo/nightly"]; !tasksEqual(got, previous) {
		t.Fatalf("task state after failed save = %#v, want %#v", got, previous)
	}
}

// tasksEqual compares two ScheduledTask structs accounting for slice comparisons
func tasksEqual(a, b ScheduledTask) bool {
	return a.Site == b.Site &&
		a.Name == b.Name &&
		a.Runtime == b.Runtime &&
		a.Command == b.Command &&
		a.OnCalendar == b.OnCalendar &&
		a.TimeoutSec == b.TimeoutSec &&
		a.Enabled == b.Enabled &&
		a.MinIntervalSeconds == b.MinIntervalSeconds &&
		a.MaxConcurrentRuns == b.MaxConcurrentRuns &&
		a.MissedRunPolicy == b.MissedRunPolicy &&
		a.CPUPercent == b.CPUPercent &&
		a.MemoryMB == b.MemoryMB &&
		a.TasksMax == b.TasksMax &&
		a.NotifyWebhook == b.NotifyWebhook &&
		a.State == b.State &&
		a.LastError == b.LastError &&
		a.Deleted == b.Deleted
}
