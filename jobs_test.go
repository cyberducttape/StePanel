package stepanel

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
	defer cancel()
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

func TestEmptyOperationKeyDoesNotCollapseDistinctRequestsAcrossConnections(t *testing.T) {
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
	for index, jobs := range []*Jobs{first, second} {
		go func(jobs *Jobs, index int) {
			defer start.Done()
			ready.Done()
			ready.Wait()
			payload := []byte(fmt.Sprintf(`{"site":"site","operation":"%d"}`, index))
			job, existing, enqueueErr := jobs.EnqueueIdempotent("site.operation", "site", "", payload, 1)
			results <- result{job: job, existing: existing, err: enqueueErr}
		}(jobs, index)
	}
	start.Wait()
	close(results)
	jobIDs := map[string]bool{}
	for item := range results {
		if item.err != nil {
			t.Fatal(item.err)
		}
		if item.job.ID == "" {
			t.Fatal("empty job ID returned")
		}
		if item.existing {
			t.Fatalf("unkeyed request unexpectedly reused job %q", item.job.ID)
		}
		jobIDs[item.job.ID] = true
	}
	if len(jobIDs) != 2 {
		t.Fatalf("distinct unkeyed requests produced %d job IDs, want 2", len(jobIDs))
	}
	var active int
	if err := first.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind = 'site.operation' AND owner = 'site' AND operation_key = '' AND state IN ('queued', 'running')`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 2 {
		t.Fatalf("active empty-key jobs = %d, want 2", active)
	}
	var uniqueIndex int
	if err := first.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'jobs_active_empty_operation_unique_idx'`).Scan(&uniqueIndex); err != nil {
		t.Fatal(err)
	}
	if uniqueIndex != 0 {
		t.Fatal("empty-key active-job unique index was not removed by migration")
	}
}

