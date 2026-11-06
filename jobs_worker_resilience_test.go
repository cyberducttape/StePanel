package stepanel

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// A database error inside the worker loop (here, another process holding
// the write lock while the worker has no busy wait) must not stop the
// worker: before, ClaimNext's error ended RunWorker, the standalone worker
// exited fatally, and the panel's embedded worker stopped for good. The
// worker now backs off, claims the job once the lock is released, and clears
// the readiness error it reported.
func TestWorkerSurvivesTransientClaimFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-plane.db")
	db, err := openControlPlaneDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	item, err := jobs.Enqueue("site.backup", "shop", "", []byte(`{}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	// The pool holds one connection; make its lock contention immediate.
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

	handled := make(chan string, 1)
	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() {
		stopped <- jobs.RunWorker(runCtx, "worker-test", []string{"site.backup"}, 20*time.Millisecond, func(_ context.Context, job Job) ([]byte, error) {
			handled <- job.ID
			return []byte(`{}`), nil
		})
	}()

	// While the lock is held the worker keeps failing to claim, and keeps
	// running.
	time.Sleep(400 * time.Millisecond)
	select {
	case err := <-stopped:
		t.Fatalf("worker stopped on a transient claim failure: %v", err)
	default:
	}
	if jobs.PersistenceError() == nil {
		t.Fatal("repeated claim failures were not reported for readiness")
	}
	if _, err := holder.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatal(err)
	}

	select {
	case id := <-handled:
		if id != item.ID {
			t.Fatalf("worker ran %s, want %s", id, item.ID)
		}
	case err := <-stopped:
		t.Fatalf("worker stopped: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("worker did not claim the job after the lock was released")
	}
	deadline := time.Now().Add(5 * time.Second)
	for jobs.PersistenceError() != nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if err := jobs.PersistenceError(); err != nil {
		t.Fatalf("readiness error not cleared after the worker recovered: %v", err)
	}
	cancel()
	if err := <-stopped; err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		t.Fatalf("worker stopped with %v", err)
	}
}
