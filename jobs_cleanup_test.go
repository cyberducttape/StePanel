package stepanel

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/state"
)

// openCleanupTestJobs returns durable jobs holding one expired and one fresh
// completed job, both committed to SQLite.
func openCleanupTestJobs(t *testing.T) (*Jobs, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control-plane.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	jobs := newJobsWithDB(db, 1)
	old := time.Now().UTC().Add(-48 * time.Hour)
	fresh := time.Now().UTC()
	jobs.mu.Lock()
	jobs.items["expired"] = &Job{ID: "expired", Kind: "import", User: "owner-a", State: "completed", StartedAt: old, FinishedAt: &old}
	jobs.items["fresh"] = &Job{ID: "fresh", Kind: "import", User: "owner-b", State: "completed", StartedAt: fresh, FinishedAt: &fresh}
	err = jobs.persistLocked()
	jobs.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return jobs, db, path
}

func durableJobIDs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func reloadedJobIDs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	reloaded := newJobsWithDB(db, 1)
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	return memoryJobIDs(reloaded)
}

// memoryJobIDs inspects the in-memory view directly; Get would fall through
// to SQLite and repopulate memory, masking the invariant under test.
func memoryJobIDs(jobs *Jobs) []string {
	jobs.mu.RLock()
	defer jobs.mu.RUnlock()
	ids := make([]string, 0, len(jobs.items))
	for id := range jobs.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func TestJobsCleanupRemovesExpiredJobsDurably(t *testing.T) {
	jobs, db, _ := openCleanupTestJobs(t)
	jobs.Cleanup(24 * time.Hour)
	if err := jobs.PersistenceError(); err != nil {
		t.Fatalf("cleanup persistence error: %v", err)
	}
	if ids := memoryJobIDs(jobs); strings.Join(ids, ",") != "fresh" {
		t.Fatalf("in-memory jobs = %v, want [fresh]", ids)
	}
	if ids := durableJobIDs(t, db); strings.Join(ids, ",") != "fresh" {
		t.Fatalf("durable jobs = %v, want [fresh]", ids)
	}
	// A restart after the commit must not resurrect the removed job.
	if ids := reloadedJobIDs(t, db); strings.Join(ids, ",") != "fresh" {
		t.Fatalf("reloaded jobs = %v, want [fresh]", ids)
	}
}

func TestJobsCleanupUsesDurableRowsBeyondLocalCacheAndBatches(t *testing.T) {
	jobs, db, _ := openCleanupTestJobs(t)
	worker := newJobsWithDB(db, 1) // Simulates the separately installed worker process.
	old := time.Now().UTC().Add(-72 * time.Hour)
	worker.mu.Lock()
	for i := 0; i < 501; i++ {
		id := fmt.Sprintf("external-%03d", i)
		worker.items[id] = &Job{ID: id, Kind: "backup", User: "site", State: "completed", StartedAt: old, FinishedAt: &old}
	}
	if err := worker.persistLocked(); err != nil {
		worker.mu.Unlock()
		t.Fatal(err)
	}
	worker.mu.Unlock()

	// This panel instance has not loaded the worker's newly completed rows.
	if _, exists := jobs.items["external-500"]; exists {
		t.Fatal("test setup unexpectedly populated the panel cache")
	}
	jobs.Cleanup(24 * time.Hour)
	if got := len(durableJobIDs(t, db)); got != 3 {
		t.Fatalf("first bounded cleanup left %d rows, want fresh plus two expired batch remainder", got)
	}
	if _, cached := jobs.items["external-000"]; cached {
		t.Fatal("cleanup failed to invalidate deleted worker job from panel cache")
	}
	jobs.Cleanup(24 * time.Hour)
	if ids := durableJobIDs(t, db); strings.Join(ids, ",") != "fresh" {
		t.Fatalf("second cleanup rows = %v, want [fresh]", ids)
	}
}

func TestDurableJobStartupCacheBoundsTerminalHistory(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seed := newJobsWithDB(db, 1)
	old := time.Now().UTC().Add(-365 * 24 * time.Hour)
	seed.mu.Lock()
	for i := 0; i < durableTerminalJobCacheLimit+20; i++ {
		id := fmt.Sprintf("history-%04d", i)
		seed.items[id] = &Job{ID: id, Kind: "backup", User: "site", State: "completed", StartedAt: old, FinishedAt: &old}
	}
	seed.items["active"] = &Job{ID: "active", Kind: "restore", User: "site", State: "queued", StartedAt: time.Now().UTC()}
	seed.items["review"] = &Job{ID: "review", Kind: "restore", User: "site", State: "dead-letter", StartedAt: old, FinishedAt: &old}
	if err := seed.persistLocked(); err != nil {
		seed.mu.Unlock()
		t.Fatal(err)
	}
	seed.mu.Unlock()

	reloaded := newJobsWithDB(db, 1)
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if got, want := len(reloaded.items), durableTerminalJobCacheLimit+2; got != want {
		t.Fatalf("startup cache has %d jobs, want %d (bounded terminal history plus active and dead-letter)", got, want)
	}
	if reloaded.items["active"] == nil || reloaded.items["review"] == nil {
		t.Fatal("startup cache omitted recovery-critical active/dead-letter jobs")
	}
}

func TestJobsCleanupRetainsExpiredDeadLettersAcrossRestart(t *testing.T) {
	j, db, _ := openCleanupTestJobs(t)
	old := time.Now().UTC().Add(-48 * time.Hour)
	j.mu.Lock()
	j.items["dead-letter"] = &Job{ID: "dead-letter", Kind: "restore", User: "owner-a", State: "dead-letter", StartedAt: old, FinishedAt: &old, Error: "operator review required"}
	if err := j.persistLocked(); err != nil {
		j.mu.Unlock()
		t.Fatal(err)
	}
	j.mu.Unlock()

	j.Cleanup(24 * time.Hour)
	if ids := memoryJobIDs(j); strings.Join(ids, ",") != "dead-letter,fresh" {
		t.Fatalf("in-memory jobs = %v, want [dead-letter fresh]", ids)
	}
	if ids := durableJobIDs(t, db); strings.Join(ids, ",") != "dead-letter,fresh" {
		t.Fatalf("durable jobs = %v, want [dead-letter fresh]", ids)
	}
	if ids := reloadedJobIDs(t, db); strings.Join(ids, ",") != "dead-letter,fresh" {
		t.Fatalf("reloaded jobs = %v, want [dead-letter fresh]", ids)
	}
}

// assertCleanupFailureKeepsJob checks the invariant every injected failure
// must preserve: memory and SQLite still agree that the expired job exists,
// the failure is surfaced, and a later Cleanup can finish the removal.
func assertCleanupFailureKeepsJob(t *testing.T, jobs *Jobs, db *sql.DB, wantErr string) {
	t.Helper()
	jobs.Cleanup(24 * time.Hour)
	err := jobs.PersistenceError()
	if err == nil || !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("persistence error = %v, want %q", err, wantErr)
	}
	if ids := memoryJobIDs(jobs); strings.Join(ids, ",") != "expired,fresh" {
		t.Fatalf("in-memory jobs after failed cleanup = %v, want [expired fresh]", ids)
	}
	if db != nil {
		if ids := durableJobIDs(t, db); strings.Join(ids, ",") != "expired,fresh" {
			t.Fatalf("durable jobs after failed cleanup = %v, want [expired fresh]", ids)
		}
	}
}