func TestDurableIdempotencyKeyIsBoundToPayload(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	first, existing, err := jobs.EnqueueIdempotent("site.operation", "site", "git.deploy:request-1", []byte(`{"operation":"git.deploy","commit":"abc"}`), 1)
	if err != nil || existing {
		t.Fatalf("first request = %+v, existing=%v, err=%v", first, existing, err)
	}
	retry, existing, err := jobs.EnqueueIdempotent("site.operation", "site", "git.deploy:request-1", []byte(`{"operation":"git.deploy","commit":"abc"}`), 1)
	if err != nil || !existing || retry.ID != first.ID {
		t.Fatalf("exact retry = %+v, existing=%v, err=%v", retry, existing, err)
	}
	if _, _, err := jobs.EnqueueIdempotent("site.operation", "site", "git.deploy:request-1", []byte(`{"operation":"composer.install","package":"vendor/pkg"}`), 1); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting payload error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestDurableJobReadDoesNotResurrectRowDeletedByAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	writer, err := OpenDurableJobs(path, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := OpenDurableJobs(path, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	item, err := writer.Enqueue("site.backup", "site-a", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cached, found, err := reader.GetWithError(item.ID); err != nil || !found || cached.ID != item.ID {
		t.Fatalf("initial cross-process read = %#v, found=%v, err=%v", cached, found, err)
	}
	if _, ok := reader.items[item.ID]; !ok {
		t.Fatal("initial durable read did not populate the local cache")
	}
	if _, err := writer.db.Exec(`DELETE FROM jobs WHERE id = ?`, item.ID); err != nil {
		t.Fatalf("prune job from writer process: %v", err)
	}
	if got, found, err := reader.GetWithError(item.ID); err != nil || found {
		t.Fatalf("authoritative missing job = %#v, found=%v, err=%v; want not found", got, found, err)
	}
	if _, ok := reader.items[item.ID]; ok {
		t.Fatal("authoritative durable miss left a stale job in the process-local cache")
	}
}

func TestDurableJobReadCannotOverwriteNewLocalLeaseSnapshot(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	queued, err := jobs.Enqueue("worker.operation", "site-a", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	staleRead, found, err := jobs.loadDurableJob(queued.ID)
	if err != nil || !found || staleRead.State != "queued" {
		t.Fatalf("pre-claim read = %#v, found=%v, err=%v", staleRead, found, err)
	}
	claimed, ok, err := jobs.ClaimNext("worker-a", "worker.operation")
	if err != nil || !ok || claimed.State != "running" {
		t.Fatalf("claim = %#v, claimed=%v, err=%v", claimed, ok, err)
	}

	// A read that began before ClaimNext committed can finish afterward. It is
	// valid for the reader, but must not roll the worker's cache back to queued.
	jobs.cacheDurableRead(staleRead)
	jobs.mu.RLock()
	cached := *jobs.items[queued.ID]
	jobs.mu.RUnlock()
	if cached.State != "running" || cached.LeaseOwner != claimed.LeaseOwner {
		t.Fatalf("stale durable read replaced local claim: cached=%#v claimed=%#v", cached, claimed)
	}
}

func TestDurableJobListingsDoNotOverwriteLocalLeaseSnapshot(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	queued, err := jobs.Enqueue("site.operation", "site-a", "site-a", nil, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Model a worker's newer local lease alongside a database snapshot that is
	// still queued. Listing APIs return their query result but must not install
	// that potentially stale snapshot into the worker's mutation cache.
	leaseExpiry := time.Now().UTC().Add(time.Minute)
	jobs.mu.Lock()
	local := *jobs.items[queued.ID]
	local.State = "running"
	local.LeaseOwner = "worker-a"
	local.LeaseExpires = &leaseExpiry
	jobs.items[queued.ID] = &local
	jobs.mu.Unlock()

	checks := []struct {
		name string
		list func() ([]Job, error)
	}{
		{"global", func() ([]Job, error) { return jobs.ListSummariesWithError(10) }},
		{"tenant", func() ([]Job, error) { return jobs.ListForOwnersSummaries(10, []string{"site-a"}) }},
		{"active-site", func() ([]Job, error) { return jobs.ListActiveForSite("site-a") }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			listed, err := check.list()
			if err != nil {
				t.Fatal(err)
			}
			if len(listed) != 1 || listed[0].State != "queued" {
				t.Fatalf("durable listing = %#v, want its queued database snapshot", listed)
			}
			jobs.mu.RLock()
			cached := *jobs.items[queued.ID]
			jobs.mu.RUnlock()
			if cached.State != "running" || cached.LeaseOwner != "worker-a" || cached.LeaseExpires == nil {
				t.Fatalf("listing replaced local lease snapshot: %#v", cached)
			}
		})
	}
}

func TestDurableBackupRetryReusesPersistedLocalArchive(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	backupRoot := filepath.Join(root, "backups")
	writeTestFile(t, filepath.Join(webRoot, "sites", "site-a", "public", "index.php"), "<?php echo 'ok';")
	created, err := CreateSiteBackup(Config{WebRoot: webRoot, BackupRoot: backupRoot}, AuthorizedSite{site: "site-a"}, false)
	if err != nil {
		t.Fatalf("create local backup before simulated offsite failure: %v", err)
	}
	path := filepath.Join(root, "control.db")
	jobs, err := OpenDurableJobs(path, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	initialPayload, err := json.Marshal(durableBackupRequest{
		Site: "site-a", Actor: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := jobs.Enqueue("site.backup", "site-a", "", initialPayload, 3)
	if err != nil {
		_ = jobs.Close()
		t.Fatal(err)
	}
	first, ok, err := jobs.ClaimNext("worker-a", "site.backup")
	if err != nil || !ok {
		_ = jobs.Close()
		t.Fatalf("first backup claim = %#v, claimed=%v, err=%v", first, ok, err)
	}
	archivePayload, err := json.Marshal(durableBackupRequest{
		Site: "site-a", Actor: "operator",
		BackupPath: created.Path, BackupSHA256: created.ArchiveSHA256, BackupBytes: created.Bytes,
	})
	if err != nil {
		_ = jobs.Close()
		t.Fatal(err)
	}
	if err := jobs.UpdateClaimPayload(queued.ID, "worker-a", archivePayload); err != nil {
		_ = jobs.Close()
		t.Fatalf("persist completed local archive before remote upload: %v", err)
	}
	if err := jobs.FailClaim(queued.ID, "worker-a", "offsite destination unavailable"); err != nil {
		_ = jobs.Close()
		t.Fatalf("record transient offsite failure: %v", err)
	}
	if _, err := jobs.db.Exec(`UPDATE jobs SET next_attempt_at=0 WHERE id=?`, queued.ID); err != nil {
		_ = jobs.Close()
		t.Fatalf("make retry immediately eligible: %v", err)
	}
	if err := jobs.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := OpenDurableJobs(path, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	retry, ok, err := restarted.ClaimNext("worker-b", "site.backup")
	if err != nil || !ok {
		t.Fatalf("retry claim after worker restart = %#v, claimed=%v, err=%v", retry, ok, err)
	}
	var persisted durableBackupRequest
	if err := json.Unmarshal(retry.Payload, &persisted); err != nil {
		t.Fatalf("decode retry payload: %v", err)
	}
	if persisted.BackupPath != created.Path || persisted.BackupSHA256 != created.ArchiveSHA256 || persisted.BackupBytes != created.Bytes {
		t.Fatalf("retry did not retain the verified local archive; payload=%+v", persisted)
	}
	app := &App{
		Config: Config{BackupRoot: backupRoot, WebRoot: filepath.Join(root, "site-tree-removed-after-failure")},
		Auth:   Auth{Username: "operator"},
		Jobs:   restarted,
	}
	if _, err := app.handleBackupJob(context.Background(), retry); err != nil {
		t.Fatalf("retry should upload the verified prior archive without rebuilding the removed site tree: %v", err)
	}
}

func TestDurableCancellationFailsClosedWhenDatabaseCannotConfirmState(t *testing.T) {
	jobs, err := OpenDurableJobs(filepath.Join(t.TempDir(), "control.db"), "", 1)
	if err != nil {
		t.Fatal(err)
	}
	item, err := jobs.Enqueue("site.backup", "site-a", "", nil, 1)
	if err != nil {
		_ = jobs.Close()
		t.Fatal(err)
	}
	if cached, ok := jobs.items[item.ID]; !ok || cached.Cancel {
		_ = jobs.Close()
		t.Fatalf("precondition: cached job = %#v, exists=%v", cached, ok)
	}
	if err := jobs.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.CancellationRequestedWithError(item.ID); err == nil {
		t.Fatal("durable cancellation lookup hid its database error")
	}
	if err := jobs.CheckNotCancelled(item.ID); err == nil {
		t.Fatal("worker checkpoint continued when durable cancellation state was unavailable")
	}
	if !jobs.CancellationRequested(item.ID) {
		t.Fatal("worker continued using cached state after durable cancellation lookup failed")
	}
}

func TestDurableCancellationTreatsMissingRowAsNoLongerRunnable(t *testing.T) {
	jobs, err := OpenDurableJobs(filepath.Join(t.TempDir(), "control.db"), "", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer jobs.Close()
	item, err := jobs.Enqueue("site.backup", "site-a", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.db.Exec(`DELETE FROM jobs WHERE id = ?`, item.ID); err != nil {
		t.Fatal(err)
	}
	requested, err := jobs.CancellationRequestedWithError(item.ID)
	if err != nil || !requested {
		t.Fatalf("missing durable job cancellation state = requested %v, err %v; want safely stopped", requested, err)
	}
	if err := jobs.CheckNotCancelled(item.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("checkpoint error for missing job = %v, want context.Canceled", err)
	}
	if !jobs.CancellationRequested(item.ID) {
		t.Fatal("worker treated a durably missing job as runnable based on cached state")
	}
}

func TestDurableIdempotencyExpiryIsTimestampBasedAndRestartSafe(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 2)
	expiresAt := time.Now().UTC().Add(time.Hour)
	key := "instance:reboot:operator-request"
	payload := []byte(`{"operation":"instance","action":"reboot"}`)
	first, existing, err := jobs.EnqueueIdempotentWithExpiry("cloud.action", "linode:instance:123", key, payload, 1, expiresAt)
	if err != nil || existing {
		t.Fatalf("initial expiring request = %+v, existing=%v, err=%v", first, existing, err)
	}
	if first.IdempotencyExpiresAt == nil || !first.IdempotencyExpiresAt.Equal(expiresAt) {
		t.Fatalf("job expiration = %v, want %v", first.IdempotencyExpiresAt, expiresAt)
	}
	restarted := newJobsWithDB(db, 2)
	if err := restarted.load(); err != nil {
		t.Fatal(err)
	}
	reloaded := restarted.items[first.ID]
	if reloaded == nil || reloaded.IdempotencyExpiresAt == nil || !reloaded.IdempotencyExpiresAt.Equal(expiresAt) {
		t.Fatalf("reloaded expiration = %v", reloaded)
	}
	retry, existing, err := restarted.EnqueueIdempotentWithExpiry("cloud.action", "linode:instance:123", key, payload, 1, expiresAt)
	if err != nil || !existing || retry.ID != first.ID {
		t.Fatalf("same request retry = %+v, existing=%v, err=%v", retry, existing, err)
	}
	expiredAt := time.Now().UTC().Add(-time.Second)
	if _, err := db.Exec(`UPDATE jobs SET idempotency_expires_at=? WHERE id=?`, expiredAt.UnixNano(), first.ID); err != nil {
		t.Fatal(err)
	}
	restarted.items[first.ID].IdempotencyExpiresAt = &expiredAt
	activeRetry, existing, err := restarted.EnqueueIdempotentWithExpiry("cloud.action", "linode:instance:123", key, payload, 1, time.Now().UTC().Add(time.Hour))
	if err != nil || !existing || activeRetry.ID != first.ID {
		t.Fatalf("expired active request = %+v, existing=%v, err=%v; active attempt must remain deduplicated", activeRetry, existing, err)
	}
	claimed, ok, err := restarted.Claim(first.ID, "expiry-test-worker")
	if err != nil || !ok {
		t.Fatalf("claim expiring job = %v, %v", ok, err)
	}
	if err := restarted.finishClaim(claimed.ID, "expiry-test-worker", func(item *Job) {
		item.State = "completed"
		finished := time.Now().UTC()
		item.FinishedAt = &finished
	}); err != nil {
		t.Fatal(err)
	}
	second, existing, err := restarted.EnqueueIdempotentWithExpiry("cloud.action", "linode:instance:123", key, payload, 1, time.Now().UTC().Add(time.Hour))
	if err != nil || existing || second.ID == first.ID {
		t.Fatalf("request after expiry = %+v, existing=%v, err=%v; want a new durable attempt", second, existing, err)
	}
	var oldKey string
	if err := db.QueryRow(`SELECT operation_key FROM jobs WHERE id=?`, first.ID).Scan(&oldKey); err != nil {
		t.Fatal(err)
	}
	if oldKey != "" {
		t.Fatalf("expired attempt retained unique operation key %q", oldKey)
	}
}

func TestLegacyCloudJobOwnerIsNamespacedBeforeNewClaims(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacy := newJobsWithDB(db, 2)
	request := durableCloudRequest{Operation: "instance", Provider: "linode", Action: "reboot", ID: "123", Actor: "admin"}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	old, err := legacy.Enqueue("cloud.action", "123", "", payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := openDurableJobsDB(db, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	loaded, found, err := jobs.loadDurableJob(old.ID)
	if err != nil || !found || loaded.User != "linode:instance:123" {
		t.Fatalf("migrated job owner = %q, found=%v, err=%v", loaded.User, found, err)
	}
	app := &App{Jobs: jobs}
	newJob, err := app.enqueueCloudJob(request, "")
	if err != nil {
		t.Fatal(err)
	}
	dnsJob, err := app.enqueueCloudJob(durableCloudRequest{Operation: "dns", Provider: "linode", Action: "update", ID: "123", DNS: cloudDNSRequest{DomainID: "123", RecordID: "7", Type: "A", Name: "www", Target: "192.0.2.7", TTL: 300}, Actor: "admin"}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, claimed, err := jobs.ClaimNext("owner-migration-worker", "cloud.action")
	if err != nil || !claimed {
		t.Fatalf("claim first same-resource attempt = %v, %v", claimed, err)
	}
	second, claimed, err := jobs.ClaimNext("owner-migration-worker", "cloud.action")
	if err != nil || !claimed {
		t.Fatalf("same-numeric-ID DNS job was blocked by instance: %v, %v", claimed, err)
	}
	if first.ID != old.ID || second.ID != dnsJob.ID {
		t.Fatalf("claim order = %q then %q, want legacy instance %q then independent DNS %q", first.ID, second.ID, old.ID, dnsJob.ID)
	}
	if _, claimed, err := jobs.ClaimNext("owner-migration-worker", "cloud.action"); err != nil || claimed {
		t.Fatalf("same instance was not serialized behind the legacy attempt: claimed=%v err=%v", claimed, err)
	}
	if err := jobs.finishClaim(first.ID, "owner-migration-worker", func(item *Job) {
		item.State = "completed"
		finished := time.Now().UTC()
		item.FinishedAt = &finished
	}); err != nil {
		t.Fatal(err)
	}
	third, claimed, err := jobs.ClaimNext("owner-migration-worker", "cloud.action")
	if err != nil || !claimed || third.ID != newJob.ID {
		t.Fatalf("claim new instance after legacy completion = %q, %v, %v; want %q", third.ID, claimed, err, newJob.ID)
	}
}

func TestActivePayloadQueriesAreSinglePassAndOwnerScoped(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	for _, job := range []struct {
		owner   string
		payload string
	}{{"123", `{"operation":"dns","domain_id":"123"}`}, {"123", `{"operation":"dns","domain_id":"123","record_id":"2"}`}, {"456", `{"operation":"dns","domain_id":"456"}`}, {"instance-1", `{"operation":"instance"}`}} {
		if _, err := jobs.Enqueue("cloud.action", job.owner, "", []byte(job.payload), 1); err != nil {
			t.Fatal(err)
		}
	}
	all, err := jobs.ActivePayloads("cloud.action")
	if err != nil || len(all) != 4 {
		t.Fatalf("all active payloads = %d, err=%v; want 4", len(all), err)
	}
	for _, payload := range all {
		if !json.Valid(payload) {
			t.Fatalf("active payload is not valid JSON: %q", payload)
		}
	}
	domain, err := jobs.ActivePayloadsForOwner("cloud.action", "123")
	if err != nil || len(domain) != 2 {
		t.Fatalf("owner-scoped payloads = %d, err=%v; want 2", len(domain), err)
	}
	for _, payload := range domain {
		var decoded map[string]string
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["domain_id"] != "123" {
			t.Fatalf("owner-scoped query returned unrelated payload: %s", payload)
		}
	}
	if _, err := jobs.activePayloads(`kind = ?`, []any{"cloud.action"}, 3); err == nil || !strings.Contains(err.Error(), "3-job safety limit") {
		t.Fatalf("over-limit payload query error = %v, want explicit bounded-query failure", err)
	}
}

func TestActivePayloadsForOwnerAcceptsMoreThanLegacy500Cutoff(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	for i := 0; i < 501; i++ {
		if _, err := jobs.Enqueue("cloud.action", "linode:domain:123", fmt.Sprintf("record-%d", i), []byte(`{"operation":"dns","domain_id":"123"}`), 1); err != nil {
			t.Fatalf("enqueue active record %d: %v", i, err)
		}
	}
	payloads, err := jobs.ActivePayloadsForOwner("cloud.action", "linode:domain:123")
	if err != nil || len(payloads) != 501 {
		t.Fatalf("active owner payload count = %d, err=%v; want 501", len(payloads), err)
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

func TestDurableFailureRetainsPartialHandlerOutput(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	queued, err := jobs.Enqueue("cpmove.restore", "site", "migration", []byte(`{"site":"site"}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := jobs.ClaimNext("worker", "cpmove.restore")
	if err != nil || !ok {
		t.Fatalf("claim = %#v, ok=%v, err=%v", claimed, ok, err)
	}
	partial := []byte(`{"user":"site","components":[{"category":"databases","source":"wpdb","disposition":"failed"}]}`)
	if err := jobs.failClaimWithOutput(queued.ID, "worker", "migration failed", false, partial); err != nil {
		t.Fatal(err)
	}
	got, ok, err := jobs.GetWithError(queued.ID)
	if err != nil || !ok {
		t.Fatalf("read failed job = %#v, ok=%v, err=%v", got, ok, err)
	}
	if got.State != "dead-letter" || got.Result == nil || len(got.Result.Components) != 1 || got.Result.Components[0].Disposition != MigrationFailed {
		t.Fatalf("failed migration did not retain partial report: %#v", got)
	}
}

func TestClaimNextQuarantinesUnreadablePayloadAndContinuesOwnerQueue(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	poisoned, err := jobs.Enqueue("test.operation", "same-site", "", []byte(`{"ok":true}`), 3)
	if err != nil {
		t.Fatal(err)
	}
	next, err := jobs.Enqueue("test.operation", "same-site", "", []byte(`{"next":true}`), 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE jobs SET payload=? WHERE id=?`, []byte(`{broken`), poisoned.ID); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := jobs.ClaimNext("worker", "test.operation"); err != nil || claimed {
		t.Fatalf("poison claim = claimed %v, err %v; want quarantined without lease", claimed, err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM jobs WHERE id=?`, poisoned.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "dead-letter" {
		t.Fatalf("poison state = %q, want dead-letter", state)
	}
	reloaded := newJobsWithDB(db, 1)
	if err := reloaded.load(); err != nil {
		t.Fatalf("load quarantined job: %v", err)
	}
	dead, ok := reloaded.Get(poisoned.ID)
	if !ok || dead.State != "dead-letter" || !strings.Contains(dead.Error, "permanent job payload error") {
		t.Fatalf("quarantined job record = %#v, found=%v", dead, ok)
	}
	claimedJob, claimed, err := jobs.ClaimNext("worker", "test.operation")
	if err != nil || !claimed || claimedJob.ID != next.ID {
		t.Fatalf("next same-owner claim = %#v, claimed %v, err %v", claimedJob, claimed, err)
	}
}

func TestClaimNextPreservesEncryptedJobWhenWorkerKeyIsUnavailable(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	producer, err := openDurableJobsDBWithKey(db, "", "correct-account-key", 1)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := producer.Enqueue("test.operation", "same-site", "", []byte(`{"operation":"verified"}`), 3)
	if err != nil {
		t.Fatal(err)
	}

	// This models a separately started worker whose key configuration is
	// missing or stale while another process continues to enqueue valid work.
	misconfiguredWorker := newJobsWithDB(db, 1)
	if _, claimed, err := misconfiguredWorker.ClaimNext("worker-without-key", "test.operation"); !errors.Is(err, errJobPayloadKeyUnavailable) || claimed {
		t.Fatalf("claim without key = claimed %v, err %v; want a key-unavailable error and no claim", claimed, err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM jobs WHERE id=?`, queued.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("job state after unavailable-key claim = %q, want queued", state)
	}

	claimedJob, claimed, err := producer.ClaimNext("worker-with-key", "test.operation")
	if err != nil || !claimed || claimedJob.ID != queued.ID || string(claimedJob.Payload) != `{"operation":"verified"}` {
		t.Fatalf("claim with key = %#v, claimed %v, err %v; want original queued payload", claimedJob, claimed, err)
	}
}

func TestDurableJobCleanupRetainsDeadLettersAcrossRestart(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 1)
	queued, err := jobs.Enqueue("test.operation", "site", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := jobs.ClaimNext("worker-a", "test.operation")
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("claim = %#v, %v, %v", claimed, ok, err)
	}
	if err := jobs.FailClaim(claimed.ID, "worker-a", "needs operator reconciliation"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM jobs WHERE id=?`, queued.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "dead-letter" {
		t.Fatalf("job state = %q, want dead-letter", state)
	}
	old := time.Now().UTC().Add(-48 * time.Hour).UnixNano()
	if _, err := db.Exec(`UPDATE jobs SET finished_at=? WHERE id=?`, old, queued.ID); err != nil {
		t.Fatal(err)
	}

	jobs.Cleanup(24 * time.Hour)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id=? AND state='dead-letter'`, queued.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expired dead-letter row count = %d, want 1", count)
	}
	restarted := newJobsWithDB(db, 1)
	if err := restarted.load(); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := restarted.GetWithError(queued.ID); err != nil || !ok || got.State != "dead-letter" {
		t.Fatalf("dead-letter after restart = %#v, found=%v, err=%v", got, ok, err)
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

func TestDurableWorkerReclaimsExpiredClaimWhileIdle(t *testing.T) {
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
	// The replacement starts before the old lease expires, then has no work
	// available. Its periodic idle wait must still trigger lease reclamation.
	if _, err := db.Exec(`UPDATE jobs SET lease_expires_at = ? WHERE id = ?`, time.Now().UTC().Add(time.Minute).UnixNano(), queued.ID); err != nil {
		t.Fatal(err)
	}

	worker := newJobsWithDB(db, 1)
	worker.leaseTTL = 180 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- worker.RunWorker(ctx, "replacement-worker", []string{"worker.operation"}, time.Hour, func(_ context.Context, item Job) ([]byte, error) {
			if item.ID != queued.ID {
				t.Errorf("reclaimed job = %s, want %s", item.ID, queued.ID)
			}
			cancel()
			return []byte(`{"recovered":true}`), nil
		})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM worker_heartbeats WHERE worker_id = ?`, "replacement-worker").Scan(&count); err == nil && count == 1 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("replacement worker did not start before lease expiry")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := db.Exec(`UPDATE jobs SET lease_expires_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Second).UnixNano(), queued.ID); err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("worker exit = %v, want context canceled after recovering job", err)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("idle worker did not reclaim the expired job")
	}
	if got, ok := worker.Get(queued.ID); !ok || got.State != "completed" {
		t.Fatalf("recovered job = %#v, found = %v", got, ok)
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

func TestDurableJobListNeverFallsBackToStaleCache(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := newJobsWithDB(db, 1)
	queued, err := jobs.Enqueue("list.operation", "site", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if listed := jobs.List(10); len(listed) != 1 || listed[0].ID != queued.ID {
		t.Fatalf("initial durable list = %#v", listed)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.ListWithError(10); err == nil {
		t.Fatal("durable list concealed SQLite failure")
	}
	if listed := jobs.List(10); listed != nil {
		t.Fatalf("compatibility list returned stale cached jobs: %#v", listed)
	}
	if listed := jobs.ListSummaries(10); listed != nil {
		t.Fatalf("summary list returned stale cached jobs: %#v", listed)
	}
	if item, ok := jobs.Get(queued.ID); ok {
		t.Fatalf("compatibility lookup returned stale cached job: %#v", item)
	}
}

func TestListForOwnersAppliesLimitAfterOwnerFilter(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	jobs := newJobsWithDB(db, 1)
	tenantJob, err := jobs.Enqueue("site.operation", "tenant-site", "", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		if _, err := jobs.Enqueue("site.operation", "busy-site", fmt.Sprintf("busy-%d", i), nil, 1); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := jobs.ListForOwners(1, []string{"tenant-site"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != tenantJob.ID {
		t.Fatalf("tenant history = %#v, want only %q", listed, tenantJob.ID)
	}
}

func TestListActiveForSiteIsAuthoritativeAndUnbounded(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	jobs := newJobsWithDB(db, 1)
	for i := 0; i < 501; i++ {
		if _, err := jobs.Enqueue("site.operation", "site", fmt.Sprintf("operation-%d", i), nil, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := jobs.Enqueue("site.operation", "other-site", "other", nil, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Enqueue("site.operation", "other-site", "site", nil, 1); err != nil {
		t.Fatal(err)
	}
	active, err := jobs.ListActiveForSite("site")
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 502 {
		t.Fatalf("active site jobs = %d, want 502", len(active))
	}
	for _, job := range active {
		if (job.User != "site" && job.OperationKey != "site") || (job.State != "queued" && job.State != "running") {
			t.Fatalf("unexpected active job: %#v", job)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.ListActiveForSite("site"); err == nil {
		t.Fatal("active job query succeeded after durable store closed")
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

func TestExpiredCloudActionBecomesDeadLetterForReconciliation(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	producer := newJobsWithDB(db, 1)
	queued, err := producer.Enqueue("cloud.action", "instance-123", "", []byte(`{"action":"reboot"}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	worker := newJobsWithDB(db, 1)
	claimed, ok, err := worker.ClaimNext("worker-a", "cloud.action")
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("claim = %#v, %v, %v", claimed, ok, err)
	}
	if _, err := db.Exec(`UPDATE jobs SET lease_expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).UnixNano(), queued.ID); err != nil {
		t.Fatal(err)
	}
	count, err := worker.RequeueExpired()
	if err != nil || count != 1 {
		t.Fatalf("expired jobs changed = %d, err = %v", count, err)
	}
	restarted := newJobsWithDB(db, 1)
	if err := restarted.load(); err != nil {
		t.Fatal(err)
	}
	got, exists := restarted.Get(queued.ID)
	if !exists || got.State != "dead-letter" || !strings.Contains(got.Error, "outcome is unknown") {
		t.Fatalf("expired cloud action = %#v; want dead-letter with reconciliation warning", got)
	}
	if _, ok, err := restarted.ClaimNext("worker-b", "cloud.action"); err != nil || ok {
		t.Fatalf("uncertain cloud action was claimable again: ok=%v err=%v", ok, err)
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
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- j.RunWorker(ctx, "worker-a", []string{"worker.operation"}, time.Millisecond, func(_ context.Context, item Job) ([]byte, error) {
			if item.ID != queued.ID || string(item.Payload) != `{"operation":"safe"}` {
				t.Errorf("handler received %#v", item)
			}
			return []byte(`{"result":"ok"}`), nil
		})
	}()
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		item, ok, readErr := j.GetWithError(queued.ID)
		if readErr != nil {
			t.Fatalf("read durable worker result: %v", readErr)
		}
		if ok && item.State == "completed" {
			if string(item.Output) != `{"result":"ok"}` {
				t.Fatalf("worker output = %s", item.Output)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("worker exit = %v", err)
			}
			return
		}
		select {
		case workerErr := <-done:
			durable, found, readErr := j.GetWithError(queued.ID)
			t.Fatalf("worker exited before completing durable job (found=%v state=%q readErr=%v): %v", found, durable.State, readErr, workerErr)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	workerErr := <-done
	item, found, readErr := j.GetWithError(queued.ID)
	t.Fatalf("worker did not complete durable job before deadline (found=%v state=%q readErr=%v workerErr=%v)", found, item.State, readErr, workerErr)
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

func TestDurableClaimTransitionRestoresMemoryOnPersistenceFailure(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	jobs := newJobsWithDB(db, 1)
	item, err := jobs.Enqueue("test.transition", "site", "", nil, 1)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	claimed, ok, err := jobs.ClaimNext("worker-a")
	if err != nil || !ok || claimed.ID != item.ID {
		db.Close()
		t.Fatalf("claim = %#v, %v, %v", claimed, ok, err)
	}
	db.Close()
	if err := jobs.finishClaim(item.ID, "worker-a", func(job *Job) { job.State = "completed" }); err == nil {
		t.Fatal("finishClaim unexpectedly succeeded after database close")
	}
	if _, ok, err := jobs.GetWithError(item.ID); ok || err == nil {
		t.Fatalf("durable lookup after database failure = found %v, err %v; want unavailable", ok, err)
	}
	jobs.mu.RLock()
	got := jobs.items[item.ID]
	if got == nil || got.State != "running" || got.LeaseOwner != "worker-a" {
		jobs.mu.RUnlock()
		t.Fatalf("local rollback state after failed transition = %#v", got)
	}
	jobs.mu.RUnlock()
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

func TestRunWorkerPoolDeadLettersNonRetryableFailuresImmediately(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	j := newJobsWithDB(db, 1)
	final, err := j.Enqueue("test.final", "site-a", "", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := j.Enqueue("test.retry", "site-b", "", nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- j.RunWorkerPool(ctx, "pool-worker", []string{"test.final", "test.retry"}, time.Millisecond, 1, func(_ context.Context, item Job) ([]byte, error) {
			if item.Kind == "test.final" {
				return nil, doNotRetry(errors.New("live state may be partial"))
			}
			return nil, errors.New("transient failure")
		})
	}()
	// The full race suite runs under a single P and can leave this lightweight
	// worker briefly unscheduled while unrelated filesystem/database tests run.
	// Keep a bounded wait, but don't make five seconds of runner contention look
	// like a worker-pool failure.
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case workerErr := <-done:
			t.Fatalf("worker stopped before jobs settled: %v", workerErr)
		default:
		}
		finalJob, _ := j.Get(final.ID)
		retriedJob, _ := j.Get(retried.ID)
		if finalJob.State == "dead-letter" && retriedJob.Attempts >= 1 {
			if finalJob.Attempts != 1 || !strings.Contains(finalJob.Error, "live state may be partial") {
				t.Fatalf("non-retryable job = %+v, want one attempt then dead-letter", finalJob)
			}
			if retriedJob.State == "dead-letter" && retriedJob.Attempts < 3 {
				t.Fatalf("retryable job dead-lettered early: %+v", retriedJob)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("jobs did not settle: final=%+v retried=%+v", finalJob, retriedJob)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// A worker whose lease moved to another worker must see the shared sentinel
// from every claim mutation, so RunWorker skips the job instead of exiting.
func TestDurableJobLostLeaseReportsSentinel(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	j := newJobsWithDB(db, 1)
	item := &Job{ID: "job-lost-lease", Kind: "test", State: "queued", User: "tenant", MaxAttempts: 3, StartedAt: time.Now().UTC()}
	if err := j.add(item); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := j.Claim(item.ID, "worker-a"); err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	for name, mutate := range map[string]func() error{
		"fail":     func() error { return j.FailClaim(item.ID, "worker-b", "boom") },
		"progress": func() error { return j.UpdateClaim(item.ID, "worker-b", 50) },
		"payload":  func() error { return j.UpdateClaimPayload(item.ID, "worker-b", []byte(`{}`)) },
	} {
		if err := mutate(); !errors.Is(err, errJobLeaseNotHeld) {
			t.Errorf("%s by a worker without the lease = %v, want errJobLeaseNotHeld", name, err)
		}
	}
}

func TestDurableJobCachePrunesOnlyOldFinishedJobs(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	j := newJobsWithDB(db, 1)
	old := time.Now().UTC().Add(-48 * time.Hour)
	recent := time.Now().UTC().Add(-time.Minute)
	j.items = map[string]*Job{
		"old-finished":    {ID: "old-finished", State: "completed", FinishedAt: &old},
		"old-dead-letter": {ID: "old-dead-letter", State: "dead-letter", FinishedAt: &old},
		"recent-finished": {ID: "recent-finished", State: "completed", FinishedAt: &recent},
		"running":         {ID: "running", State: "running"},
	}
	j.mu.Lock()
	j.pruneFinishedCacheLocked(time.Now().Add(-finishedCacheRetention))
	j.mu.Unlock()
	for id, want := range map[string]bool{"old-finished": false, "old-dead-letter": true, "recent-finished": true, "running": true} {
		if _, kept := j.items[id]; kept != want {
			t.Errorf("cache entry %s kept = %v, want %v", id, kept, want)
		}
	}
}
