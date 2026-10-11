package stepanel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	jobadmission "github.com/cyberducttape/StePanel/internal/jobs"
	"github.com/cyberducttape/StePanel/internal/secretbox"
	"github.com/cyberducttape/StePanel/internal/state"
)

var ErrJobBusy = errors.New("too many long-running jobs or target is already active")

// authorizeDurableSiteJob rechecks tenant policy at execution time. Queue
// admission is not sufficient because an account can be suspended or detached
// from a site while a worker is waiting on another job. On success, returns
// an AuthorizedDurableSite capability that can be passed to data-access
// functions expecting SiteCapability.
func (a *App) authorizeDurableSiteJob(site, actor string, scheduled bool) (AuthorizedDurableSite, error) {
	if safeUser(site) == "" || strings.TrimSpace(actor) == "" {
		return AuthorizedDurableSite{}, errors.New("durable site job has invalid ownership data")
	}
	if scheduled || actor == a.Auth.Username {
		return AuthorizedDurableSite{site: site}, nil
	}
	if a.Accounts == nil {
		return AuthorizedDurableSite{}, errors.New("tenant ownership state is unavailable")
	}
	_, ok := a.Accounts.Get(actor)
	if !ok || a.Accounts.TenantSuspended(actor) || !a.Accounts.OwnsSite(actor, site) {
		return AuthorizedDurableSite{}, errors.New("durable job actor no longer owns the site")
	}
	return AuthorizedDurableSite{site: site}, nil
}

const maxJobStateBytes = 16 << 20

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validJobOperationKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:-", r) {
			continue
		}
		return false
	}
	return true
}

// requestOperationKey validates an optional HTTP idempotency key. Callers may
// pass the returned value directly to EnqueueIdempotent; an empty value keeps
// the existing active-job de-duplication behavior for clients that have not
// opted into replay-safe retries yet.
func requestOperationKey(r *http.Request) (string, error) {
	if r == nil {
		return "", errors.New("request is required")
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key != "" && !validJobOperationKey(key) {
		return "", errors.New("invalid Idempotency-Key")
	}
	return key, nil
}

func newJobID(kind string) (string, error) {
	random, err := randomSecret()
	if err != nil {
		return "", err
	}
	return kind + "-" + random, nil
}

type Job struct {
	ID           string               `json:"id"`
	Kind         string               `json:"kind"`
	OperationKey string               `json:"operation_key,omitempty"`
	State        string               `json:"state"`
	User         string               `json:"user"`
	Result       *ImportResult        `json:"result,omitempty"`
	WPress       *WPressResult        `json:"wpress,omitempty"`
	Certificate  *CertificateResult   `json:"certificate,omitempty"`
	Backup       *BackupResult        `json:"backup,omitempty"`
	Restore      *BackupRestoreResult `json:"restore,omitempty"`
	Cloud        *CloudActionResult   `json:"cloud,omitempty"`
	Error        string               `json:"error,omitempty"`
	StartedAt    time.Time            `json:"started_at"`
	FinishedAt   *time.Time           `json:"finished_at,omitempty"`
	Payload      json.RawMessage      `json:"-"`
	Output       json.RawMessage      `json:"output,omitempty"`
	Attempts     int                  `json:"attempts,omitempty"`
	MaxAttempts  int                  `json:"max_attempts,omitempty"`
	NextAttempt  *time.Time           `json:"next_attempt_at,omitempty"`
	Progress     int                  `json:"progress,omitempty"`
	Cancel       bool                 `json:"cancel_requested,omitempty"`
	LeaseOwner   string               `json:"-"`
	LeaseExpires *time.Time           `json:"-"`
}

// JobEvent is emitted after a durable job change has been persisted. Payloads
// are deliberately omitted from the event stream; the browser only needs the
// public job state and can fetch a detail record when required.
type JobEvent struct {
	Job Job `json:"job"`
}

type persistedJob struct {
	Job
	Payload json.RawMessage `json:"payload,omitempty"`
}

var encryptedJobPayloadPrefix = []byte("SPJ1")

func decodePersistedJobPayload(raw json.RawMessage) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var encoded []byte
	if err := json.Unmarshal(raw, &encoded); err == nil {
		return encoded
	}
	// Rows written before payload encryption stored the JSON payload directly.
	return append([]byte(nil), raw...)
}

const jobLeaseDuration = 2 * time.Minute

var errJobLeaseNotHeld = errors.New("job lease is not held by this worker")

const workerHeartbeatFreshness = 30 * time.Second

var durableWorkerJobKinds = []string{"cpmove.restore", "site.backup", "certificate.issue", "wordpress.restore", "backup.restore", "backup.rehearsal", "cloud.action", "node.deployment", "site.terminate", "migration.analysis", "archive.inspect", "archive.import", "site.create", "site.operation"}

type workerHeartbeatState struct {
	mu          sync.RWMutex
	currentJobs map[string]bool
}

func workerHostID() string {
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		if value := strings.TrimSpace(string(data)); value != "" {
			return value
		}
	}
	if value, err := os.Hostname(); err == nil && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return "unknown-host"
}

func (j *Jobs) publishWorkerHeartbeat(workerID, hostID string, pid int, startedAt time.Time, supported []string, current []string) error {
	if j.db == nil {
		return errors.New("worker heartbeat requires a durable database")
	}
	supportedJSON, err := json.Marshal(supported)
	if err != nil {
		return err
	}
	currentJSON, err := json.Marshal(current)
	if err != nil {
		return err
	}
	_, err = j.db.Exec(`INSERT INTO worker_heartbeats (worker_id, host_id, pid, started_at, last_seen, supported_job_kinds, current_jobs, version, build)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(worker_id) DO UPDATE SET host_id=excluded.host_id, pid=excluded.pid, started_at=excluded.started_at, last_seen=excluded.last_seen, supported_job_kinds=excluded.supported_job_kinds, current_jobs=excluded.current_jobs, version=excluded.version, build=excluded.build`,
		workerID, hostID, pid, startedAt.UnixNano(), time.Now().UTC().UnixNano(), string(supportedJSON), string(currentJSON), Version, Commit)
	return err
}

func (j *Jobs) removeWorkerHeartbeat(workerID string) error {
	if j.db == nil {
		return nil
	}
	_, err := j.db.Exec(`DELETE FROM worker_heartbeats WHERE worker_id = ?`, workerID)
	return err
}

func (j *Jobs) workerReadiness(requiredKinds []string, maxAge time.Duration) (bool, string, error) {
	if j == nil || j.db == nil {
		return false, "durable worker heartbeat store is unavailable", nil
	}
	if maxAge <= 0 {
		maxAge = workerHeartbeatFreshness
	}
	rows, err := j.db.Query(`SELECT worker_id, last_seen, supported_job_kinds, version, build FROM worker_heartbeats WHERE last_seen >= ?`, time.Now().UTC().Add(-maxAge).UnixNano())
	if err != nil {
		return false, "worker heartbeat query failed: " + err.Error(), err
	}
	defer rows.Close()
	for rows.Next() {
		var workerID, kindsJSON, version, build string
		var lastSeen int64
		if err := rows.Scan(&workerID, &lastSeen, &kindsJSON, &version, &build); err != nil {
			return false, "worker heartbeat row is invalid: " + err.Error(), err
		}
		var supported []string
		if err := json.Unmarshal([]byte(kindsJSON), &supported); err != nil {
			continue
		}
		set := make(map[string]bool, len(supported))
		for _, kind := range supported {
			set[kind] = true
		}
		compatible := true
		for _, kind := range requiredKinds {
			if !set[kind] {
				compatible = false
				break
			}
		}
		if compatible {
			return true, fmt.Sprintf("worker %s is alive (version %s, build %s)", workerID, version, build), nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, "worker heartbeat query failed: " + err.Error(), err
	}
	return false, fmt.Sprintf("no compatible durable worker heartbeat within %s", maxAge), nil
}

// Claim acquires a durable lease for a queued job. It is the boundary used by
// local workers and future remote agents; only the lease holder may complete
// or renew the job.
func (j *Jobs) Claim(id, owner string) (Job, bool, error) {
	if j.db == nil || id == "" || owner == "" {
		return Job{}, false, errors.New("durable job claiming requires a job ID and owner")
	}
	now := time.Now().UTC()
	expires := now.Add(j.leaseDuration())
	j.mu.Lock()
	defer j.mu.Unlock()
	item, ok := j.items[id]
	if !ok || item == nil || item.State != "queued" {
		return Job{}, false, nil
	}
	previous := *item
	item.State = "running"
	item.LeaseOwner = owner
	item.LeaseExpires = &expires
	result, err := j.db.Exec(`UPDATE jobs SET state='running', lease_owner=?, lease_expires_at=?, updated_at=unixepoch() WHERE id=? AND state='queued'`, owner, expires.UnixNano(), id)
	if err != nil {
		*item = previous
		return Job{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		*item = previous
		return Job{}, false, err
	}
	if count != 1 {
		*item = previous
		return Job{}, false, nil
	}
	copy := *item
	return copy, true, nil
}

func (j *Jobs) encodeDurableItem(item *Job) ([]byte, any, any, any, error) {
	if item == nil || item.ID == "" {
		return nil, nil, nil, nil, errors.New("cannot persist invalid job")
	}
	sealedPayload, err := j.sealPayload(item)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("seal durable job %s: %w", item.ID, err)
	}
	encodedPayload, err := json.Marshal(sealedPayload)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("encode durable job payload %s: %w", item.ID, err)
	}
	data, err := json.Marshal(&persistedJob{Job: *item, Payload: encodedPayload})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("encode durable job %s: %w", item.ID, err)
	}
	if len(data) > maxJobStateBytes {
		return nil, nil, nil, nil, errors.New("durable job payload exceeds 16 MiB")
	}
	var finished, leaseExpires, nextAttempt any
	if item.FinishedAt != nil {
		finished = item.FinishedAt.UnixNano()
	}
	if item.LeaseExpires != nil {
		leaseExpires = item.LeaseExpires.UnixNano()
	}
	if item.NextAttempt != nil {
		nextAttempt = item.NextAttempt.UnixNano()
	}
	return data, finished, leaseExpires, nextAttempt, nil
}