func TestJobsCleanupBeginFailureKeepsJobsConsistent(t *testing.T) {
	jobs, db, path := openCleanupTestJobs(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertCleanupFailureKeepsJob(t, jobs, nil, "begin durable job cleanup")
	reopened, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if ids := reloadedJobIDs(t, reopened); strings.Join(ids, ",") != "expired,fresh" {
		t.Fatalf("reloaded jobs = %v, want [expired fresh]", ids)
	}
}

func TestJobsCleanupDeleteFailureKeepsJobsAndRetries(t *testing.T) {
	jobs, db, _ := openCleanupTestJobs(t)
	// Stands in for an I/O error, full disk, or any statement-level failure.
	if _, err := db.Exec(`CREATE TRIGGER inject_job_delete_failure BEFORE DELETE ON jobs BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	assertCleanupFailureKeepsJob(t, jobs, db, "injected delete failure")

	if _, err := db.Exec(`DROP TRIGGER inject_job_delete_failure`); err != nil {
		t.Fatal(err)
	}
	jobs.Cleanup(24 * time.Hour)
	if err := jobs.PersistenceError(); err != nil {
		t.Fatalf("retry persistence error: %v", err)
	}
	if ids := durableJobIDs(t, db); strings.Join(ids, ",") != "fresh" {
		t.Fatalf("durable jobs after retry = %v, want [fresh]", ids)
	}
	if ids := memoryJobIDs(jobs); strings.Join(ids, ",") != "fresh" {
		t.Fatalf("in-memory jobs after retry = %v, want [fresh]", ids)
	}
}

func TestJobsCleanupCommitFailureKeepsJobsConsistent(t *testing.T) {
	jobs, db, _ := openCleanupTestJobs(t)
	// A deferred foreign key is checked only at COMMIT, so the DELETE succeeds
	// inside the transaction and the commit itself fails.
	if _, err := db.Exec(`CREATE TABLE inject_commit_failure (job_id TEXT REFERENCES jobs(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO inject_commit_failure (job_id) VALUES ('expired')`); err != nil {
		t.Fatal(err)
	}
	assertCleanupFailureKeepsJob(t, jobs, db, "commit durable job cleanup")
	if ids := reloadedJobIDs(t, db); strings.Join(ids, ",") != "expired,fresh" {
		t.Fatalf("reloaded jobs = %v, want [expired fresh]", ids)
	}
}

func TestJobsCleanupBusyDatabaseKeepsJobsConsistent(t *testing.T) {
	jobs, db, path := openCleanupTestJobs(t)
	// The control-plane pool holds a single connection, so this pragma applies
	// to the connection Cleanup uses and turns lock contention into SQLITE_BUSY
	// immediately instead of after the production five-second wait.
	if _, err := db.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx := context.Background()
	holder, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	assertCleanupFailureKeepsJob(t, jobs, nil, "delete completed durable job expired")
	if _, err := holder.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if ids := durableJobIDs(t, db); strings.Join(ids, ",") != "expired,fresh" {
		t.Fatalf("durable jobs after busy cleanup = %v, want [expired fresh]", ids)
	}
	jobs.Cleanup(24 * time.Hour)
	if err := jobs.PersistenceError(); err != nil {
		t.Fatalf("retry persistence error: %v", err)
	}
	if ids := durableJobIDs(t, db); strings.Join(ids, ",") != "fresh" {
		t.Fatalf("durable jobs after retry = %v, want [fresh]", ids)
	}
}

func TestJobsCleanupFileStoreFailureKeepsJobs(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state", "jobs.json")
	jobs := newJobs(statePath, 1)
	old := time.Now().UTC().Add(-48 * time.Hour)
	jobs.mu.Lock()
	jobs.items["expired"] = &Job{ID: "expired", Kind: "import", User: "owner-a", State: "completed", StartedAt: old, FinishedAt: &old}
	jobs.mu.Unlock()
	// A regular file where the state directory belongs makes the atomic
	// rewrite fail before anything is replaced.
	if err := os.WriteFile(filepath.Join(root, "state"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	jobs.Cleanup(24 * time.Hour)
	if jobs.PersistenceError() == nil {
		t.Fatal("file store cleanup failure was not reported")
	}
	if ids := memoryJobIDs(jobs); strings.Join(ids, ",") != "expired" {
		t.Fatalf("in-memory jobs = %v, want [expired]", ids)
	}
}

func TestJobsCleanupReportsCategorizedPersistenceFailures(t *testing.T) {
	jobs, db, path := openCleanupTestJobs(t)
	var seen []state.StateError
	jobs.SetStateErrorObserver(func(err state.StateError) { seen = append(seen, err) })
	if _, err := db.Exec(`CREATE TRIGGER inject_job_delete_failure BEFORE DELETE ON jobs BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	jobs.Cleanup(24 * time.Hour)
	if len(seen) != 1 || seen[0].Category != state.Persistence || seen[0].Operation != "persist_job_cleanup" {
		t.Fatalf("observed %+v, want one persistence error", seen)
	}
	if _, err := db.Exec(`DROP TRIGGER inject_job_delete_failure`); err != nil {
		t.Fatal(err)
	}

	// Lock contention is retryable and must not page as a persistence failure.
	if _, err := db.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	holder, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	seen = nil
	jobs.Cleanup(24 * time.Hour)
	_, _ = holder.ExecContext(context.Background(), `ROLLBACK`)
	if len(seen) != 1 || seen[0].Category != state.Temporary {
		t.Fatalf("observed %+v, want one temporary error", seen)
	}
}
