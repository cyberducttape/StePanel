package stepanel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/operations"
)

func TestBackupScheduleLifecycle(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web", "sites", "demo", "public")
	if err := os.MkdirAll(web, 0750); err != nil {
		t.Fatal(err)
	}
	store, err := openBackupSchedules(filepath.Join(root, "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Config: Config{WebRoot: filepath.Join(root, "web")}, Auth: Auth{}, Schedules: store}
	req := httptest.NewRequest(http.MethodPut, "/api/backup-schedules", strings.NewReader(`{"site":"demo","interval_minutes":60}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	a.backupSchedules(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"enabled":true`) {
		t.Fatalf("save: %d %s", res.Code, res.Body.String())
	}
	list := httptest.NewRecorder()
	a.backupSchedules(list, httptest.NewRequest(http.MethodGet, "/api/backup-schedules", nil))
	if !strings.Contains(list.Body.String(), `"keep_last":7`) {
		t.Fatalf("default retention missing from response: %s", list.Body.String())
	}
	if !strings.Contains(list.Body.String(), `"site":"demo"`) {
		t.Fatalf("list: %s", list.Body.String())
	}
}

func TestBackupScheduleUpdateRollsBackWhenPersistenceFails(t *testing.T) {
	root := t.TempDir()
	web := filepath.Join(root, "web", "sites", "demo", "public")
	if err := os.MkdirAll(web, 0750); err != nil {
		t.Fatal(err)
	}
	store := &backupSchedules{path: root, items: map[string]BackupSchedule{}}
	a := &App{Config: Config{WebRoot: filepath.Join(root, "web")}, Auth: Auth{}, Schedules: store}
	req := httptest.NewRequest(http.MethodPut, "/api/backup-schedules", strings.NewReader(`{"site":"demo","interval_minutes":60}`))
	res := httptest.NewRecorder()
	a.backupSchedules(res, req)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	if _, exists := store.items["demo"]; exists {
		t.Fatal("failed persistence left an uncommitted schedule in memory")
	}
}

func TestBackupScheduleDeleteRollsBackWhenPersistenceFails(t *testing.T) {
	root := t.TempDir()
	existing := BackupSchedule{Site: "demo", IntervalMinutes: 60, KeepLast: 7, Enabled: true, NextRun: time.Now().Add(time.Hour)}
	store := &backupSchedules{path: root, items: map[string]BackupSchedule{"demo": existing}}
	a := &App{Auth: Auth{}, Schedules: store}
	req := httptest.NewRequest(http.MethodDelete, "/api/backup-schedules?site=demo", nil)
	res := httptest.NewRecorder()
	a.backupSchedules(res, req)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	if _, exists := store.items["demo"]; !exists {
		t.Fatal("failed persistence removed the schedule from memory")
	}
}

func TestTerminationRemovesBackupScheduleAcrossRestartAndSiteReuse(t *testing.T) {
	root := t.TempDir()
	schedulePath := filepath.Join(root, "schedules.json")
	store, err := openBackupSchedules(schedulePath)
	if err != nil {
		t.Fatal(err)
	}
	store.items["demo"] = BackupSchedule{
		Site: "demo", IntervalMinutes: 60, KeepLast: 7, Enabled: true,
		NextRun: time.Now().UTC().Add(-time.Minute),
	}
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	terminator := &terminationHost{app: &App{Schedules: store}, site: "demo"}
	if err := terminator.RemoveTasks(context.Background()); err != nil {
		t.Fatalf("remove scheduled work during termination: %v", err)
	}

	// A process restart must not reload the deleted site's due schedule. If the
	// name is reused later, no backup is queued until an operator explicitly
	// creates a new schedule for the recreated site.
	restartedSchedules, err := openBackupSchedules(schedulePath)
	if err != nil {
		t.Fatalf("reload schedule state after termination: %v", err)
	}
	recreatedRoot := filepath.Join(root, "sites", "demo", "public")
	if err := os.MkdirAll(recreatedRoot, 0750); err != nil {
		t.Fatal(err)
	}
	jobs, err := OpenDurableJobs(filepath.Join(root, "control.db"), "")
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	if _, err := bindControlPlaneState(restartedSchedules, jobs.db, "backup-schedules", &restartedSchedules.items); err != nil {
		t.Fatal(err)
	}
	if err := restartedSchedules.persistLocked(); err != nil {
		t.Fatal(err)
	}
	restarted := &App{Schedules: restartedSchedules, Jobs: jobs}
	restarted.runDueBackups()
	if len(jobs.List(10)) != 0 {
		t.Fatal("stale schedule queued a backup after restart and site-name reuse")
	}
	if _, exists := restartedSchedules.items["demo"]; exists {
		t.Fatal("terminated site's schedule reappeared after restart")
	}

	newSchedule := BackupSchedule{Site: "demo", IntervalMinutes: 60, KeepLast: 7, Enabled: true, NextRun: time.Now().UTC().Add(-time.Minute)}
	if err := persistMapKeyChange(restartedSchedules.items, "demo", &newSchedule, restartedSchedules.persistLocked); err != nil {
		t.Fatal(err)
	}
	restarted.runDueBackups()
	queued := jobs.List(10)
	if len(queued) != 1 || queued[0].Kind != "site.backup" || queued[0].User != "demo" {
		t.Fatalf("explicitly recreated schedule did not enqueue exactly one site backup: %#v", queued)
	}
}

func TestDueBackupScheduleAndOutboxCommitBeforeJobAdmission(t *testing.T) {
	root := t.TempDir()
	db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schedules, err := openBackupSchedules(filepath.Join(root, "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	due := BackupSchedule{Site: "demo", IntervalMinutes: 60, KeepLast: 7, Enabled: true, NextRun: time.Now().UTC().Add(-time.Minute)}
	schedules.items[due.Site] = due
	if _, err := bindControlPlaneState(schedules, db, "backup-schedules", &schedules.items); err != nil {
		t.Fatal(err)
	}
	if err := schedules.persistLocked(); err != nil {
		t.Fatal(err)
	}
	jobs := newJobsWithDB(db, 1)
	app := &App{Schedules: schedules, Jobs: jobs}
	if _, err := db.Exec(`CREATE TRIGGER fail_scheduled_backup BEFORE INSERT ON jobs WHEN NEW.kind='site.backup' BEGIN SELECT RAISE(ABORT,'injected enqueue failure'); END`); err != nil {
		t.Fatal(err)
	}
	app.runDueBackups()
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_schedule_outbox WHERE delivered_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending outbox rows = %d, want 1", pending)
	}
	if len(jobs.List(10)) != 0 {
		t.Fatal("injected enqueue failure unexpectedly created a job")
	}
	if got := schedules.items[due.Site]; !got.NextRun.After(due.NextRun) || got.LastRun == nil {
		t.Fatalf("schedule did not atomically advance: %#v", got)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_scheduled_backup`); err != nil {
		t.Fatal(err)
	}
	app.runDueBackups()
	if got := jobs.List(10); len(got) != 1 || got[0].Kind != "site.backup" {
		t.Fatalf("outbox replay jobs = %#v", got)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_schedule_outbox`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("delivered outbox rows = %d, want drained", pending)
	}
	staleKey := "schedule:demo:termination-test"
	if _, err := db.Exec(`INSERT INTO backup_schedule_outbox(id, site, operation_key, payload, created_at) VALUES(?, 'demo', ?, '{}', ?)`, staleKey, staleKey, time.Now().UTC().UnixNano()); err != nil {
		t.Fatal(err)
	}
	if err := persistMapKeyChange(schedules.items, "demo", (*BackupSchedule)(nil), schedules.persistLocked); err != nil {
		t.Fatal(err)
	}
	if err := app.dispatchBackupScheduleOutbox(); err != nil {
		t.Fatal(err)
	}
	if got := jobs.List(10); len(got) != 1 {
		t.Fatalf("deleted site's stale outbox intent was dispatched: %#v", got)
	}
}

func TestScheduledBackupDispatchSkipsBusySitesWithoutBlockingOthers(t *testing.T) {
	root := t.TempDir()
	db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schedules, err := openBackupSchedules(filepath.Join(root, "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{"site-a", "site-b", "site-c"} {
		schedules.items[site] = BackupSchedule{Site: site, IntervalMinutes: 60, KeepLast: 7, Enabled: true, NextRun: time.Now().UTC().Add(time.Hour)}
	}
	if _, err := bindControlPlaneState(schedules, db, "backup-schedules", &schedules.items); err != nil {
		t.Fatal(err)
	}
	if err := schedules.persistLocked(); err != nil {
		t.Fatal(err)
	}
	panelLocks, err := operations.NewDBLocks(db, "panel", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	workerLocks, err := operations.NewDBLocks(db, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	jobs := newJobsWithDB(db, 1)
	app := &App{Schedules: schedules, Jobs: jobs, dbLocks: panelLocks}

	// site-a has a backlog older than every other intent. Its oldest intent
	// is blocked by a restore in another process, and site-b by one in this
	// process.
	base := time.Now().UTC().Add(-time.Hour).UnixNano()
	intents := []struct{ site, key string }{
		{"site-a", "schedule:site-a:1"}, {"site-a", "schedule:site-a:2"}, {"site-a", "schedule:site-a:3"},
		{"site-b", "schedule:site-b:1"}, {"site-c", "schedule:site-c:1"},
	}
	for i, intent := range intents {
		payload, err := json.Marshal(durableBackupRequest{Site: intent.site, Scheduled: true, KeepLast: 7, Actor: "scheduler"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO backup_schedule_outbox(id, site, operation_key, payload, created_at) VALUES(?, ?, ?, ?, ?)`, intent.key, intent.site, intent.key, payload, base+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	workerLease, err := workerLocks.Acquire(t.Context(), "site-a")
	if err != nil {
		t.Fatal(err)
	}
	releaseB, err := app.acquireSiteMutationLock(t.Context(), "site-b")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- app.dispatchBackupScheduleOutbox() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dispatch with busy sites = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dispatch waited on a busy site")
	}
	pendingSites := func() []string {
		t.Helper()
		rows, err := db.Query(`SELECT operation_key FROM backup_schedule_outbox WHERE delivered_at IS NULL ORDER BY created_at, id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var keys []string
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				t.Fatal(err)
			}
			keys = append(keys, key)
		}
		return keys
	}
	if got, want := pendingSites(), []string{"schedule:site-a:1", "schedule:site-a:2", "schedule:site-a:3", "schedule:site-b:1"}; !slices.Equal(got, want) {
		t.Fatalf("pending intents = %v, want %v", got, want)
	}
	if got := jobs.List(10); len(got) != 1 || got[0].User != "site-c" {
		t.Fatalf("jobs after busy dispatch = %#v, want only site-c", got)
	}

	releaseB()
	if err := workerLocks.Release(workerLease); err != nil {
		t.Fatal(err)
	}
	// One intent per site per pass: site-a's backlog drains in order.
	for pass := 0; pass < 3; pass++ {
		if err := app.dispatchBackupScheduleOutbox(); err != nil {
			t.Fatalf("dispatch pass %d: %v", pass, err)
		}
	}
	if got := pendingSites(); len(got) != 0 {
		t.Fatalf("intents still pending after locks were released: %v", got)
	}
	if got := jobs.List(10); len(got) != 5 {
		t.Fatalf("jobs after drain = %d, want 5", len(got))
	}
}

func TestScheduledBackupDispatchContinuesPastSiteLocalEnqueueFailure(t *testing.T) {
	root := t.TempDir()
	db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schedules, err := openBackupSchedules(filepath.Join(root, "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []string{"site-a", "site-b"} {
		schedules.items[site] = BackupSchedule{Site: site, IntervalMinutes: 60, KeepLast: 7, Enabled: true, NextRun: time.Now().UTC().Add(time.Hour)}
	}
	if _, err := bindControlPlaneState(schedules, db, "backup-schedules", &schedules.items); err != nil {
		t.Fatal(err)
	}
	if err := schedules.persistLocked(); err != nil {
		t.Fatal(err)
	}
	jobs := newJobsWithDB(db, 1)
	app := &App{Schedules: schedules, Jobs: jobs}
	base := time.Now().UTC().Add(-time.Hour).UnixNano()
	for i, site := range []string{"site-a", "site-b"} {
		key := "schedule:" + site + ":1"
		payload, err := json.Marshal(durableBackupRequest{Site: site, Scheduled: true, KeepLast: 7, Actor: "scheduler"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO backup_schedule_outbox(id, site, operation_key, payload, created_at) VALUES(?, ?, ?, ?, ?)`, key, site, key, payload, base+int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_site_a_backup BEFORE INSERT ON jobs WHEN NEW.kind='site.backup' AND NEW.owner='site-a' BEGIN SELECT RAISE(ABORT,'injected site-a enqueue failure'); END`); err != nil {
		t.Fatal(err)
	}
	err = app.dispatchBackupScheduleOutbox()
	if err == nil || !strings.Contains(err.Error(), "injected site-a enqueue failure") {
		t.Fatalf("dispatch error = %v, want site-a failure reported", err)
	}
	if got := jobs.List(10); len(got) != 1 || got[0].User != "site-b" {
		t.Fatalf("jobs = %#v, want site-b despite site-a failure", got)
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_schedule_outbox WHERE site='site-a' AND delivered_at IS NULL`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("site-a pending intents = %d, want 1 kept for retry", pending)
	}
}
