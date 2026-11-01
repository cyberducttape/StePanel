package stepanel

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
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