func (j *Jobs) persistDurableItem(item *Job) error {
	data, finished, leaseExpires, nextAttempt, err := j.encodeDurableItem(item)
	if err != nil {
		return err
	}
	_, err = j.db.Exec(`INSERT INTO jobs (id, kind, operation_key, state, owner, started_at, finished_at, payload, lease_owner, lease_expires_at, next_attempt_at, cancel_requested, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch()) ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, operation_key=excluded.operation_key, state=excluded.state, owner=excluded.owner, started_at=excluded.started_at, finished_at=excluded.finished_at, payload=excluded.payload, lease_owner=excluded.lease_owner, lease_expires_at=excluded.lease_expires_at, next_attempt_at=excluded.next_attempt_at, cancel_requested=excluded.cancel_requested, updated_at=excluded.updated_at`, item.ID, item.Kind, item.OperationKey, item.State, item.User, item.StartedAt.UnixNano(), finished, data, nullString(item.LeaseOwner), leaseExpires, nextAttempt, boolInt(item.Cancel))
	return err
}

func (j *Jobs) persistDurableItemCAS(item *Job, expectedOwner string) error {
	data, finished, leaseExpires, nextAttempt, err := j.encodeDurableItem(item)
	if err != nil {
		return err
	}
	result, err := j.db.Exec(`UPDATE jobs SET kind=?, operation_key=?, state=?, owner=?, started_at=?, finished_at=?, payload=?, lease_owner=?, lease_expires_at=?, next_attempt_at=?, cancel_requested=?, updated_at=unixepoch() WHERE id=? AND state='running' AND lease_owner=?`, item.Kind, item.OperationKey, item.State, item.User, item.StartedAt.UnixNano(), finished, data, nullString(item.LeaseOwner), leaseExpires, nextAttempt, boolInt(item.Cancel), item.ID, expectedOwner)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errJobLeaseNotHeld
	}
	return nil
}

// Renew extends a worker lease. Expired leases cannot be renewed.
func (j *Jobs) Renew(id, owner string) (bool, error) {
	if j.db == nil || id == "" || owner == "" {
		return false, errors.New("durable job renewal requires a job ID and owner")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	item, ok := j.items[id]
	if !ok || item == nil || item.State != "running" || item.LeaseOwner != owner || item.LeaseExpires == nil || time.Now().UTC().After(*item.LeaseExpires) {
		return false, nil
	}
	previous := *item
	expires := time.Now().UTC().Add(j.leaseDuration())
	item.LeaseExpires = &expires
	if err := j.persistDurableItemCAS(item, owner); err != nil {
		*item = previous
		return false, err
	}
	return true, nil
}

// RequeueExpired returns abandoned running jobs to the queue.
func (j *Jobs) RequeueExpired() (int, error) {
	if j.db == nil {
		return 0, errors.New("durable job requeue requires the control-plane database")
	}
	now := time.Now().UTC()
	j.mu.Lock()
	defer j.mu.Unlock()
	changed := 0
	previous := make(map[*Job]Job)
	for _, item := range j.items {
		if item != nil && item.State == "running" && item.LeaseExpires != nil && now.After(*item.LeaseExpires) {
			previous[item] = *item
			item.State = "queued"
			item.LeaseOwner = ""
			item.LeaseExpires = nil
			changed++
		}
	}
	// Requeue against the database as well; another process may have claimed
	// jobs that this process did not load into its local map.
	result, err := j.db.Exec(`UPDATE jobs SET state='queued', lease_owner=NULL, lease_expires_at=NULL, updated_at=unixepoch() WHERE state='running' AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?`, now.UnixNano())
	if err != nil {
		for item, state := range previous {
			*item = state
		}
		return 0, err
	}
	if count, err := result.RowsAffected(); err == nil && int(count) > changed {
		changed = int(count)
	}
	return changed, nil
}

func (j *Jobs) finishClaim(id, owner string, apply func(*Job)) error {
	if j.db == nil || id == "" || owner == "" {
		return errors.New("durable job completion requires a job ID and owner")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	item, ok := j.items[id]
	if !ok || item == nil || item.State != "running" || item.LeaseOwner != owner {
		return errJobLeaseNotHeld
	}
	previous := *item
	apply(item)
	item.LeaseOwner = ""
	item.LeaseExpires = nil
	if err := j.persistDurableItemCAS(item, owner); err != nil {
		*item = previous
		return err
	}
	j.publish(*item)
	return nil
}

// Enqueue creates a durable job that can be claimed by an independent worker.
// Payload is opaque to the control plane and must contain only replay-safe
// operation input, never credentials or bearer tokens.
func (j *Jobs) Enqueue(kind, owner, operationKey string, payload []byte, maxAttempts int) (Job, error) {
	if j.db == nil || strings.TrimSpace(kind) == "" || strings.TrimSpace(owner) == "" {
		return Job{}, errors.New("durable enqueue requires a kind and owner")
	}
	if len(payload) > maxJobStateBytes {
		return Job{}, errors.New("job payload exceeds 16 MiB")
	}
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	id, err := newJobID(kind)
	if err != nil {
		return Job{}, err
	}
	item := &Job{ID: id, Kind: kind, OperationKey: operationKey, State: "queued", User: owner, Payload: append([]byte(nil), payload...), MaxAttempts: maxAttempts, StartedAt: time.Now().UTC()}
	if err := j.add(item); err != nil {
		return Job{}, err
	}
	return *item, nil
}

func (j *Jobs) EnqueueIdempotent(kind, owner, operationKey string, payload []byte, maxAttempts int) (Job, bool, error) {
	var existingID string
	if j.db == nil || strings.TrimSpace(operationKey) == "" {
		if j.db != nil && strings.TrimSpace(operationKey) == "" {
			if err := j.db.QueryRow(`SELECT id FROM jobs WHERE kind = ? AND owner = ? AND state IN ('queued', 'running') ORDER BY started_at LIMIT 1`, kind, owner).Scan(&existingID); err == nil {
				if item, ok := j.Get(existingID); ok {
					return item, true, nil
				}
				return Job{ID: existingID, Kind: kind, User: owner}, true, nil
			}
		}
		item, err := j.Enqueue(kind, owner, operationKey, payload, maxAttempts)
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
			if existingIDErr := j.db.QueryRow(`SELECT id FROM jobs WHERE kind = ? AND owner = ? AND operation_key = '' AND state IN ('queued', 'running') ORDER BY started_at LIMIT 1`, kind, owner).Scan(&existingID); existingIDErr == nil {
				if item, ok := j.Get(existingID); ok {
					return item, true, nil
				}
				return Job{ID: existingID, Kind: kind, User: owner}, true, nil
			}
		}
		return item, false, err
	}
	err := j.db.QueryRow(`SELECT id FROM jobs WHERE kind = ? AND owner = ? AND operation_key = ? LIMIT 1`, kind, owner, operationKey).Scan(&existingID)
	if err == nil {
		if item, ok := j.Get(existingID); ok {
			return item, true, nil
		}
		return Job{ID: existingID, Kind: kind, User: owner, OperationKey: operationKey}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, err
	}
	item, err := j.Enqueue(kind, owner, operationKey, payload, maxAttempts)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique") {
		if lookupErr := j.db.QueryRow(`SELECT id FROM jobs WHERE kind = ? AND owner = ? AND operation_key = ? LIMIT 1`, kind, owner, operationKey).Scan(&existingID); lookupErr == nil {
			return Job{ID: existingID, Kind: kind, User: owner, OperationKey: operationKey}, true, nil
		}
	}
	return item, false, err
}

