package stepanel

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnqueueRefusesNewJobsAtThePendingLimit(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 2)
	jobs.SetMaxPending(2)
	first, err := jobs.Enqueue("site.backup", "alpha", "", []byte(`{}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Enqueue("site.backup", "beta", "", []byte(`{}`), 1); err != nil {
		t.Fatal(err)
	}
	_, err = jobs.Enqueue("site.backup", "gamma", "", []byte(`{}`), 1)
	if !errors.Is(err, ErrJobQueueFull) {
		t.Fatalf("third pending job = %v, want ErrJobQueueFull", err)
	}

	response := httptest.NewRecorder()
	writeJobEnqueueError(response, err, "could not persist backup job")
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") == "" {
		t.Fatalf("queue-full response = %d (Retry-After %q), want 503 with Retry-After", response.Code, response.Header().Get("Retry-After"))
	}
	response = httptest.NewRecorder()
	writeJobEnqueueError(response, errors.New("disk I/O error"), "could not persist backup job")
	if response.Code != http.StatusInternalServerError || response.Body.String() != "could not persist backup job\n" {
		t.Fatalf("persistence failure response = %d %q", response.Code, response.Body.String())
	}

	// Finished jobs do not count against the limit.
	if _, err := db.Exec(`UPDATE jobs SET state = 'completed' WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Enqueue("site.backup", "gamma", "", []byte(`{}`), 1); err != nil {
		t.Fatalf("enqueue after a job finished = %v", err)
	}
}

func TestMaxPendingJobsConfigurationIsBounded(t *testing.T) {
	t.Setenv("STEPANEL_MAX_PENDING_JOBS", "")
	if cfg := LoadConfig(); cfg.MaxPendingJobs != defaultMaxPendingJobs {
		t.Fatalf("default MaxPendingJobs = %d", cfg.MaxPendingJobs)
	}
	t.Setenv("STEPANEL_MAX_PENDING_JOBS", "2500")
	if cfg := LoadConfig(); cfg.MaxPendingJobs != 2500 {
		t.Fatalf("configured MaxPendingJobs = %d", cfg.MaxPendingJobs)
	}
	t.Setenv("STEPANEL_MAX_PENDING_JOBS", "1000000")
	if cfg := LoadConfig(); cfg.MaxPendingJobs != defaultMaxPendingJobs {
		t.Fatalf("an unmeasured backlog limit was accepted: %d", cfg.MaxPendingJobs)
	}
}

// The pending limit is exact under concurrency: the panel and worker are
// separate processes that both enqueue, so two job stores on separate
// connections race many enqueues and exactly the limit is admitted.
func TestPendingLimitIsExactUnderConcurrentEnqueues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	const limit, callers = 25, 80
	var stores []*Jobs
	for i := 0; i < 2; i++ {
		db, err := openControlPlaneDB(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		store := newJobsWithDB(db, 2)
		store.SetMaxPending(limit)
		stores = append(stores, store)
	}
	var admitted, refused atomic.Int64
	var other sync.Map
	var wait sync.WaitGroup
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, err := stores[i%2].Enqueue("site.backup", fmt.Sprintf("site-%03d", i), "", []byte(`{}`), 1)
			switch {
			case err == nil:
				admitted.Add(1)
			case errors.Is(err, ErrJobQueueFull):
				refused.Add(1)
			default:
				other.Store(i, err)
			}
		}(i)
	}
	wait.Wait()
	other.Range(func(key, value any) bool {
		t.Errorf("enqueue %v failed: %v", key, value)
		return true
	})
	var pending int
	if err := stores[0].db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state IN ('queued','running')`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if admitted.Load() != limit || pending != limit || refused.Load() != callers-limit {
		t.Fatalf("admitted %d, refused %d, pending %d; want exactly %d admitted", admitted.Load(), refused.Load(), pending, limit)
	}
	// A refusal is not a storage failure.
	if err := stores[0].PersistenceError(); err != nil {
		t.Fatalf("queue-full refusals marked the store unhealthy: %v", err)
	}
}

// Dead letters older than the retention are deleted (and reported for
// audit); recent ones, other states, and requeued jobs are kept.
func TestExpireDeadLettersRespectsRetentionAndState(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 2)
	mark := func(owner, state string, age time.Duration) string {
		t.Helper()
		item, err := jobs.Enqueue("site.backup", owner, "", []byte(`{}`), 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE jobs SET state = ?, finished_at = ? WHERE id = ?`, state, time.Now().Add(-age).UnixNano(), item.ID); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	old := mark("old", "dead-letter", 100*24*time.Hour)
	recent := mark("recent", "dead-letter", 10*24*time.Hour)
	completed := mark("completed", "completed", 100*24*time.Hour)
	requeued := mark("requeued", "dead-letter", 100*24*time.Hour)
	if _, err := db.Exec(`UPDATE jobs SET state = 'queued', finished_at = NULL WHERE id = ?`, requeued); err != nil {
		t.Fatal(err)
	}
	expired, err := jobs.ExpireDeadLetters(90*24*time.Hour, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(expired) != 1 || expired[0].ID != old || expired[0].Owner != "old" || expired[0].Kind != "site.backup" {
		t.Fatalf("expired = %+v, want only %s", expired, old)
	}
	for _, id := range []string{recent, completed, requeued} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id = ?`, id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("job %s removed (%d, %v)", id, count, err)
		}
	}
	if again, err := jobs.ExpireDeadLetters(90*24*time.Hour, 500); err != nil || len(again) != 0 {
		t.Fatalf("second expiry = %+v, %v", again, err)
	}
}
