package stepanel

import (
	"encoding/json"
	"reflect"
	"testing"
)

func scheduledTaskTestValue(site, name string) ScheduledTask {
	return ScheduledTask{Site: site, Name: name, Runtime: "php", Command: "php artisan schedule:run", OnCalendar: "*:0/5", TimeoutSec: 300, Enabled: true, MinIntervalSeconds: 60, MaxConcurrentRuns: 1, MissedRunPolicy: "skip", CPUPercent: 50, MemoryMB: 256, TasksMax: 64, NotifyWebhook: "https://hooks.example.org/x", State: "applied", LastRunAt: 1764150000, LastRunExitCode: 1, ConsecutiveFailures: 2, CurrentRunCount: 0,
		LastRunOutput: []string{"starting", "failed: timeout"},
		Executions:    []TaskExecution{{StartedAt: 1764149700, DurationSec: 3, Result: "success", ExitCode: "0", ExitStatus: "exited"}, {StartedAt: 1764150000, DurationSec: 300, Result: "timeout", ExitCode: "", ExitStatus: "killed"}}}
}

func TestScheduledTasksMigrateFromTheStateBlobWithHistory(t *testing.T) {
	db := openTestControlPlane(t)
	legacy := map[string]ScheduledTask{"shop/cron": scheduledTaskTestValue("shop", "cron"), "blog/backup": {Site: "blog", Name: "backup", Enabled: false, Deleted: true}}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('tasks', ?, unixepoch(), 1)`, payload); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateScheduledTasksBlob(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &TaskStore{values: map[string]ScheduledTask{}}
	if err := bindScheduledTasks(store, db); err != nil {
		t.Fatal(err)
	}
	if got := store.values["shop/cron"]; !scheduledTaskEqual(got, legacy["shop/cron"]) {
		t.Fatalf("migrated task = %#v", got)
	}
	if got := store.values["blog/backup"]; !got.Deleted || got.LastRunOutput != nil || got.Executions != nil {
		t.Fatalf("migrated task without history = %#v", got)
	}
	var blobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM state_blobs WHERE name = 'tasks'`).Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("tasks blob after migration: %d, %v", blobs, err)
	}
}

func TestScheduledTaskHistoryIsReplacedAndCascades(t *testing.T) {
	db := openTestControlPlane(t)
	store := &TaskStore{values: map[string]ScheduledTask{}}
	if err := bindScheduledTasks(store, db); err != nil {
		t.Fatal(err)
	}
	store.values["shop/cron"] = scheduledTaskTestValue("shop", "cron")
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	// A run appends history in place; the change must still be detected.
	task := store.values["shop/cron"]
	task.Executions[1].Result = "success"
	task.LastRunOutput = task.LastRunOutput[:1]
	store.values["shop/cron"] = task
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	reopened := &TaskStore{values: map[string]ScheduledTask{}}
	if err := bindScheduledTasks(reopened, db); err != nil {
		t.Fatal(err)
	}
	if got := reopened.values["shop/cron"]; got.Executions[1].Result != "success" || len(got.LastRunOutput) != 1 {
		t.Fatalf("history after an in-place change = %#v", got)
	}
	delete(store.values, "shop/cron")
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"scheduled_task_output", "scheduled_task_executions"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s rows outlived their task: %d, %v", table, n, err)
		}
	}
}

// A field added to a stored struct without a column would be silently
// dropped on save. Each table's column list must cover every field.
func TestRelationalStoresCoverEveryField(t *testing.T) {
	scalarFields := func(value any) int {
		typ := reflect.TypeOf(value)
		count := 0
		for i := 0; i < typ.NumField(); i++ {
			if typ.Field(i).Type.Kind() != reflect.Slice {
				count++
			}
		}
		return count
	}
	cases := []struct {
		name    string
		value   any
		columns int
	}{
		// Site is the key column, the rest are resourceProfileRow fields.
		{"ResourceProfile", ResourceProfile{}, 1 + reflect.TypeOf(resourceProfileRow{}).NumField()},
		// Domain is the key column.
		{"DomainClaim", DomainClaim{}, 1 + len(domainClaimArgs(DomainClaim{}))},
		// Name is the key column.
		{"RouteDesired", RouteDesired{}, 1 + len(routeArgs(RouteDesired{}))},
		// Keys live in site_access_keys.
		{"SiteAccess", SiteAccess{}, 5},
		{"ScheduledTask", ScheduledTask{}, len(scheduledTaskArgs(ScheduledTask{}))},
	}
	for _, tc := range cases {
		if got := scalarFields(tc.value); got != tc.columns {
			t.Errorf("%s has %d scalar fields but its table stores %d; add a column (and a migration) for the new field", tc.name, got, tc.columns)
		}
	}
	if n := reflect.TypeOf(SSHKey{}).NumField(); n != 3 {
		t.Errorf("SSHKey has %d fields but site_access_keys stores 3", n)
	}
	if n := reflect.TypeOf(TaskExecution{}).NumField(); n != 5 {
		t.Errorf("TaskExecution has %d fields but scheduled_task_executions stores 5", n)
	}
}
