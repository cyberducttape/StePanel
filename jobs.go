package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	jobadmission "github.com/itchyitchy123/StePanel/internal/jobs"
)

var ErrJobBusy = errors.New("too many long-running jobs or target is already active")

// authorizeDurableSiteJob rechecks tenant policy at execution time. Queue
// admission is not sufficient because an account can be suspended or detached
// from a site while a worker is waiting on another job.
func (a *App) authorizeDurableSiteJob(site, actor string, scheduled bool) error {
	if safeUser(site) == "" || strings.TrimSpace(actor) == "" {
		return errors.New("durable site job has invalid ownership data")
	}
	if scheduled || actor == a.Auth.Username {
		return nil
	}
	if a.Accounts == nil {
		return errors.New("tenant ownership state is unavailable")
	}
	account, ok := a.Accounts.Get(actor)
	if !ok || account.Suspended || !a.Accounts.OwnsSite(actor, site) {
		return errors.New("durable job actor no longer owns the site")
	}
	return nil
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

// Claim acquires a durable lease for a queued job. It is the boundary used by
// local workers and future remote agents; only the lease holder may complete
// or renew the job.
func (j *Jobs) Claim(id, owner string) (Job, bool, error) {
	if j.db == nil || id == "" || owner == "" {
		return Job{}, false, errors.New("durable job claiming requires a job ID and owner")
	}
	now := time.Now().UTC()
	expires := now.Add(jobLeaseDuration)
	j.mu.Lock()
	defer j.mu.Unlock()
	item, ok := j.items[id]
	if !ok || item == nil || item.State != "queued" {
		return Job{}, false, nil
	}
	item.State = "running"
	item.LeaseOwner = owner
	item.LeaseExpires = &expires
	result, err := j.db.Exec(`UPDATE jobs SET state='running', lease_owner=?, lease_expires_at=?, updated_at=unixepoch() WHERE id=? AND state='queued'`, owner, expires.UnixNano(), id)
	if err != nil {
		return Job{}, false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Job{}, false, err
	}
	if count != 1 {
		item.State = "queued"
		item.LeaseOwner = ""
		item.LeaseExpires = nil
		return Job{}, false, nil
	}
	copy := *item
	return copy, true, nil
}

func (j *Jobs) encodeDurableItem(item *Job) ([]byte, any, any, any, error) {
	if item == nil || item.ID == "" {
		return nil, nil, nil, nil, errors.New("cannot persist invalid job")
	}
	sealedPayload, err := sealJobPayload(j.payloadKey, item.Payload)
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
		return errors.New("job lease is no longer held by this worker")
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
	expires := time.Now().UTC().Add(jobLeaseDuration)
	item.LeaseExpires = &expires
	if err := j.persistDurableItemCAS(item, owner); err != nil {
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
	for _, item := range j.items {
		if item != nil && item.State == "running" && item.LeaseExpires != nil && now.After(*item.LeaseExpires) {
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
		return errors.New("job lease is not held by this worker")
	}
	apply(item)
	item.LeaseOwner = ""
	item.LeaseExpires = nil
	return j.persistDurableItemCAS(item, owner)
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
	if j.db == nil || strings.TrimSpace(operationKey) == "" {
		if j.db != nil && strings.TrimSpace(operationKey) == "" {
			var existingID string
			if err := j.db.QueryRow(`SELECT id FROM jobs WHERE kind = ? AND owner = ? AND state IN ('queued', 'running') ORDER BY started_at LIMIT 1`, kind, owner).Scan(&existingID); err == nil {
				if item, ok := j.Get(existingID); ok {
					return item, true, nil
				}
				return Job{ID: existingID, Kind: kind, User: owner}, true, nil
			}
		}
		item, err := j.Enqueue(kind, owner, operationKey, payload, maxAttempts)
		return item, false, err
	}
	var existingID string
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
	expires := time.Now().UTC().Add(jobLeaseDuration)
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
	openedPayload, err := openJobPayload(j.payloadKey, item.Payload)
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
	return j.persistDurableItemCAS(item, owner)
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
	item.Progress = progress
	return j.persistDurableItemCAS(item, owner)
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
	opened, err := openJobPayload(j.payloadKey, persisted.Job.Payload)
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

// IntegrityCheck verifies the SQLite control plane before it is advertised as
// ready. quick_check is bounded enough for frequent readiness probes while
// still detecting structural corruption across the control-plane tables.
func (j *Jobs) IntegrityCheck() error {
	if j == nil || j.db == nil {
		return errors.New("control-plane database is unavailable")
	}
	var result string
	if err := j.db.QueryRow(`PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if strings.TrimSpace(strings.ToLower(result)) != "ok" {
		return fmt.Errorf("SQLite quick_check returned %q", result)
	}
	return nil
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
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		item, claimed, err := j.ClaimNext(owner, kinds...)
		if err != nil {
			return err
		}
		if claimed {
			if j.CancellationRequested(item.ID) {
				if err := j.finishClaim(item.ID, owner, func(job *Job) {
					job.State = "cancelled"
					now := time.Now().UTC()
					job.FinishedAt = &now
				}); err != nil {
					return err
				}
				continue
			}
			var output []byte
			output, err = func() (runOutput []byte, runErr error) {
				defer func() {
					if recovered := recover(); recovered != nil {
						runErr = fmt.Errorf("worker panic: %v", recovered)
					}
				}()
				return handler(ctx, item)
			}()
			if err != nil {
				if failErr := j.FailClaim(item.ID, owner, err.Error()); failErr != nil {
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
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
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
	interval := jobLeaseDuration / 3
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
	payloadKey    []byte
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
	}
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
	return j, nil
}

func jobPayloadKey(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

func sealJobPayload(key, payload []byte) ([]byte, error) {
	if len(key) == 0 || len(payload) == 0 {
		return append([]byte(nil), payload...), nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, payload, nil)
	result := make([]byte, 0, len(encryptedJobPayloadPrefix)+len(nonce)+len(sealed))
	result = append(result, encryptedJobPayloadPrefix...)
	result = append(result, nonce...)
	result = append(result, sealed...)
	return result, nil
}

func openJobPayload(key, payload []byte) ([]byte, error) {
	if !bytes.HasPrefix(payload, encryptedJobPayloadPrefix) {
		return payload, nil
	}
	if len(key) == 0 {
		return nil, errors.New("encrypted job payload requires STEPANEL_ACCOUNT_KEY")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(payload) < len(encryptedJobPayloadPrefix)+gcm.NonceSize() {
		return nil, errors.New("encrypted job payload is truncated")
	}
	nonceStart := len(encryptedJobPayloadPrefix)
	nonce := payload[nonceStart : nonceStart+gcm.NonceSize()]
	return gcm.Open(nil, nonce, payload[nonceStart+gcm.NonceSize():], nil)
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
	}
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
			openedPayload, err := openJobPayload(j.payloadKey, item.Payload)
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
	var store jobStore
	if err := json.Unmarshal(data, &store); err != nil {
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
	if reconciled {
		if err := j.persistLocked(); err != nil {
			return fmt.Errorf("persist reconciled job state: %w", err)
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
			sealedPayload, err := sealJobPayload(j.payloadKey, item.Payload)
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
	if _, err = tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write job state: %w", err)
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
	if dir, err := os.Open(root); err == nil {
		syncErr := dir.Sync()
		_ = dir.Close()
		if syncErr != nil {
			return fmt.Errorf("sync job state directory: %w", syncErr)
		}
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
		expires := time.Now().UTC().Add(jobLeaseDuration)
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
		j.persistErr = err
		delete(j.items, item.ID)
		return err
	}
	j.persistErr = nil
	return nil
}

func (j *Jobs) complete(item *Job) {
	j.mu.Lock()
	defer j.mu.Unlock()
	item.LeaseOwner = ""
	item.LeaseExpires = nil
	var err error
	if j.db != nil {
		err = j.persistDurableItem(item)
	} else {
		err = j.persistLocked()
	}
	if err != nil {
		j.persistErr = err
		log.Printf("persist completed job %s: %v", item.ID, err)
	} else {
		j.persistErr = nil
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
	err := j.db.QueryRow(`SELECT state, payload, lease_owner, lease_expires_at, next_attempt_at, cancel_requested FROM jobs WHERE id = ?`, id).Scan(&state, &data, &leaseOwner, &leaseExpires, &nextAttempt, &cancelRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	var persisted persistedJob
	if err := json.Unmarshal(data, &persisted); err != nil {
		return Job{}, false, err
	}
	item := persisted.Job
	item.State = state
	item.Payload = decodePersistedJobPayload(persisted.Payload)
	opened, err := openJobPayload(j.payloadKey, item.Payload)
	if err != nil {
		return Job{}, false, err
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
	return item, true, nil
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

func (j *Jobs) List(limit int) []Job {
	if limit < 1 {
		limit = 50
	}
	if j.db != nil {
		rows, err := j.db.Query(`SELECT id FROM jobs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
		if err == nil {
			ids := make([]string, 0, limit)
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}
			rowsErr := rows.Err()
			closeErr := rows.Close()
			if rowsErr != nil {
				rowsErr = fmt.Errorf("list durable jobs: %w", rowsErr)
			}
			if rowsErr == nil && closeErr == nil {
				items := make([]Job, 0, limit)
				for _, id := range ids {
					if item, ok, loadErr := j.loadDurableJob(id); loadErr == nil && ok {
						materializeJobOutput(&item)
						stored := item
						j.mu.Lock()
						j.items[id] = &stored
						j.mu.Unlock()
						items = append(items, item)
					}
				}
				return items
			}
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
		items = items[:limit]
	}
	return items
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

func (j *Jobs) Cleanup(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	j.mu.Lock()
	defer j.mu.Unlock()
	changed := false
	removed := make([]string, 0)
	for id, item := range j.items {
		if item.FinishedAt != nil && item.FinishedAt.Before(cutoff) {
			delete(j.items, id)
			removed = append(removed, id)
			changed = true
		}
	}
	if changed {
		if err := j.persistLocked(); err != nil {
			j.persistErr = err
			log.Printf("persist job cleanup: %v", err)
		} else if j.db != nil {
			tx, err := j.db.Begin()
			if err == nil {
				for _, id := range removed {
					if _, err = tx.Exec(`DELETE FROM jobs WHERE id = ?`, id); err != nil {
						break
					}
				}
			}
			var commitErr error
			if err == nil {
				commitErr = tx.Commit()
			}
			if err != nil || commitErr != nil {
				if tx != nil {
					_ = tx.Rollback()
				}
				if err == nil {
					err = commitErr
				}
				j.persistErr = fmt.Errorf("delete completed durable jobs: %w", err)
			} else {
				j.persistErr = nil
			}
		} else {
			j.persistErr = nil
		}
	}
}
