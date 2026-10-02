package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobsSubscribePublishesPublicSummaryOnly(t *testing.T) {
	jobs := NewJobs()
	updates, unsubscribe := jobs.Subscribe()
	defer unsubscribe()

	jobs.publish(Job{ID: "job-1", Kind: "site.backup", State: "running", User: "site", Payload: []byte("secret"), Output: []byte("private result")})
	select {
	case event := <-updates:
		if event.Job.ID != "job-1" || event.Job.State != "running" {
			t.Fatalf("unexpected event: %#v", event)
		}
		if len(event.Job.Payload) != 0 || len(event.Job.Output) != 0 {
			t.Fatal("job event exposed private payload or output")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job event")
	}
}

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

func TestWorkerHeartbeatRequiresCompatibleFreshWorker(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	ready, _, err := jobs.workerReadiness(durableWorkerJobKinds, workerHeartbeatFreshness)
	if err != nil || ready {
		t.Fatalf("empty worker readiness = %v, err %v", ready, err)
	}
	if err := jobs.publishWorkerHeartbeat("worker-a", "host-a", 42, time.Now().UTC(), []string{"archive.inspect"}, nil); err != nil {
		t.Fatal(err)
	}
	ready, _, err = jobs.workerReadiness(durableWorkerJobKinds, workerHeartbeatFreshness)
	if err != nil || ready {
		t.Fatalf("incompatible worker readiness = %v, err %v", ready, err)
	}
	if err := jobs.publishWorkerHeartbeat("worker-a", "host-a", 42, time.Now().UTC(), durableWorkerJobKinds, []string{"job-1"}); err != nil {
		t.Fatal(err)
	}
	ready, detail, err := jobs.workerReadiness(durableWorkerJobKinds, workerHeartbeatFreshness)
	if err != nil || !ready || !strings.Contains(detail, "worker-a") {
		t.Fatalf("compatible worker readiness = %v, detail %q, err %v", ready, detail, err)
	}
}

func TestRunWorkerPublishesAndRemovesHeartbeat(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- jobs.RunWorker(ctx, "heartbeat-worker", []string{"test.operation"}, time.Millisecond, func(context.Context, Job) ([]byte, error) {
			return nil, nil
		})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM worker_heartbeats WHERE worker_id = ?`, "heartbeat-worker").Scan(&count); err == nil && count == 1 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("worker heartbeat was not published")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exit = %v, want context canceled", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM worker_heartbeats WHERE worker_id = ?`, "heartbeat-worker").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("worker heartbeat rows after shutdown = %d, want 0", count)
	}
}