// ClaimNext atomically claims the oldest eligible queued job directly in SQL.
// This avoids relying on a stale in-memory snapshot when multiple workers or
// hosts share the control-plane database.
func (j *Jobs) ClaimNext(owner string, kinds ...string) (Job, bool, error) {
	if j.db == nil || strings.TrimSpace(owner) == "" {
		return Job{}, false, errors.New("durable claim requires a worker owner")
	}
	tx, err := j.db.Begin()
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	nowNanos := time.Now().UTC().UnixNano()
	limit := j.workerLimit
	if limit < 1 {
		limit = 1
	}
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state = 'running' AND (lease_expires_at IS NULL OR lease_expires_at > ?)`, nowNanos).Scan(&active); err != nil {
		return Job{}, false, err
	}
	if active >= limit {
		return Job{}, false, nil
	}
	args := []any{nowNanos, nowNanos}
	filter := ""
	if len(kinds) > 0 {
		placeholders := make([]string, len(kinds))
		for i, kind := range kinds {
			placeholders[i] = "?"
			args = append(args, kind)
		}
		filter = " AND kind IN (" + strings.Join(placeholders, ",") + ")"
	}
	var id string
	var payload []byte
	err = tx.QueryRow(`SELECT id, payload FROM jobs AS candidate WHERE candidate.state = 'queued' AND (candidate.next_attempt_at IS NULL OR candidate.next_attempt_at <= ?) AND NOT EXISTS (SELECT 1 FROM jobs AS active WHERE active.owner = candidate.owner AND ((active.state = 'running' AND (active.lease_expires_at IS NULL OR active.lease_expires_at > ?)) OR (active.state = 'queued' AND (active.started_at < candidate.started_at OR (active.started_at = candidate.started_at AND active.id < candidate.id)))))`+filter+` ORDER BY candidate.started_at, candidate.id LIMIT 1`, args...).Scan(&id, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	expires := time.Now().UTC().Add(j.leaseDuration())
	result, err := tx.Exec(`UPDATE jobs SET state='running', lease_owner=?, lease_expires_at=?, updated_at=unixepoch() WHERE id=? AND state='queued'`, owner, expires.UnixNano(), id)
	if err != nil {
		return Job{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Job{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, err
	}
	var persisted persistedJob
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return Job{}, false, fmt.Errorf("decode claimed job: %w", err)
	}
	item := persisted.Job
	item.Payload = decodePersistedJobPayload(persisted.Payload)
	openedPayload, err := j.openPayload(&item, item.Payload)
	if err != nil {
		return Job{}, false, fmt.Errorf("open claimed job payload: %w", err)
	}
	item.Payload = openedPayload
	item.State = "running"
	item.LeaseOwner = owner
	item.LeaseExpires = &expires
	j.mu.Lock()
	j.items[id] = &item
	j.mu.Unlock()
	j.publish(item)
	return item, true, nil
}

// FailClaim records a worker failure and schedules bounded exponential retry.
// Jobs that exhaust MaxAttempts become dead-letter records for operator review.
func (j *Jobs) FailClaim(id, owner, message string) error {
	if j.db == nil || id == "" || owner == "" {
		return errors.New("durable failure requires a job ID and owner")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	item, ok := j.items[id]
	if !ok || item == nil || item.State != "running" || item.LeaseOwner != owner {
		return errors.New("job lease is not held by this worker")
	}
	previous := *item
	item.Attempts++
	item.Error = strings.TrimSpace(message)
	item.LeaseOwner = ""
	item.LeaseExpires = nil
	if item.MaxAttempts > 0 && item.Attempts >= item.MaxAttempts {
		item.State = "dead-letter"
		now := time.Now().UTC()
		item.FinishedAt = &now
	} else {
		item.State = "queued"
		seconds := 1 << min(item.Attempts, 8)
		next := time.Now().UTC().Add(time.Duration(seconds) * time.Second)
		item.NextAttempt = &next
	}
	if err := j.persistDurableItemCAS(item, owner); err != nil {
		*item = previous
		return err
	}
	j.publish(*item)
	return nil
}

// UpdateClaim persists worker progress without changing ownership.
func (j *Jobs) UpdateClaim(id, owner string, progress int) error {
	if j.db == nil || id == "" || owner == "" {
		return errors.New("durable progress requires a job ID and owner")
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	item, ok := j.items[id]
	if !ok || item == nil || item.State != "running" || item.LeaseOwner != owner {
		return errors.New("job lease is not held by this worker")
	}
	previous := *item
	item.Progress = progress
	if err := j.persistDurableItemCAS(item, owner); err != nil {
		*item = previous
		return err
	}
	j.publish(*item)
	return nil
}

// UpdateClaimPayload persists replay-safe workflow state while a durable job
// is running. This is used when a workflow has completed an expensive local
// phase and must retry only a later remote phase.
func (j *Jobs) UpdateClaimPayload(id, owner string, payload []byte) error {
	if j.db == nil || id == "" || owner == "" {
		return errors.New("durable payload update requires a job ID and owner")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	item, ok := j.items[id]
	if !ok || item == nil || item.State != "running" || item.LeaseOwner != owner {
		return errors.New("job lease is not held by this worker")
	}
	previous := append([]byte(nil), item.Payload...)
	item.Payload = append([]byte(nil), payload...)
	if err := j.persistDurableItemCAS(item, owner); err != nil {
		item.Payload = previous
		return err
	}
	j.publish(*item)
	return nil
}

func (j *Jobs) RequestCancel(id string) error {
	if j.db == nil || id == "" {
		return errors.New("durable cancellation requires a job ID")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	var state string
	if err := j.db.QueryRow(`SELECT state FROM jobs WHERE id = ?`, id).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("job is not cancellable")
		}
		return err
	}
	now := time.Now().UTC()
	switch state {
	case "queued":
		result, err := j.db.Exec(`UPDATE jobs SET state='cancelled', finished_at=?, cancel_requested=1, updated_at=unixepoch() WHERE id=? AND state='queued'`, now.UnixNano(), id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("job is no longer queued")
		}
		if item := j.items[id]; item != nil {
			item.State, item.Cancel, item.FinishedAt = "cancelled", true, &now
			j.publish(*item)
		}
		return nil
	case "running":
		result, err := j.db.Exec(`UPDATE jobs SET cancel_requested=1, updated_at=unixepoch() WHERE id=? AND state='running'`, id)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return errors.New("job is no longer running")
		}
		if item := j.items[id]; item != nil {
			item.Cancel = true
			j.publish(*item)
		}
		return nil
	default:
		return errors.New("job is not cancellable")
	}
}

// RequeueDeadLetter moves one operator-reviewed dead-letter job back to the
// durable queue. Attempts are reset because the operator is asserting that
// the original failure condition has been addressed.
func (j *Jobs) RequeueDeadLetter(id string) error {
	if j.db == nil || strings.TrimSpace(id) == "" {
		return errors.New("dead-letter retry requires a job ID")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	var raw []byte
	if err := j.db.QueryRow(`SELECT payload FROM jobs WHERE id=? AND state='dead-letter'`, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("job is not a dead-letter record")
		}
		return err
	}
	var persisted persistedJob
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return fmt.Errorf("decode dead-letter job: %w", err)
	}
	persisted.Job.Payload = decodePersistedJobPayload(persisted.Payload)
	opened, err := j.openPayload(&persisted.Job, persisted.Job.Payload)
	if err != nil {
		return fmt.Errorf("open dead-letter job payload: %w", err)
	}
	persisted.Job.Payload = opened
	item := &persisted.Job
	if item.State != "dead-letter" {
		return errors.New("job is not a dead-letter record")
	}
	item.State = "queued"
	item.Error = ""
	item.FinishedAt = nil
	item.NextAttempt = nil
	item.LeaseOwner = ""
	item.LeaseExpires = nil
	item.Cancel = false
	item.Attempts = 0
	item.Progress = 0
	data, finished, _, _, err := j.encodeDurableItem(item)
	if err != nil {
		return err
	}
	result, err := j.db.Exec(`UPDATE jobs SET kind=?, operation_key=?, state=?, owner=?, started_at=?, finished_at=?, payload=?, lease_owner=NULL, lease_expires_at=NULL, next_attempt_at=NULL, cancel_requested=0, updated_at=unixepoch() WHERE id=? AND state='dead-letter'`, item.Kind, item.OperationKey, item.State, item.User, item.StartedAt.UnixNano(), finished, data, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("job is no longer a dead-letter record")
	}
	j.items[id] = item
	return nil
}

// CancellationRequested lets a worker handler stop at a safe checkpoint.
func (j *Jobs) CancellationRequested(id string) bool {
	if j.db != nil {
		var requested int
		if err := j.db.QueryRow(`SELECT cancel_requested FROM jobs WHERE id = ?`, id).Scan(&requested); err == nil {
			return requested != 0
		}
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	item, ok := j.items[id]
	return ok && item != nil && item.Cancel
}

type DurableJobHandler func(context.Context, Job) ([]byte, error)

type JobQueueStats struct {
	Queued     int
	Running    int
	DeadLetter int
}

func (j *Jobs) QueueStats() (JobQueueStats, error) {
	if j.db == nil {
		return JobQueueStats{}, errors.New("queue statistics require the control-plane database")
	}
	var stats JobQueueStats
	rows, err := j.db.Query(`SELECT state, COUNT(*) FROM jobs GROUP BY state`)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return stats, err
		}
		switch state {
		case "queued":
			stats.Queued = count
		case "running":
			stats.Running = count
		case "dead-letter":
			stats.DeadLetter = count
		}
	}
	return stats, rows.Err()
}

// LightweightReadiness performs the cheap query used by frequent readiness
// probes. Full SQLite integrity verification runs in the maintenance loop and
// is exposed through IntegrityStatus instead of contending with every probe.
func (j *Jobs) LightweightReadiness() error {
	if j == nil || j.db == nil {
		return errors.New("control-plane database is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var result int
	if err := j.db.QueryRowContext(ctx, `SELECT 1`).Scan(&result); err != nil {
		return err
	}
	if result != 1 {
		return fmt.Errorf("control-plane readiness query returned %d", result)
	}
	return nil
}

// VerifyIntegrity runs the expensive structural check outside the readiness
// request path and records its latest result for operators.
func (j *Jobs) VerifyIntegrity(ctx context.Context) error {
	if j == nil || j.db == nil {
		return errors.New("control-plane database is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result string
	err := j.db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result)
	if err == nil && strings.TrimSpace(strings.ToLower(result)) != "ok" {
		err = fmt.Errorf("SQLite quick_check returned %q", result)
	}
	j.integrityMu.Lock()
	j.integrityAt = time.Now().UTC()
	j.integrityErr = err
	j.integrityMu.Unlock()
	return err
}

// IntegrityCheck is retained for explicit operator/doctor checks. Readiness
// probes use LightweightReadiness instead.
func (j *Jobs) IntegrityCheck() error {
	return j.VerifyIntegrity(context.Background())
}

func (j *Jobs) IntegrityStatus() (checkedAt time.Time, err error, checked bool) {
	if j == nil {
		return time.Time{}, errors.New("job store is unavailable"), false
	}
	j.integrityMu.RLock()
	defer j.integrityMu.RUnlock()
	if j.integrityAt.IsZero() {
		return time.Time{}, nil, false
	}
	return j.integrityAt, j.integrityErr, true
}

// RunWorker consumes the durable queue until ctx is cancelled. It is kept
// independent of HTTP and platform helpers so it can run in the panel process
// during the single-host phase or in a separately supervised worker later.
func (j *Jobs) RunWorker(ctx context.Context, owner string, kinds []string, poll time.Duration, handler DurableJobHandler) error {
	if j.db == nil || strings.TrimSpace(owner) == "" || handler == nil {
		return errors.New("durable worker requires a database, owner, and handler")
	}
	if poll <= 0 {
		poll = time.Second
	}
	if _, err := j.RequeueExpired(); err != nil {
		return err
	}
	startedAt := time.Now().UTC()
	heartbeat := &workerHeartbeatState{currentJobs: make(map[string]bool)}
	currentJobs := func() []string {
		heartbeat.mu.RLock()
		defer heartbeat.mu.RUnlock()
		result := make([]string, 0, len(heartbeat.currentJobs))
		for jobID := range heartbeat.currentJobs {
			result = append(result, jobID)
		}
		sort.Strings(result)
		return result
	}
	hostID := workerHostID()
	if err := j.publishWorkerHeartbeat(owner, hostID, os.Getpid(), startedAt, kinds, currentJobs()); err != nil {
		return fmt.Errorf("publish worker heartbeat: %w", err)
	}
	heartbeatStop := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(workerHeartbeatFreshness / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := j.publishWorkerHeartbeat(owner, hostID, os.Getpid(), startedAt, kinds, currentJobs()); err != nil {
					log.Printf("publish worker heartbeat %s: %v", owner, err)
				}
			case <-heartbeatStop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() {
		close(heartbeatStop)
		<-heartbeatDone
		if err := j.removeWorkerHeartbeat(owner); err != nil {
			log.Printf("remove worker heartbeat %s: %v", owner, err)
		}
	}()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	requeueTicker := time.NewTicker(j.leaseDuration() / 3)
	defer requeueTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-requeueTicker.C:
			if _, err := j.RequeueExpired(); err != nil {
				return err
			}
		default:
		}
		item, claimed, err := j.ClaimNext(owner, kinds...)
		if err != nil {
			return err
		}
		if claimed {
			heartbeat.mu.Lock()
			heartbeat.currentJobs[item.ID] = true
			heartbeat.mu.Unlock()
			if j.CancellationRequested(item.ID) {
				if err := j.finishClaim(item.ID, owner, func(job *Job) {
					job.State = "cancelled"
					now := time.Now().UTC()
					job.FinishedAt = &now
				}); err != nil {
					heartbeat.mu.Lock()
					delete(heartbeat.currentJobs, item.ID)
					heartbeat.mu.Unlock()
					return err
				}
				heartbeat.mu.Lock()
				delete(heartbeat.currentJobs, item.ID)
				heartbeat.mu.Unlock()
				continue
			}
			var output []byte
			output, err = func() (runOutput []byte, runErr error) {
				handlerCtx, stopLease := j.maintainLeaseContext(ctx, &item)
				defer stopLease()
				defer func() {
					if recovered := recover(); recovered != nil {
						runErr = fmt.Errorf("worker panic: %v\n%s", recovered, debug.Stack())
					}
				}()
				return handler(handlerCtx, item)
			}()
			if err != nil {
				if failErr := j.FailClaim(item.ID, owner, err.Error()); failErr != nil {
					if errors.Is(failErr, errJobLeaseNotHeld) {
						// A replacement worker owns the claim now. It is responsible
						// for completing or retrying the durable operation.
						continue
					}
					return failErr
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
			} else if err := j.finishClaim(item.ID, owner, func(job *Job) {
				job.State = "completed"
				job.Progress = 100
				job.Output = append([]byte(nil), output...)
				now := time.Now().UTC()
				job.FinishedAt = &now
			}); err != nil {
				heartbeat.mu.Lock()
				delete(heartbeat.currentJobs, item.ID)
				heartbeat.mu.Unlock()
				return err
			}
			heartbeat.mu.Lock()
			delete(heartbeat.currentJobs, item.ID)
			heartbeat.mu.Unlock()
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunWorkerPool runs the durable worker with the configured number of local
// consumers. Each consumer has its own lease owner and heartbeat, while
// ClaimNext provides the cross-process atomic queue admission.
func (j *Jobs) RunWorkerPool(ctx context.Context, owner string, kinds []string, poll time.Duration, concurrency int, handler DurableJobHandler) error {
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency == 1 {
		return j.RunWorker(ctx, owner, kinds, poll, handler)
	}
	poolCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, concurrency)
	var workers sync.WaitGroup
	for index := 0; index < concurrency; index++ {
		workerOwner := fmt.Sprintf("%s-%d", owner, index)
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := j.RunWorker(poolCtx, workerOwner, kinds, poll, handler)
			if err != nil && !errors.Is(err, context.Canceled) {
				cancel()
			}
			errs <- err
		}()
	}
	workers.Wait()
	close(errs)
	if err := ctx.Err(); err != nil {
		return err
	}
	for err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return context.Canceled
}

// maintainLease keeps a local long-running execution owned until its callback
// finishes. A worker that loses the lease is logged and must not silently
// assume another worker will preserve its side effects.
func (j *Jobs) maintainLease(item *Job) func() {
	if j.db == nil || item == nil || item.LeaseOwner == "" {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	interval := j.leaseDuration() / 3
	go func(owner, id string) {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ok, err := j.Renew(id, owner)
				if err != nil {
					log.Printf("renew job lease %s: %v", id, err)
				} else if !ok {
					log.Printf("job lease %s is no longer owned by local worker", id)
					return
				}
			case <-stop:
				return
			}
		}
	}(item.LeaseOwner, item.ID)
	return func() {
		close(stop)
		<-done
	}
}

// maintainLeaseContext renews a durable worker lease and cancels the handler
// context as soon as renewal fails or ownership is lost. Continuing a handler
// after that boundary could let two workers perform the same side effect.
func (j *Jobs) maintainLeaseContext(parent context.Context, item *Job) (context.Context, func()) {
	if j.db == nil || item == nil || item.LeaseOwner == "" {
		return parent, func() {}
	}
	ctx, cancel := context.WithCancel(parent)
	stop := make(chan struct{})
	done := make(chan struct{})
	interval := j.leaseDuration() / 3
	go func(owner, id string) {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ok, err := j.Renew(id, owner)
				if err != nil {
					log.Printf("renew job lease %s: %v", id, err)
					cancel()
					return
				}
				if !ok {
					log.Printf("job lease %s is no longer owned by local worker", id)
					cancel()
					return
				}
			case <-stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}(item.LeaseOwner, item.ID)
	return ctx, func() {
		close(stop)
		<-done
		cancel()
	}
}

func (j *Jobs) SubmitCloud(id, target string, work func() (CloudActionResult, error)) error {
	j.admission.RLock()
	defer j.admission.RUnlock()
	if !j.reserve(target) {
		return ErrJobBusy
	}
	j.wg.Add(1)
	item := &Job{ID: id, Kind: "cloud.action", State: "running", User: target, StartedAt: time.Now().UTC()}
	if err := j.add(item); err != nil {
		j.wg.Done()
		j.release(target)
		return fmt.Errorf("persist queued job: %w", err)
	}
	go func() {
		defer j.wg.Done()
		defer j.release(target)
		stopLease := j.maintainLease(item)
		defer stopLease()
		result, err := work()
		now := time.Now().UTC()
		j.mu.Lock()
		item.FinishedAt = &now
		if err != nil {
			item.State = "failed"
			item.Error = err.Error()
		} else {
			item.State = "completed"
			item.Cloud = &result
		}
		j.mu.Unlock()
		j.complete(item)
	}()
	return nil
}

type jobStore struct {
	Version int    `json:"version"`
	Jobs    []*Job `json:"jobs"`
}

// decodeJobStore accepts both the versioned object written by current
// releases and the top-level array written by older releases. Keeping this
// compatibility at the file boundary lets upgrades preserve queued and
// completed work instead of treating a valid legacy file as corrupt state.
func decodeJobStore(data []byte) (jobStore, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return jobStore{}, errors.New("job state is empty")
	}
	if trimmed[0] == '[' {
		var jobs []*Job
		if err := json.Unmarshal(trimmed, &jobs); err != nil {
			return jobStore{}, err
		}
		return jobStore{Version: 1, Jobs: jobs}, nil
	}
	var store jobStore
	if err := json.Unmarshal(trimmed, &store); err != nil {
		return jobStore{}, err
	}
	return store, nil
}

type Jobs struct {
	mu            sync.RWMutex
	items         map[string]*Job
	admissionGate *jobadmission.Admission
	activeDomains map[string]bool
	activeTargets map[string]bool
	wg            sync.WaitGroup
	admission     sync.RWMutex
	path          string
	db            *sql.DB
	ownedDB       *sql.DB
	persistErr    error
	payloadKey    []byte // pre-v2 key: SHA-256 of the account key
	payloadBox    *secretbox.Box
	// legacyPayloads permits reading payloads sealed without binding to
	// their job (or stored unsealed) until the store has been migrated;
	// legacyPayloadsSeen records that one was read.
	legacyPayloads     atomic.Bool
	legacyPayloadsSeen atomic.Bool
	leaseTTL           time.Duration
	workerLimit        int
	subscriberMu       sync.RWMutex
	subscribers        map[chan JobEvent]struct{}
	// stateErrors receives categorized persistence failures for metrics and
	// operator visibility; nil in tests that do not observe them.
	stateErrors  func(state.StateError)
	integrityMu  sync.RWMutex
	integrityAt  time.Time
	integrityErr error
}

// SetStateErrorObserver reports durable persistence failures to fn.
func (j *Jobs) SetStateErrorObserver(fn func(state.StateError)) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.stateErrors = fn
}

// observePersistFailureLocked records a failed durable write. Callers hold j.mu.
func (j *Jobs) observePersistFailureLocked(operation string, err error) {
	j.persistErr = err
	if j.stateErrors != nil {
		j.stateErrors(state.Classify(operation, err, "durable job state"))
	}
}

func NewJobs() *Jobs { return newJobs("") }

func OpenJobs(path string, limits ...int) (*Jobs, error) {
	limit := 2
	if len(limits) > 0 && limits[0] > 0 {
		limit = limits[0]
	}
	jobs := newJobs(path, limit)
	if err := jobs.load(); err != nil {
		return nil, err
	}
	return jobs, nil
}

// OpenDurableJobs opens the relational job store used by production. A legacy
// JSON state file is imported once when the database has no jobs, which keeps
// upgrades recoverable without retaining JSON as the live source of truth.
func OpenDurableJobs(databasePath, legacyPath string, limits ...int) (*Jobs, error) {
	db, err := openControlPlaneDB(databasePath)
	if err != nil {
		return nil, err
	}
	j, err := openDurableJobsDB(db, legacyPath, limits...)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	j.ownedDB = db
	return j, nil
}

func openDurableJobsDB(db *sql.DB, legacyPath string, limits ...int) (*Jobs, error) {
	return openDurableJobsDBWithKey(db, legacyPath, "", limits...)
}

func openDurableJobsDBWithKey(db *sql.DB, legacyPath, payloadKey string, limits ...int) (*Jobs, error) {
	j := newJobsWithDB(db, limits...)
	if strings.TrimSpace(payloadKey) != "" {
		j.payloadKey = jobPayloadKey(payloadKey)
		box, err := secretbox.New([]byte(payloadKey), "job-payload")
		if err != nil {
			return nil, err
		}
		j.payloadBox = box
	}
	legacyAllowed, err := legacyCiphertextAllowed(db, encryptionStoreJobPayloads)
	if err != nil {
		return nil, err
	}
	j.legacyPayloads.Store(legacyAllowed)
	if err := j.load(); err != nil {
		return nil, err
	}
	if len(j.items) == 0 && legacyPath != "" {
		legacy, legacyErr := OpenJobs(legacyPath, limits...)
		if legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
			return nil, fmt.Errorf("load legacy job state: %w", legacyErr)
		}
		if legacyErr == nil && len(legacy.items) > 0 {
			for id, item := range legacy.items {
				j.items[id] = item
			}
			if err := j.persistLocked(); err != nil {
				return nil, fmt.Errorf("migrate legacy job state: %w", err)
			}
		}
	}
	if j.payloadBox != nil && j.legacyPayloads.Load() {
		// Re-seal every payload bound to its job, then refuse the legacy
		// formats from now on.
		if j.legacyPayloadsSeen.Load() {
			if err := j.resealLegacyPayloads(); err != nil {
				return nil, fmt.Errorf("re-seal job payloads: %w", err)
			}
		}
		if err := markContextBoundEncryption(db, encryptionStoreJobPayloads); err != nil {
			return nil, err
		}
		j.legacyPayloads.Store(false)
		j.legacyPayloadsSeen.Store(false)
	}
	return j, nil
}

func jobPayloadKey(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

// resealLegacyPayloads rewrites every legacy payload in the context-bound
// format. Each row is updated only if it is unchanged since it was read, so
// a concurrent claim or state change by another process is never reverted;
// a row that changed is read again on the next pass.
func (j *Jobs) resealLegacyPayloads() error {
	for pass := 0; pass < 5; pass++ {
		rows, err := j.db.Query(`SELECT id, payload FROM jobs`)
		if err != nil {
			return err
		}
		type pending struct {
			id  string
			raw []byte
		}
		var legacy []pending
		for rows.Next() {
			var item pending
			if err := rows.Scan(&item.id, &item.raw); err != nil {
				_ = rows.Close()
				return err
			}
			var persisted persistedJob
			if err := json.Unmarshal(item.raw, &persisted); err != nil {
				_ = rows.Close()
				return fmt.Errorf("decode job %s: %w", item.id, err)
			}
			payload := decodePersistedJobPayload(persisted.Payload)
			if len(payload) > 0 && !secretbox.IsSealed(payload) {
				legacy = append(legacy, item)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if len(legacy) == 0 {
			return nil
		}
		for _, item := range legacy {
			var persisted persistedJob
			if err := json.Unmarshal(item.raw, &persisted); err != nil {
				return fmt.Errorf("decode job %s: %w", item.id, err)
			}
			if persisted.Job.ID != item.id {
				return fmt.Errorf("job row %s holds the record of job %s", item.id, persisted.Job.ID)
			}
			opened, err := j.openPayload(&persisted.Job, decodePersistedJobPayload(persisted.Payload))
			if err != nil {
				return fmt.Errorf("open legacy payload of job %s: %w", item.id, err)
			}
			persisted.Job.Payload = opened
			sealed, err := j.sealPayload(&persisted.Job)
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(sealed)
			if err != nil {
				return err
			}
			data, err := json.Marshal(&persistedJob{Job: persisted.Job, Payload: encoded})
			if err != nil {
				return err
			}
			if _, err := j.db.Exec(`UPDATE jobs SET payload = ? WHERE id = ? AND payload = ?`, data, item.id, item.raw); err != nil {
				return fmt.Errorf("re-seal job %s: %w", item.id, err)
			}
		}
	}
	return errors.New("job payloads kept changing during re-sealing")
}

// sealPayload seals a job payload bound to the job's ID, kind and owner, so
// a payload cannot be moved to another job (for example to replay a restore
// request against a different site).
func (j *Jobs) sealPayload(item *Job) ([]byte, error) {
	if j.payloadBox == nil || len(item.Payload) == 0 {
		return append([]byte(nil), item.Payload...), nil
	}
	return j.payloadBox.Seal(item.Payload, item.ID, item.Kind, item.User)
}

// openPayload opens a persisted payload for item. Payloads sealed without
// context binding (SPJ1) or stored unsealed while a key is configured are
// accepted only until the store has been migrated.
func (j *Jobs) openPayload(item *Job, payload []byte) ([]byte, error) {
	switch {
	case secretbox.IsSealed(payload):
		if j.payloadBox == nil {
			return nil, errors.New("encrypted job payload requires STEPANEL_ACCOUNT_KEY")
		}
		return j.payloadBox.Open(payload, item.ID, item.Kind, item.User)
	case bytes.HasPrefix(payload, encryptedJobPayloadPrefix):
		if len(j.payloadKey) == 0 {
			return nil, errors.New("encrypted job payload requires STEPANEL_ACCOUNT_KEY")
		}
		if !j.legacyPayloads.Load() {
			return nil, errors.New("job payload is not bound to its job; legacy ciphertexts are no longer accepted")
		}
		opened, err := secretbox.OpenLegacyWithKey(j.payloadKey, payload[len(encryptedJobPayloadPrefix):])
		if err != nil {
			return nil, err
		}
		j.legacyPayloadsSeen.Store(true)
		return opened, nil
	case j.payloadBox == nil || len(payload) == 0:
		return payload, nil
	case j.legacyPayloads.Load():
		// Written before payload encryption was enabled.
		j.legacyPayloadsSeen.Store(true)
		return payload, nil
	default:
		return nil, errors.New("unsealed job payload refused while payload encryption is enabled")
	}
}

func (j *Jobs) Close() error {
	if j.ownedDB == nil {
		return nil
	}
	err := j.ownedDB.Close()
	j.ownedDB = nil
	return err
}

func (j *Jobs) PayloadEncryptionEnabled() bool { return len(j.payloadKey) > 0 }

func newJobs(path string, limits ...int) *Jobs {
	limit := 2
	if len(limits) > 0 && limits[0] > 0 {
		limit = limits[0]
	}
	return &Jobs{
		items:         make(map[string]*Job),
		admissionGate: jobadmission.NewAdmission(limit),
		activeDomains: make(map[string]bool),
		activeTargets: make(map[string]bool),
		path:          path,
		leaseTTL:      jobLeaseDuration,
		workerLimit:   limit,
		subscribers:   make(map[chan JobEvent]struct{}),
	}
}

// Subscribe returns a bounded best-effort stream of job changes. A slow
// browser is refreshed from the durable snapshot by the client rather than
// blocking workers or other subscribers.
func (j *Jobs) Subscribe() (<-chan JobEvent, func()) {
	ch := make(chan JobEvent, 32)
	j.subscriberMu.Lock()
	j.subscribers[ch] = struct{}{}
	j.subscriberMu.Unlock()
	return ch, func() {
		j.subscriberMu.Lock()
		if _, ok := j.subscribers[ch]; ok {
			delete(j.subscribers, ch)
			close(ch)
		}
		j.subscriberMu.Unlock()
	}
}

func (j *Jobs) publish(item Job) {
	item.Payload = nil
	item.Output = nil
	item.Result = nil
	item.WPress = nil
	item.Certificate = nil
	item.Backup = nil
	item.Restore = nil
	item.Cloud = nil
	item.LeaseOwner = ""
	item.LeaseExpires = nil
	event := JobEvent{Job: item}
	j.subscriberMu.RLock()
	defer j.subscriberMu.RUnlock()
	for ch := range j.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func (j *Jobs) leaseDuration() time.Duration {
	if j.leaseTTL > 0 {
		return j.leaseTTL
	}
	return jobLeaseDuration
}

func newJobsWithDB(db *sql.DB, limits ...int) *Jobs {
	j := newJobs("", limits...)
	j.db = db
	return j
}

func (j *Jobs) load() error {
	if j.db != nil {
		rows, err := j.db.Query(`SELECT state, payload, lease_owner, lease_expires_at, next_attempt_at, cancel_requested FROM jobs ORDER BY started_at, id`)
		if err != nil {
			return fmt.Errorf("read durable job state: %w", err)
		}
		defer rows.Close()
		now := time.Now().UTC()
		reconciled := false
		for rows.Next() {
			var state string
			var data []byte
			var leaseOwner sql.NullString
			var leaseExpires sql.NullInt64
			var nextAttempt sql.NullInt64
			var cancelRequested int
			if err := rows.Scan(&state, &data, &leaseOwner, &leaseExpires, &nextAttempt, &cancelRequested); err != nil {
				return fmt.Errorf("read durable job row: %w", err)
			}
			if len(data) > maxJobStateBytes {
				return errors.New("durable job payload exceeds 16 MiB")
			}
			var persisted persistedJob
			if err := json.Unmarshal(data, &persisted); err != nil || persisted.ID == "" {
				return errors.New("durable job state contains an invalid job")
			}
			item := persisted.Job
			if state != "queued" && state != "running" && state != "completed" && state != "failed" && state != "cancelled" && state != "dead-letter" {
				return fmt.Errorf("durable job state contains invalid SQL state %q", state)
			}
			item.State = state
			item.Payload = decodePersistedJobPayload(persisted.Payload)
			openedPayload, err := j.openPayload(&item, item.Payload)
			if err != nil {
				return fmt.Errorf("open durable job payload: %w", err)
			}
			item.Payload = openedPayload
			if _, exists := j.items[item.ID]; exists {
				return fmt.Errorf("durable job state contains duplicate ID %q", item.ID)
			}
			if leaseOwner.Valid {
				item.LeaseOwner = leaseOwner.String
			}
			if leaseExpires.Valid {
				expires := time.Unix(0, leaseExpires.Int64).UTC()
				item.LeaseExpires = &expires
			}
			if nextAttempt.Valid {
				next := time.Unix(0, nextAttempt.Int64).UTC()
				item.NextAttempt = &next
			}
			item.Cancel = cancelRequested != 0 || item.Cancel
			if item.State == "running" && item.LeaseOwner == "" {
				item.State = "failed"
				item.Error = "interrupted by an unclean shutdown; verify restore rollback and destination integrity"
				item.FinishedAt = &now
				reconciled = true
			}
			copy := item
			j.items[item.ID] = &copy
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate durable job state: %w", err)
		}
		if reconciled {
			if err := j.persistLocked(); err != nil {
				return fmt.Errorf("persist reconciled durable job state: %w", err)
			}
		}
		return nil
	}
	if j.path == "" {
		return nil
	}
	data, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read job state: %w", err)
	}
	if len(data) > maxJobStateBytes {
		return errors.New("job state exceeds 16 MiB")
	}
	legacyFormat := func() bool {
		trimmed := bytes.TrimSpace(data)
		return len(trimmed) > 0 && trimmed[0] == '['
	}()
	store, err := decodeJobStore(data)
	if err != nil {
		return fmt.Errorf("decode job state: %w", err)
	}
	if store.Version != 1 {
		return fmt.Errorf("unsupported job state version %d", store.Version)
	}
	now := time.Now().UTC()
	reconciled := false
	for _, item := range store.Jobs {
		if item == nil || item.ID == "" {
			return errors.New("job state contains an invalid job")
		}
		if _, exists := j.items[item.ID]; exists {
			return fmt.Errorf("job state contains duplicate ID %q", item.ID)
		}
		if item.State == "running" {
			item.State = "failed"
			item.Error = "interrupted by an unclean shutdown; verify restore rollback and destination integrity"
			item.FinishedAt = &now
			reconciled = true
		}
		j.items[item.ID] = item
	}
	if legacyFormat || reconciled {
		if err := j.persistLocked(); err != nil {
			return fmt.Errorf("persist migrated job state: %w", err)
		}
	}
	return nil
}

func (j *Jobs) persistLocked() error {
	if j.db != nil {
		tx, err := j.db.Begin()
		if err != nil {
			return fmt.Errorf("begin durable job transaction: %w", err)
		}
		rollback := func(err error) error {
			_ = tx.Rollback()
			return err
		}
		for _, item := range j.items {
			if item == nil || item.ID == "" {
				return rollback(errors.New("cannot persist invalid job"))
			}
			sealedPayload, err := j.sealPayload(item)
			if err != nil {
				return rollback(fmt.Errorf("seal durable job %s: %w", item.ID, err))
			}
			encodedPayload, err := json.Marshal(sealedPayload)
			if err != nil {
				return rollback(fmt.Errorf("encode durable job payload %s: %w", item.ID, err))
			}
			data, err := json.Marshal(&persistedJob{Job: *item, Payload: encodedPayload})
			if err != nil {
				return rollback(fmt.Errorf("encode durable job %s: %w", item.ID, err))
			}
			if len(data) > maxJobStateBytes {
				return rollback(errors.New("durable job payload exceeds 16 MiB"))
			}
			var finished any
			if item.FinishedAt != nil {
				finished = item.FinishedAt.UnixNano()
			}
			var leaseExpires any
			if item.LeaseExpires != nil {
				leaseExpires = item.LeaseExpires.UnixNano()
			}
			var nextAttempt any
			if item.NextAttempt != nil {
				nextAttempt = item.NextAttempt.UnixNano()
			}
			if _, err := tx.Exec(`INSERT INTO jobs (id, kind, operation_key, state, owner, started_at, finished_at, payload, lease_owner, lease_expires_at, next_attempt_at, cancel_requested, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch()) ON CONFLICT(id) DO UPDATE SET kind=excluded.kind, operation_key=excluded.operation_key, state=excluded.state, owner=excluded.owner, started_at=excluded.started_at, finished_at=excluded.finished_at, payload=excluded.payload, lease_owner=excluded.lease_owner, lease_expires_at=excluded.lease_expires_at, next_attempt_at=excluded.next_attempt_at, cancel_requested=excluded.cancel_requested, updated_at=excluded.updated_at`, item.ID, item.Kind, item.OperationKey, item.State, item.User, item.StartedAt.UnixNano(), finished, data, nullString(item.LeaseOwner), leaseExpires, nextAttempt, boolInt(item.Cancel)); err != nil {
				return rollback(fmt.Errorf("write durable job %s: %w", item.ID, err))
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit durable job state: %w", err)
		}
		return nil
	}
	if j.path == "" {
		return nil
	}
	root := filepath.Dir(j.path)
	if err := os.MkdirAll(root, 0750); err != nil {
		return fmt.Errorf("create job state directory: %w", err)
	}
	ids := make([]string, 0, len(j.items))
	for id := range j.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	store := jobStore{Version: 1, Jobs: make([]*Job, 0, len(ids))}
	for _, id := range ids {
		store.Jobs = append(store.Jobs, j.items[id])
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("encode job state: %w", err)
	}
	if len(data)+1 > maxJobStateBytes {
		return errors.New("job state exceeds 16 MiB; prune completed jobs before retrying")
	}
	tmp, err := os.CreateTemp(root, ".jobs-*.tmp")
	if err != nil {
		return fmt.Errorf("create job state: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	// Set restrictive permissions immediately to protect job state
	if err = tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure job state file permissions: %w", err)
	}
	// Write job data with proper permissions guaranteed
	payload := append(data, '\n')
	if written, writeErr := tmp.Write(payload); writeErr != nil {
		tmp.Close()
		return fmt.Errorf("write job state: %w", writeErr)
	} else if written != len(payload) {
		tmp.Close()
		return fmt.Errorf("write job state: %w", io.ErrShortWrite)
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync job state: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close job state: %w", err)
	}
	if err := os.Rename(tmpName, j.path); err != nil {
		return fmt.Errorf("replace job state: %w", err)
	}
	dir, err := os.Open(root)
	if err != nil {
		return fmt.Errorf("open job state directory for sync: %w", err)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return fmt.Errorf("sync job state directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close job state directory: %w", closeErr)
	}
	return nil
}

func (j *Jobs) add(item *Job) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, exists := j.items[item.ID]; exists {
		return fmt.Errorf("job ID %q already exists", item.ID)
	}
	if j.db != nil && item.State == "running" {
		owner, err := randomSecret()
		if err != nil {
			return fmt.Errorf("create job lease owner: %w", err)
		}
		item.LeaseOwner = "local-" + owner
		expires := time.Now().UTC().Add(j.leaseDuration())
		item.LeaseExpires = &expires
	}
	j.items[item.ID] = item
	var err error
	if j.db != nil {
		err = j.persistDurableItem(item)
	} else {
		err = j.persistLocked()
	}
	if err != nil {
		j.observePersistFailureLocked("persist_job_admission", err)
		delete(j.items, item.ID)
		return err
	}
	j.persistErr = nil
	j.publish(*item)
	return nil
}

func (j *Jobs) complete(item *Job) {
	j.mu.Lock()
	defer j.mu.Unlock()
	previousLeaseOwner := item.LeaseOwner
	previousLeaseExpires := item.LeaseExpires
	item.LeaseOwner = ""
	item.LeaseExpires = nil
	var err error
	if j.db != nil {
		err = j.persistDurableItem(item)
	} else {
		err = j.persistLocked()
	}
	if err != nil {
		j.observePersistFailureLocked("persist_job_completion", err)
		// Do not expose a completed result that was not durably recorded. The
		// durable row/file is still active (or its final state is uncertain),
		// so keep the in-memory view active as well; restart/requeue remains
		// honest and the readiness check exposes the persistence failure.
		item.State = "running"
		item.FinishedAt = nil
		item.LeaseOwner = previousLeaseOwner
		item.LeaseExpires = previousLeaseExpires
		item.Output = nil
		item.Result = nil
		item.Backup = nil
		item.Restore = nil
		item.WPress = nil
		persistenceError := fmt.Sprintf("completion state could not be persisted: %v", err)
		if item.Error != "" {
			item.Error += "; " + persistenceError
		} else {
			item.Error = persistenceError
		}
		log.Printf("persist completed job %s: %v", item.ID, err)
		j.publish(*item)
	} else {
		j.persistErr = nil
		j.publish(*item)
	}
}

func (j *Jobs) PersistenceError() error {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.persistErr
}

func (j *Jobs) SubmitWPress(id, user string, work func() (WPressResult, error)) error {
	j.admission.RLock()
	defer j.admission.RUnlock()
	if !j.reserve(user) {
		return ErrJobBusy
	}
	j.wg.Add(1)
	item := &Job{ID: id, Kind: "wordpress.restore", State: "running", User: user, StartedAt: time.Now().UTC()}
	if err := j.add(item); err != nil {
		j.wg.Done()
		j.release(user)
		return fmt.Errorf("persist queued job: %w", err)
	}
	go func() {
		defer j.wg.Done()
		defer j.release(user)
		stopLease := j.maintainLease(item)
		defer stopLease()
		result, err := work()
		now := time.Now().UTC()
		j.mu.Lock()
		item.FinishedAt = &now
		if err != nil {
			item.State = "failed"
			item.Error = err.Error()
		} else {
			item.State = "completed"
			item.WPress = &result
		}
		j.mu.Unlock()
		j.complete(item)
	}()
	return nil
}

func (j *Jobs) loadDurableJob(id string) (Job, bool, error) {
	if j.db == nil {
		return Job{}, false, nil
	}
	var state string
	var data []byte
	var leaseOwner sql.NullString
	var leaseExpires, nextAttempt sql.NullInt64
	var cancelRequested int
	err := j.db.QueryRow(`SELECT `+durableJobColumns+` FROM jobs WHERE id = ?`, id).Scan(&state, &data, &leaseOwner, &leaseExpires, &nextAttempt, &cancelRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	item, err := j.decodeDurableJob(state, data, leaseOwner, leaseExpires, nextAttempt, cancelRequested)
	if err != nil {
		return Job{}, false, err
	}
	return item, true, nil
}

// durableJobColumns are the columns decodeDurableJob consumes, in order.
const durableJobColumns = `state, payload, lease_owner, lease_expires_at, next_attempt_at, cancel_requested`

func (j *Jobs) decodeDurableJob(state string, data []byte, leaseOwner sql.NullString, leaseExpires, nextAttempt sql.NullInt64, cancelRequested int) (Job, error) {
	var persisted persistedJob
	if err := json.Unmarshal(data, &persisted); err != nil {
		return Job{}, err
	}
	item := persisted.Job
	item.State = state
	item.Payload = decodePersistedJobPayload(persisted.Payload)
	opened, err := j.openPayload(&item, item.Payload)
	if err != nil {
		return Job{}, err
	}
	item.Payload = opened
	if leaseOwner.Valid {
		item.LeaseOwner = leaseOwner.String
	}
	if leaseExpires.Valid {
		expires := time.Unix(0, leaseExpires.Int64).UTC()
		item.LeaseExpires = &expires
	}
	if nextAttempt.Valid {
		next := time.Unix(0, nextAttempt.Int64).UTC()
		item.NextAttempt = &next
	}
	item.Cancel = cancelRequested != 0 || item.Cancel
	return item, nil
}

// ActivePayloads returns the decrypted payloads of every queued or running
// job of kind. It reads durable storage directly so startup reconciliation
// sees all live work, not only the most recent page of jobs.
func (j *Jobs) ActivePayloads(kind string) ([]json.RawMessage, error) {
	if j.db == nil {
		// Durable jobs only exist with a control-plane database.
		return nil, nil
	}
	rows, err := j.db.Query(`SELECT id FROM jobs WHERE kind = ? AND state IN ('queued', 'running')`, kind)
	if err != nil {
		return nil, fmt.Errorf("list active %s jobs: %w", kind, err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("list active %s jobs: %w", kind, err)
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("list active %s jobs: %w", kind, err)
	}
	payloads := make([]json.RawMessage, 0, len(ids))
	for _, id := range ids {
		item, ok, err := j.loadDurableJob(id)
		if err != nil {
			return nil, fmt.Errorf("load active job %s: %w", id, err)
		}
		if ok {
			payloads = append(payloads, item.Payload)
		}
	}
	return payloads, nil
}

// GetWithError reads durable state authoritatively. A database read failure is
// distinct from a missing job; callers serving an API status must report the
// control plane as degraded instead of returning a stale in-memory snapshot.
func (j *Jobs) GetWithError(id string) (Job, bool, error) {
	if j.db != nil {
		item, ok, err := j.loadDurableJob(id)
		if err != nil {
			return Job{}, false, err
		}
		if ok {
			materializeJobOutput(&item)
			stored := item
			j.mu.Lock()
			j.items[id] = &stored
			j.mu.Unlock()
			return item, true, nil
		}
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	item, ok := j.items[id]
	if !ok {
		return Job{}, false, nil
	}
	copy := *item
	materializeJobOutput(&copy)
	return copy, true, nil
}

func (j *Jobs) Get(id string) (Job, bool) {
	if j.db != nil {
		if item, ok, err := j.loadDurableJob(id); err == nil && ok {
			materializeJobOutput(&item)
			stored := item
			j.mu.Lock()
			j.items[id] = &stored
			j.mu.Unlock()
			return item, true
		}
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	item, ok := j.items[id]
	if !ok {
		return Job{}, false
	}
	copy := *item
	materializeJobOutput(&copy)
	return copy, true
}

func materializeJobOutput(item *Job) {
	if item == nil || len(item.Output) == 0 {
		return
	}
	switch item.Kind {
	case "cpmove.restore":
		if item.Result == nil {
			var result ImportResult
			if json.Unmarshal(item.Output, &result) == nil {
				item.Result = &result
			}
		}
	case "site.backup":
		if item.Backup == nil {
			var result BackupResult
			if json.Unmarshal(item.Output, &result) == nil {
				item.Backup = &result
			}
		}
	case "certificate.issue":
		if item.Certificate == nil {
			var result CertificateResult
			if json.Unmarshal(item.Output, &result) == nil {
				item.Certificate = &result
			}
		}
	case "wordpress.restore":
		if item.WPress == nil {
			var result WPressResult
			if json.Unmarshal(item.Output, &result) == nil {
				item.WPress = &result
			}
		}
	case "backup.restore":
		if item.Restore == nil {
			var result BackupRestoreResult
			if json.Unmarshal(item.Output, &result) == nil {
				item.Restore = &result
			}
		}
	case "cloud.action":
		if item.Cloud == nil {
			var result CloudActionResult
			if json.Unmarshal(item.Output, &result) == nil {
				item.Cloud = &result
			}
		}
	}
}

// List returns the limit most recent jobs plus every queued or running job,
// newest first. Active jobs are always included so clients can report active
// work authoritatively even when it started before the recent window.
// listDurable returns the newest limit jobs plus every active job in a single
// query; it replaces an ID query followed by one lookup per job.
func (j *Jobs) listDurable(limit int) ([]Job, error) {
	rows, err := j.db.Query(`SELECT id, `+durableJobColumns+` FROM jobs WHERE id IN (
			SELECT id FROM (SELECT id FROM jobs ORDER BY started_at DESC, id DESC LIMIT ?)
			UNION
			SELECT id FROM jobs WHERE state IN ('queued', 'running')
		) ORDER BY started_at DESC, id DESC`, limit)
	if err != nil {
		return nil, fmt.Errorf("list durable jobs: %w", err)
	}
	defer rows.Close()
	items := make([]Job, 0, limit)
	for rows.Next() {
		var id, state string
		var data []byte
		var leaseOwner sql.NullString
		var leaseExpires, nextAttempt sql.NullInt64
		var cancelRequested int
		if err := rows.Scan(&id, &state, &data, &leaseOwner, &leaseExpires, &nextAttempt, &cancelRequested); err != nil {
			return nil, fmt.Errorf("list durable jobs: %w", err)
		}
		item, err := j.decodeDurableJob(state, data, leaseOwner, leaseExpires, nextAttempt, cancelRequested)
		if err != nil {
			// As before, a single undecodable row is omitted rather than
			// hiding every other job from the Job Center.
			continue
		}
		materializeJobOutput(&item)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list durable jobs: %w", err)
	}
	j.mu.Lock()
	for i := range items {
		stored := items[i]
		j.items[stored.ID] = &stored
	}
	j.mu.Unlock()
	return items, nil
}

func (j *Jobs) List(limit int) []Job {
	if limit < 1 {
		limit = 50
	}
	if j.db != nil {
		if items, err := j.listDurable(limit); err == nil {
			return items
		}
	}
	j.mu.RLock()
	items := make([]Job, 0, len(j.items))
	for _, item := range j.items {
		if item != nil {
			copy := *item
			materializeJobOutput(&copy)
			items = append(items, copy)
		}
	}
	j.mu.RUnlock()
	sort.Slice(items, func(i, k int) bool { return items[i].StartedAt.After(items[k].StartedAt) })
	if len(items) > limit {
		kept := items[:limit:limit]
		for _, item := range items[limit:] {
			if item.State == "queued" || item.State == "running" {
				kept = append(kept, item)
			}
		}
		items = kept
	}
	return items
}

// ListForOwners returns the durable job history for the supplied site owners.
// The owner predicate is applied inside SQLite before the limit, so one
// tenant's busy neighbors cannot hide its older completed jobs. It never
// falls back to the in-memory cache because callers use this for an
// authorization-sensitive operational view.
func (j *Jobs) ListForOwners(limit int, owners []string) ([]Job, error) {
	if j == nil || j.db == nil {
		return nil, errors.New("durable job store is unavailable")
	}
	if limit < 1 {
		limit = 50
	}
	clean := make([]string, 0, len(owners))
	seen := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		owner = strings.TrimSpace(owner)
		if owner == "" {
			continue
		}
		if _, ok := seen[owner]; ok {
			continue
		}
		seen[owner] = struct{}{}
		clean = append(clean, owner)
	}
	if len(clean) == 0 {
		return []Job{}, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(clean)), ",")
	query := `SELECT id, ` + durableJobColumns + ` FROM jobs WHERE id IN (
		SELECT id FROM (SELECT id FROM jobs WHERE owner IN (` + placeholders + `)
			ORDER BY started_at DESC, id DESC LIMIT ?)
		UNION
		SELECT id FROM jobs WHERE owner IN (` + placeholders + `)
			AND state IN ('queued', 'running')
	) ORDER BY started_at DESC, id DESC`
	args := make([]any, 0, len(clean)*2+1)
	for _, owner := range clean {
		args = append(args, owner)
	}
	args = append(args, limit)
	for _, owner := range clean {
		args = append(args, owner)
	}
	rows, err := j.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs for owners: %w", err)
	}
	defer rows.Close()
	items := make([]Job, 0, limit)
	for rows.Next() {
		var id, state string
		var data []byte
		var leaseOwner sql.NullString
		var leaseExpires, nextAttempt sql.NullInt64
		var cancelRequested int
		if err := rows.Scan(&id, &state, &data, &leaseOwner, &leaseExpires, &nextAttempt, &cancelRequested); err != nil {
			return nil, fmt.Errorf("scan jobs for owners: %w", err)
		}
		item, err := j.decodeDurableJob(state, data, leaseOwner, leaseExpires, nextAttempt, cancelRequested)
		if err != nil {
			continue
		}
		materializeJobOutput(&item)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs for owners: %w", err)
	}
	j.mu.Lock()
	for i := range items {
		stored := items[i]
		j.items[stored.ID] = &stored
	}
	j.mu.Unlock()
	return items, nil
}

// ListActiveForSite queries durable active jobs associated with one site. It
// checks both the owner and operation key because older workflows used the
// operation key as their site serialization field. It intentionally returns
// an error instead of falling back to the in-memory cache: callers using this
// method make destructive readiness decisions and must not rely on stale state
// when SQLite is unavailable.
func (j *Jobs) ListActiveForSite(site string) ([]Job, error) {
	if j == nil || j.db == nil {
		return nil, errors.New("durable job store is unavailable")
	}
	site = strings.TrimSpace(site)
	if site == "" {
		return nil, errors.New("site is required")
	}
	rows, err := j.db.Query(`SELECT id, `+durableJobColumns+` FROM jobs WHERE (owner = ? OR operation_key = ?) AND state IN ('queued', 'running') ORDER BY started_at ASC, id ASC`, site, site)
	if err != nil {
		return nil, fmt.Errorf("list active jobs for site %s: %w", site, err)
	}
	defer rows.Close()
	items := make([]Job, 0)
	for rows.Next() {
		var id, state string
		var data []byte
		var leaseOwner sql.NullString
		var leaseExpires, nextAttempt sql.NullInt64
		var cancelRequested int
		if err := rows.Scan(&id, &state, &data, &leaseOwner, &leaseExpires, &nextAttempt, &cancelRequested); err != nil {
			return nil, fmt.Errorf("list active jobs for site %s: %w", site, err)
		}
		item, err := j.decodeDurableJob(state, data, leaseOwner, leaseExpires, nextAttempt, cancelRequested)
		if err != nil {
			return nil, fmt.Errorf("decode active job %s: %w", id, err)
		}
		materializeJobOutput(&item)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list active jobs for site %s: %w", site, err)
	}
	j.mu.Lock()
	for i := range items {
		stored := items[i]
		j.items[stored.ID] = &stored
	}
	j.mu.Unlock()
	return items, nil
}

func (j *Jobs) Submit(id, user string, work func() (ImportResult, error)) error {
	_, _, err := j.submitImport(id, user, "", work)
	return err
}

// SubmitIdempotent queues a cpmove restore with a caller-supplied operation
// key. Reusing the same key for the same account returns the original job ID
// and does not execute the restore a second time. The key is persisted with
// the job so this remains true after a control-plane restart.
func (j *Jobs) SubmitIdempotent(id, user, operationKey string, work func() (ImportResult, error)) (jobID string, existing bool, err error) {
	return j.submitImport(id, user, operationKey, work)
}

func (j *Jobs) submitImport(id, user, operationKey string, work func() (ImportResult, error)) (jobID string, existing bool, err error) {
	j.admission.RLock()
	defer j.admission.RUnlock()
	existingID, reserved := j.reserveOperation(user, "cpmove.restore", operationKey)
	if existingID != "" {
		return existingID, true, nil
	}
	if !reserved {
		return "", false, ErrJobBusy
	}
	j.wg.Add(1)
	item := &Job{ID: id, Kind: "cpmove.restore", OperationKey: operationKey, State: "running", User: user, StartedAt: time.Now().UTC()}
	if err := j.add(item); err != nil {
		j.wg.Done()
		j.release(user)
		return "", false, fmt.Errorf("persist queued job: %w", err)
	}
	go func() {
		defer j.wg.Done()
		defer j.release(user)
		stopLease := j.maintainLease(item)
		defer stopLease()
		result, err := work()
		now := time.Now().UTC()
		j.mu.Lock()
		item.FinishedAt = &now
		if err != nil {
			item.State = "failed"
			item.Error = err.Error()
		} else {
			item.State = "completed"
			item.Result = &result
		}
		j.mu.Unlock()
		j.complete(item)
	}()
	return id, false, nil
}

func (j *Jobs) SubmitBackup(id, site string, work func() (BackupResult, error)) error {
	j.admission.RLock()
	defer j.admission.RUnlock()
	if !j.reserve(site) {
		return ErrJobBusy
	}
	j.wg.Add(1)
	item := &Job{ID: id, Kind: "site.backup", State: "running", User: site, StartedAt: time.Now().UTC()}
	if err := j.add(item); err != nil {
		j.wg.Done()
		j.release(site)
		return fmt.Errorf("persist queued job: %w", err)
	}
	go func() {
		defer j.wg.Done()
		defer j.release(site)
		stopLease := j.maintainLease(item)
		defer stopLease()
		result, err := work()
		now := time.Now().UTC()
		j.mu.Lock()
		item.FinishedAt = &now
		if err != nil {
			item.State = "failed"
			item.Error = err.Error()
		} else {
			item.State = "completed"
			item.Backup = &result
		}
		j.mu.Unlock()
		j.complete(item)
	}()
	return nil
}

// SubmitBackupRestore queues a destructive, journaled site restore while
// using the same per-site admission guard as backup creation.
func (j *Jobs) SubmitBackupRestore(id, site string, work func() (BackupRestoreResult, error)) error {
	j.admission.RLock()
	defer j.admission.RUnlock()
	if !j.reserve(site) {
		return ErrJobBusy
	}
	j.wg.Add(1)
	item := &Job{ID: id, Kind: "site.backup-restore", State: "running", User: site, StartedAt: time.Now().UTC()}
	if err := j.add(item); err != nil {
		j.wg.Done()
		j.release(site)
		return fmt.Errorf("persist queued job: %w", err)
	}
	go func() {
		defer j.wg.Done()
		defer j.release(site)
		stopLease := j.maintainLease(item)
		defer stopLease()
		result, err := work()
		now := time.Now().UTC()
		j.mu.Lock()
		item.FinishedAt = &now
		if err != nil {
			item.State = "failed"
			item.Error = err.Error()
		} else {
			item.State = "completed"
			item.Restore = &result
		}
		j.mu.Unlock()
		j.complete(item)
	}()
	return nil
}

func (j *Jobs) reserve(target string) bool {
	_, reserved := j.reserveOperation(target, "", "")
	return reserved
}

// reserveOperation combines admission and idempotency lookup while holding
// the job lock after acquiring a slot. This closes the race where two retrying
// requests could both observe no matching operation before either persisted.
func (j *Jobs) reserveOperation(target, kind, operationKey string) (existingID string, reserved bool) {
	if !j.admissionGate.Acquire() {
		return "", false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if operationKey != "" {
		for _, item := range j.items {
			if item != nil && item.Kind == kind && item.User == target && item.OperationKey == operationKey {
				j.admissionGate.Release()
				return item.ID, false
			}
		}
	}
	if j.activeTargets[target] {
		j.admissionGate.Release()
		return "", false
	}
	j.activeTargets[target] = true
	return "", true
}

func (j *Jobs) release(target string) {
	j.mu.Lock()
	delete(j.activeTargets, target)
	j.mu.Unlock()
	j.admissionGate.Release()
}

func (j *Jobs) SubmitCertificate(id, domain string, work func() (CertificateResult, error)) error {
	j.admission.RLock()
	defer j.admission.RUnlock()
	if !j.admissionGate.Acquire() {
		return ErrJobBusy
	}
	j.mu.Lock()
	if j.activeDomains[domain] {
		j.mu.Unlock()
		j.admissionGate.Release()
		return ErrJobBusy
	}
	j.activeDomains[domain] = true
	j.mu.Unlock()
	j.wg.Add(1)
	item := &Job{ID: id, Kind: "certificate.issue", State: "running", User: domain, StartedAt: time.Now().UTC()}
	if err := j.add(item); err != nil {
		j.wg.Done()
		j.mu.Lock()
		delete(j.activeDomains, domain)
		j.mu.Unlock()
		j.admissionGate.Release()
		return fmt.Errorf("persist queued job: %w", err)
	}
	go func() {
		defer j.wg.Done()
		defer func() { j.mu.Lock(); delete(j.activeDomains, domain); j.mu.Unlock(); j.admissionGate.Release() }()
		stopLease := j.maintainLease(item)
		defer stopLease()
		result, err := work()
		now := time.Now().UTC()
		j.mu.Lock()
		item.FinishedAt = &now
		if err != nil {
			item.State = "failed"
			item.Error = err.Error()
		} else {
			item.State = "completed"
			item.Certificate = &result
		}
		j.mu.Unlock()
		j.complete(item)
	}()
	return nil
}

func (j *Jobs) Wait(ctx context.Context) error {
	j.admission.Lock()
	defer j.admission.Unlock()
	done := make(chan struct{})
	go func() {
		j.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Cleanup forgets finished jobs older than maxAge. The durable store is
// authoritative: a job leaves memory only after its removal has been
// committed. If the removal fails, the jobs stay in memory (and remain
// durable), the readiness check exposes the persistence error, and the next
// Cleanup retries them instead of leaving rows that would resurface on restart.
func (j *Jobs) Cleanup(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	j.mu.Lock()
	defer j.mu.Unlock()
	removed := make(map[string]*Job)
	for id, item := range j.items {
		if item.FinishedAt != nil && item.FinishedAt.Before(cutoff) && item.State != "dead-letter" {
			removed[id] = item
		}
	}
	if len(removed) == 0 {
		return
	}
	for id := range removed {
		delete(j.items, id)
	}
	if err := j.persistCleanupLocked(removed); err != nil {
		for id, item := range removed {
			j.items[id] = item
		}
		j.observePersistFailureLocked("persist_job_cleanup", err)
		log.Printf("persist job cleanup: %v", err)
		return
	}
	j.persistErr = nil
}

// persistCleanupLocked durably removes the given finished jobs. With SQLite all
// deletions commit in one transaction; the file store rewrites the remaining
// jobs atomically. Callers must hold j.mu and have already removed the jobs
// from j.items.
func (j *Jobs) persistCleanupLocked(removed map[string]*Job) error {
	if j.db == nil {
		return j.persistLocked()
	}
	tx, err := j.db.Begin()
	if err != nil {
		return fmt.Errorf("begin durable job cleanup: %w", err)
	}
	for id := range removed {
		// Only finished rows are eligible; never delete a row another process
		// has re-activated since this process loaded it.
		if _, err := tx.Exec(`DELETE FROM jobs WHERE id = ? AND finished_at IS NOT NULL AND state <> 'dead-letter'`, id); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("delete completed durable job %s: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit durable job cleanup: %w", err)
	}
	return nil
}
