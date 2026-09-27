package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobsPersistCompletedWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Submit("restore-1", "site", func() (ImportResult, error) {
		return ImportResult{User: "site", FilesRestored: true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := jobs.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	job, ok := reopened.Get("restore-1")
	if !ok || job.State != "completed" || job.Result == nil || !job.Result.FilesRestored {
		t.Fatalf("persisted job = %#v, found = %v", job, ok)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("job state mode = %o, want 600", info.Mode().Perm())
	}
}

func TestDurableJobsPersistAndReconcileRunningWork(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "control-plane.db")
	jobs, err := OpenDurableJobs(databasePath, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.Submit("durable-1", "site", func() (ImportResult, error) {
		return ImportResult{User: "site", FilesRestored: true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := jobs.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDurableJobs(databasePath, "")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	job, ok := reopened.Get("durable-1")
	if !ok || job.State != "completed" || job.Result == nil || !job.Result.FilesRestored {
		t.Fatalf("durable job = %#v, found = %v", job, ok)
	}
}

func TestDurableJobLeaseLifecycle(t *testing.T) {
	dir := t.TempDir()
	db, err := openControlPlaneDB(filepath.Join(dir, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	j := newJobsWithDB(db, 1)
	if err := j.IntegrityCheck(); err != nil {
		t.Fatalf("control-plane integrity check failed: %v", err)
	}
	item := &Job{ID: "job-lease", Kind: "test", State: "queued", User: "tenant", StartedAt: time.Now().UTC()}
	if err := j.add(item); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := j.Claim(item.ID, "worker-a")
	if err != nil || !ok || claimed.State != "running" {
		t.Fatalf("claim = %+v, %v, %v", claimed, ok, err)
	}
	if renewed, err := j.Renew(item.ID, "worker-a"); err != nil || !renewed {
		t.Fatalf("renew = %v, %v", renewed, err)
	}
	if err := j.finishClaim(item.ID, "worker-a", func(job *Job) {
		job.State = "completed"
		now := time.Now().UTC()
		job.FinishedAt = &now
	}); err != nil {
		t.Fatal(err)
	}
	if got, ok := j.Get(item.ID); !ok || got.State != "completed" {
		t.Fatalf("completed job = %+v, %v", got, ok)
	}
}

func TestDurableQueueClaimRetryAndDeadLetter(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	j := newJobsWithDB(db, 1)
	queued, err := j.Enqueue("test.operation", "site", "op-1", []byte(`{"site":"site"}`), 2)
	if err != nil {
		t.Fatal(err)
	}
	second, err := j.Enqueue("test.operation", "site", "op-2", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_ = second
	j2 := newJobsWithDB(db, 1)
	claimed, ok, err := j2.ClaimNext("worker-a", "test.operation")
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("claim next = %#v, %v, %v", claimed, ok, err)
	}
	if err := j2.UpdateClaim(claimed.ID, "worker-a", 45); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := j2.ClaimNext("worker-b", "test.operation"); err != nil || ok {
		t.Fatalf("same-owner job was claimed concurrently: ok=%v err=%v", ok, err)
	}
	if err := j2.FailClaim(claimed.ID, "worker-a", "temporary failure"); err != nil {
		t.Fatal(err)
	}
	retried, ok, err := j2.ClaimNext("worker-b", "test.operation")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		// The first retry is intentionally delayed by backoff.
		t.Fatalf("retry became claimable before backoff: %#v", retried)
	}
	if _, err := db.Exec(`UPDATE jobs SET next_attempt_at = NULL WHERE id = ?`, queued.ID); err != nil {
		t.Fatal(err)
	}
	retried, ok, err = j2.ClaimNext("worker-b", "test.operation")
	if err != nil || !ok || retried.Attempts != 1 {
		t.Fatalf("retry claim = %#v, %v, %v", retried, ok, err)
	}
	if err := j2.FailClaim(retried.ID, "worker-b", "permanent failure"); err != nil {
		t.Fatal(err)
	}
	if got, ok := j2.Get(queued.ID); !ok || got.State != "dead-letter" || got.Attempts != 2 {
		t.Fatalf("dead-letter job = %#v, %v", got, ok)
	}
	secondClaim, ok, err := j2.ClaimNext("worker-c", "test.operation")
	if err != nil || !ok {
		t.Fatalf("second job was lost during another worker update: ok=%v err=%v", ok, err)
	}
	if err := j2.finishClaim(secondClaim.ID, "worker-c", func(job *Job) { job.State = "completed" }); err != nil {
		t.Fatal(err)
	}
	if err := j2.RequeueDeadLetter(queued.ID); err != nil {
		t.Fatal(err)
	}
	requeued, ok, err := newJobsWithDB(db, 1).ClaimNext("worker-d", "test.operation")
	if err != nil || !ok || requeued.ID != queued.ID || requeued.Attempts != 0 {
		t.Fatalf("requeued dead-letter = %#v, %v, %v", requeued, ok, err)
	}
}

func TestDurableWorkerReclaimsExpiredClaimOnStartup(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	producer := newJobsWithDB(db, 1)
	queued, err := producer.Enqueue("worker.operation", "site", "", []byte(`{"operation":"recover"}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := producer.ClaimNext("dead-worker", "worker.operation"); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	if _, err := db.Exec(`UPDATE jobs SET lease_expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second).UnixNano(), queued.ID); err != nil {
		t.Fatal(err)
	}

	worker := newJobsWithDB(db, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- worker.RunWorker(ctx, "replacement-worker", []string{"worker.operation"}, time.Millisecond, func(_ context.Context, item Job) ([]byte, error) {
			if item.ID != queued.ID {
				t.Errorf("reclaimed job = %s, want %s", item.ID, queued.ID)
			}
			cancel()
			return []byte(`{"recovered":true}`), nil
		})
	}()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exit = %v", err)
	}
	if got, ok := worker.Get(queued.ID); !ok || got.State != "completed" {
		t.Fatalf("recovered job = %#v, %v", got, ok)
	}
}

func TestDurableCancellationCrossProcessIsAuthoritative(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	producer := newJobsWithDB(db, 1)
	queued, err := producer.Enqueue("cancel.operation", "site", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	worker := newJobsWithDB(db, 1)
	claimed, ok, err := worker.ClaimNext("worker", "cancel.operation")
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("claim = %#v, %v, %v", claimed, ok, err)
	}
	if err := producer.RequestCancel(queued.ID); err != nil {
		t.Fatal(err)
	}
	if got, ok := producer.Get(queued.ID); !ok || got.State != "running" || !got.Cancel {
		t.Fatalf("cross-process job view = %#v, %v", got, ok)
	}
	listed := producer.List(10)
	if len(listed) != 1 || listed[0].ID != queued.ID || listed[0].State != "running" || !listed[0].Cancel {
		t.Fatalf("cross-process job list = %#v", listed)
	}
	if !worker.CancellationRequested(queued.ID) {
		t.Fatal("worker did not observe cancellation persisted by another process")
	}
}

func TestDurableListReleasesSQLiteRowsBeforeRefreshingJobs(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	jobs := newJobsWithDB(db, 1)
	queued, err := jobs.Enqueue("list.operation", "site", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	listed := jobs.List(10)
	if len(listed) != 1 || listed[0].ID != queued.ID {
		t.Fatalf("durable list = %#v, want job %q", listed, queued.ID)
	}
	if _, ok := jobs.Get(queued.ID); !ok {
		t.Fatal("job could not be refreshed after durable list")
	}
}

func TestDurableLoaderUsesRelationalStateAfterLeaseTransitions(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	producer := newJobsWithDB(db, 1)
	queued, err := producer.Enqueue("state.operation", "site", "", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	worker := newJobsWithDB(db, 1)
	claimed, ok, err := worker.ClaimNext("worker-a", "state.operation")
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("claim = %#v, %v, %v", claimed, ok, err)
	}
	reloaded := newJobsWithDB(db, 1)
	if err := reloaded.load(); err != nil {
		t.Fatal(err)
	}
	if got, ok := reloaded.Get(queued.ID); !ok || got.State != "running" {
		t.Fatalf("reloaded claimed job = %#v, %v", got, ok)
	}
	if _, err := db.Exec(`UPDATE jobs SET lease_expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute).UnixNano(), queued.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RequeueExpired(); err != nil {
		t.Fatal(err)
	}
	restarted := newJobsWithDB(db, 1)
	if err := restarted.load(); err != nil {
		t.Fatal(err)
	}
	if got, ok := restarted.Get(queued.ID); !ok || got.State != "queued" || got.LeaseOwner != "" {
		t.Fatalf("reloaded requeued job = %#v, %v", got, ok)
	}
}

func TestDurableWorkerRunsAndCompletesClaimedJob(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	j := newJobsWithDB(db, 1)
	queued, err := j.Enqueue("worker.operation", "site", "", []byte(`{"operation":"safe"}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- j.RunWorker(ctx, "worker-a", []string{"worker.operation"}, time.Millisecond, func(_ context.Context, item Job) ([]byte, error) {
			if item.ID != queued.ID || string(item.Payload) != `{"operation":"safe"}` {
				t.Errorf("handler received %#v", item)
			}
			return []byte(`{"result":"ok"}`), nil
		})
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if item, ok := j.Get(queued.ID); ok && item.State == "completed" {
			if string(item.Output) != `{"result":"ok"}` {
				t.Fatalf("worker output = %s", item.Output)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("worker exit = %v", err)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("worker did not complete durable job")
}

func TestDurableJobPayloadIsEncryptedAtRest(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	j, err := openDurableJobsDBWithKey(db, "", "test-job-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte(`{"db_password":"never-in-plaintext"}`)
	item, err := j.Enqueue("wordpress.restore", "site", "", secret, 1)
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := db.QueryRow(`SELECT payload FROM jobs WHERE id = ?`, item.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "never-in-plaintext") {
		t.Fatal("durable job payload contains plaintext secret")
	}
	reloaded, err := openDurableJobsDBWithKey(db, "", "test-job-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reloaded.Get(item.ID)
	if !ok || string(got.Payload) != string(secret) {
		t.Fatalf("decrypted payload = %#v found=%v", got.Payload, ok)
	}
	public, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "db_password") || strings.Contains(string(public), "never-in-plaintext") {
		t.Fatalf("job API representation leaked payload: %s", public)
	}
}

func TestJobsIdempotentRestoreReturnsExistingJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	work := func() (ImportResult, error) {
		calls.Add(1)
		close(started)
		<-release
		return ImportResult{User: "site"}, nil
	}
	first, existing, err := jobs.SubmitIdempotent("restore-1", "site", "deploy-123", work)
	if err != nil || existing || first != "restore-1" {
		t.Fatalf("first submit = id %q existing %v err %v", first, existing, err)
	}
	<-started
	second, existing, err := jobs.SubmitIdempotent("restore-2", "site", "deploy-123", work)
	if err != nil || !existing || second != first {
		t.Fatalf("retry submit = id %q existing %v err %v; want original job", second, existing, err)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := jobs.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("restore work calls = %d, want 1", got)
	}
	reopened, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	retryID, existing, err := reopened.SubmitIdempotent("restore-3", "site", "deploy-123", func() (ImportResult, error) {
		t.Fatal("persisted idempotency key launched duplicate work")
		return ImportResult{}, nil
	})
	if err != nil || !existing || retryID != first {
		t.Fatalf("post-restart retry = id %q existing %v err %v; want original job", retryID, existing, err)
	}
}

func TestValidJobOperationKey(t *testing.T) {
	for _, value := range []string{"retry-1", "github.delivery:abc_123", "a"} {
		if !validJobOperationKey(value) {
			t.Errorf("valid operation key %q was rejected", value)
		}
	}
	for _, value := range []string{"", "with space", "with/slash", strings.Repeat("a", 129)} {
		if validJobOperationKey(value) {
			t.Errorf("invalid operation key %q was accepted", value)
		}
	}
}

func TestJobsPersistCompletedBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jobs.SubmitBackup("backup-1", "site", func() (BackupResult, error) {
		return BackupResult{Site: "site", Path: "/backups/site", ArchiveSHA256: strings.Repeat("a", 64)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := jobs.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	job, ok := reopened.Get("backup-1")
	if !ok || job.State != "completed" || job.Backup == nil || job.Backup.Site != "site" {
		t.Fatalf("persisted backup job = %#v, found = %v", job, ok)
	}
}

func TestOpenJobsReconcilesInterruptedWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	store := jobStore{Version: 1, Jobs: []*Job{{ID: "restore-1", Kind: "cpmove.restore", State: "running", User: "site", StartedAt: time.Now().Add(-time.Minute)}}}
	data, err := json.Marshal(store)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	jobs, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	job, ok := jobs.Get("restore-1")
	if !ok || job.State != "failed" || job.FinishedAt == nil || !strings.Contains(job.Error, "unclean shutdown") {
		t.Fatalf("reconciled job = %#v, found = %v", job, ok)
	}
}

func TestOpenJobsImportsLegacyArrayState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	legacy := []*Job{{ID: "legacy-1", Kind: "cpmove.restore", State: "completed", User: "site", StartedAt: time.Now().Add(-time.Minute)}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	jobs, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	if job, ok := jobs.Get("legacy-1"); !ok || job.State != "completed" {
		t.Fatalf("imported legacy job = %#v, found = %v", job, ok)
	}

	reopened, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	if job, ok := reopened.Get("legacy-1"); !ok || job.State != "completed" {
		t.Fatalf("rewritten legacy job = %#v, found = %v", job, ok)
	}
	var store jobStore
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &store); err != nil || store.Version != 1 || len(store.Jobs) != 1 {
		t.Fatalf("rewritten state = %s, err = %v", data, err)
	}
}

func TestOpenJobsImportsEmptyLegacyArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	if err := os.WriteFile(path, []byte("[]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	jobs, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := jobs.List(10); len(got) != 0 {
		t.Fatalf("imported empty legacy state = %#v", got)
	}
}

func TestOpenJobsRejectsCorruptState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJobs(path); err == nil {
		t.Fatal("corrupt job state was accepted")
	}
}

func TestJobsFailClosedWhenStateCannotBePersisted(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "state")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	jobs, err := OpenJobs(filepath.Join(blocked, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	err = jobs.Submit("restore-1", "site", func() (ImportResult, error) {
		called = true
		return ImportResult{}, nil
	})
	if err == nil || called {
		t.Fatalf("submit error = %v, work called = %v", err, called)
	}
	if _, ok := jobs.Get("restore-1"); ok {
		t.Fatal("unpersisted job remained visible")
	}
}