func TestRunWorkerPoolUsesConfiguredConcurrency(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	j := newJobsWithDB(db, 2)
	for i := 0; i < 2; i++ {
		if _, err := j.Enqueue("test.operation", fmt.Sprintf("site-%d", i), "", nil, 1); err != nil {
			t.Fatal(err)
		}
	}
	var active, maximum atomic.Int32
	entered := make(chan struct{}, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- j.RunWorkerPool(ctx, "pool-worker", []string{"test.operation"}, time.Millisecond, 2, func(context.Context, Job) ([]byte, error) {
			current := active.Add(1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			entered <- struct{}{}
			time.Sleep(50 * time.Millisecond)
			active.Add(-1)
			return nil, nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("first pooled worker did not start")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("second pooled worker did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker pool exit = %v, want context canceled", err)
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrent handlers = %d, want 2", got)
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

func TestEmptyOperationKeyIsAtomicallyIdempotentAcrossConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	first, err := OpenDurableJobs(path, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenDurableJobs(path, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	type result struct {
		job      Job
		existing bool
		err      error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, jobs := range []*Jobs{first, second} {
		go func(jobs *Jobs) {
			defer start.Done()
			ready.Done()
			ready.Wait()
			job, existing, enqueueErr := jobs.EnqueueIdempotent("site.terminate", "site", "", []byte(`{"site":"site"}`), 1)
			results <- result{job: job, existing: existing, err: enqueueErr}
		}(jobs)
	}
	start.Wait()
	close(results)
	var jobsSeen int
	for item := range results {
		if item.err != nil {
			t.Fatal(item.err)
		}
		if item.job.ID == "" {
			t.Fatal("empty job ID returned")
		}
		jobsSeen++
	}
	if jobsSeen != 2 {
		t.Fatalf("received %d enqueue results, want 2", jobsSeen)
	}
	var active int
	if err := first.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind = 'site.terminate' AND owner = 'site' AND operation_key = '' AND state IN ('queued', 'running')`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active empty-key jobs = %d, want 1", active)
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

func TestDurableWorkerStopsWhenClaimIsTakenOver(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	worker := newJobsWithDB(db, 1)
	worker.leaseTTL = 30 * time.Millisecond
	queued, err := worker.Enqueue("worker.operation", "site", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	stopped := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- worker.RunWorker(ctx, "worker-a", []string{"worker.operation"}, time.Millisecond, func(handlerCtx context.Context, item Job) ([]byte, error) {
			if item.ID != queued.ID {
				t.Errorf("handler job = %s, want %s", item.ID, queued.ID)
			}
			close(started)
			<-handlerCtx.Done()
			close(stopped)
			return nil, handlerCtx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start handler")
	}

	// Simulate a replacement worker fencing the original claim while the
	// original handler is still running.
	if _, err := db.Exec(`UPDATE jobs SET lease_owner = ?, lease_expires_at = ? WHERE id = ?`, "replacement-worker", time.Now().UTC().Add(time.Minute).UnixNano(), queued.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("handler context was not cancelled after claim takeover")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exit = %v", err)
	}

	var owner, state string
	if err := db.QueryRow(`SELECT lease_owner, state FROM jobs WHERE id = ?`, queued.ID).Scan(&owner, &state); err != nil {
		t.Fatal(err)
	}
	if owner != "replacement-worker" || state != "running" {
		t.Fatalf("replacement claim was overwritten: owner=%q state=%q", owner, state)
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

func TestJobsDoNotExposeUnpersistedCompletion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "jobs.json")
	jobs, err := OpenJobs(path)
	if err != nil {
		t.Fatal(err)
	}
	item := &Job{ID: "completion-1", Kind: "restore", State: "running", User: "site", StartedAt: time.Now().UTC()}
	if err := jobs.add(item); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	item.State = "completed"
	item.Result = &ImportResult{FilesRestored: true}
	now := time.Now().UTC()
	item.FinishedAt = &now
	jobs.complete(item)
	got, ok := jobs.Get(item.ID)
	if !ok {
		t.Fatal("job disappeared after completion persistence failure")
	}
	if got.State != "running" || got.Result != nil || got.FinishedAt != nil {
		t.Fatalf("job after persistence failure = %#v, want active without result", got)
	}
	if jobs.PersistenceError() == nil {
		t.Fatal("completion persistence failure was not recorded")
	}
}

// TestDurableListAlwaysIncludesActiveJobsOutsideTheRecentWindow keeps the
// Job Center's active count authoritative: a long-running job must be listed
// even when many newer jobs have finished since it started.
func TestDurableListAlwaysIncludesActiveJobsOutsideTheRecentWindow(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	old, err := jobs.Enqueue("long.operation", "site-old", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET started_at = 1 WHERE id = ?`, old.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		finished, err := jobs.Enqueue("short.operation", fmt.Sprintf("site-%d", i), "", nil, 1)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE jobs SET state = 'completed' WHERE id = ?`, finished.ID); err != nil {
			t.Fatal(err)
		}
	}
	listed := jobs.List(3)
	if len(listed) != 4 {
		t.Fatalf("List(3) returned %d jobs; want 3 recent plus the old active job", len(listed))
	}
	if last := listed[len(listed)-1]; last.ID != old.ID || last.State != "queued" {
		t.Fatalf("oldest listed job = %#v; want the old queued job last", last)
	}
}
