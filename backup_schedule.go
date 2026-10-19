package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cyberducttape/StePanel/internal/backup"
	"log"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

// Type alias for backward compatibility
type BackupSchedule = backup.BackupSchedule

type backupSchedules struct {
	mu    sync.Mutex
	path  string
	items map[string]BackupSchedule
}

type backupScheduleOutboxItem struct {
	id, site, key string
	payload       []byte
}

func openBackupSchedules(path string) (*backupSchedules, error) {
	s := &backupSchedules{path: path, items: map[string]BackupSchedule{}}
	b, err := readStateFile(path, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if errors.Is(err, errStateFileTooLarge) {
		return nil, errors.New("backup schedule state exceeds 1 MiB")
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.items); err != nil {
		return nil, fmt.Errorf("decode backup schedules: %w", err)
	}
	for site, item := range s.items {
		if item.KeepLast == 0 {
			item.KeepLast = 7
			s.items[site] = item
		}
		if safeUser(site) == "" || item.Site != site || item.IntervalMinutes < 5 || item.IntervalMinutes > 10080 || item.KeepLast < 1 || item.KeepLast > 365 || item.NextRun.IsZero() {
			return nil, fmt.Errorf("invalid backup schedule for site %q", site)
		}
	}
	return s, nil
}
func (s *backupSchedules) persistLocked() error {
	b, err := json.Marshal(s.items)
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(s, b); bound {
		return err
	}
	return writeAtomic(s.path, append(b, '\n'), 0600)
}
func (s *backupSchedules) list() []BackupSchedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]BackupSchedule, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Site < out[j].Site })
	return out
}

func (a *App) backupSchedules(w http.ResponseWriter, r *http.Request) {
	if a.Schedules == nil {
		http.Error(w, "backup scheduling is unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"schedules": a.Schedules.list()})
	case http.MethodPut:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		var in BackupSchedule
		if err := decodeJSON(w, r, 4096, &in); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		in.Site = safeUser(in.Site)
		if in.KeepLast == 0 {
			in.KeepLast = 7
		}
		if in.Site == "" || in.IntervalMinutes < 5 || in.IntervalMinutes > 10080 || in.KeepLast < 1 || in.KeepLast > 365 {
			http.Error(w, "site is invalid, interval must be 5-10080 minutes, and keep_last must be 1-365", 422)
			return
		}
		if in.IncludeDatabases && a.Config.DBCtl == "" {
			http.Error(w, "managed database backup requires the local database helper", 422)
			return
		}
		if _, err := existingManagedSitePublicRoot(a.Config.WebRoot, in.Site); err != nil {
			http.Error(w, "site document root does not exist", 422)
			return
		}
		operationCtx, release, lockErr := a.acquireSiteMutationLockContext(r.Context(), in.Site)
		if lockErr != nil {
			http.Error(w, "backup schedule mutation is busy", http.StatusConflict)
			return
		}
		defer release()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "backup schedule mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		in.Enabled = true
		in.NextRun = time.Now().UTC().Add(time.Duration(in.IntervalMinutes) * time.Minute)
		a.Schedules.mu.Lock()
		err := persistMapKeyChange(a.Schedules.items, in.Site, &in, a.Schedules.persistLocked)
		a.Schedules.mu.Unlock()
		if err != nil {
			http.Error(w, "could not persist backup schedule", 500)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "backup schedule mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "backup.schedule.updated", in.Site, fmt.Sprintf("every %d minutes; keep last %d", in.IntervalMinutes, in.KeepLast))
		writeJSON(w, http.StatusOK, in)
	case http.MethodDelete:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		site := safeUser(r.URL.Query().Get("site"))
		if site == "" {
			http.Error(w, "invalid site", 422)
			return
		}
		operationCtx, release, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
		if lockErr != nil {
			http.Error(w, "backup schedule mutation is busy", http.StatusConflict)
			return
		}
		defer release()
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "backup schedule mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		a.Schedules.mu.Lock()
		err := persistMapKeyChange(a.Schedules.items, site, (*BackupSchedule)(nil), a.Schedules.persistLocked)
		a.Schedules.mu.Unlock()
		if err != nil {
			http.Error(w, "could not persist backup schedule", 500)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "backup schedule mutation cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "backup.schedule.deleted", site, "schedule removed")
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (a *App) runDueBackups() {
	if a.Schedules == nil || a.Jobs == nil || a.Jobs.db == nil {
		return
	}
	now := time.Now().UTC()
	a.Schedules.mu.Lock()
	due := make([]BackupSchedule, 0)
	for _, s := range a.Schedules.items {
		if !s.Enabled || s.NextRun.After(now) {
			continue
		}
		due = append(due, s)
	}
	a.Schedules.mu.Unlock()
	if err := a.dispatchBackupScheduleOutbox(); err != nil {
		log.Printf("dispatch scheduled backup outbox: %v", err)
	}
	bindingValue, bound := controlPlaneStateBindings.Load(a.Schedules)
	if !bound {
		TelemetryAudit(a.Config.AuditLog, "scheduler", "backup.schedule.persistence_failed", "", "schedule state is not bound to the durable control-plane database")
		return
	}
	binding := bindingValue.(controlPlaneStateBinding)
	if binding.db != a.Jobs.db || binding.name == "" {
		TelemetryAudit(a.Config.AuditLog, "scheduler", "backup.schedule.persistence_failed", "", "backup schedules and jobs do not share a control-plane database")
		return
	}
	for _, schedule := range due {
		if err := a.stageScheduledBackup(binding, schedule, now); err != nil {
			TelemetryAudit(a.Config.AuditLog, "scheduler", "backup.schedule.persistence_failed", schedule.Site, err.Error())
		}
	}
	if err := a.dispatchBackupScheduleOutbox(); err != nil {
		log.Printf("dispatch scheduled backup outbox: %v", err)
	}
}

// stageScheduledBackup atomically advances the authoritative SQLite schedule
// image and records a durable intent for job admission. The dispatcher may
// safely replay the intent because its operation key is deterministic.
func (a *App) stageScheduledBackup(binding controlPlaneStateBinding, due BackupSchedule, now time.Time) error {
	tx, err := binding.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	var revision int64
	if err := tx.QueryRow(`SELECT payload, revision FROM state_blobs WHERE name=?`, binding.name).Scan(&raw, &revision); err != nil {
		return err
	}
	var schedules map[string]BackupSchedule
	if err := json.Unmarshal(raw, &schedules); err != nil {
		return fmt.Errorf("decode authoritative backup schedules: %w", err)
	}
	current, exists := schedules[due.Site]
	if !exists || !current.Enabled || !current.NextRun.Equal(due.NextRun) {
		_ = tx.Rollback()
		if err := a.refreshBoundBackupSchedules(binding); err != nil {
			return fmt.Errorf("refresh schedules changed by another process: %w", err)
		}
		return nil
	}
	operationKey := fmt.Sprintf("schedule:%s:%d", current.Site, current.NextRun.UnixNano())
	started := now
	updated := current
	updated.LastRun = &started
	updated.NextRun = now.Add(time.Duration(current.IntervalMinutes) * time.Minute)
	schedules[current.Site] = updated
	encodedState, err := json.Marshal(schedules)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(durableBackupRequest{Site: current.Site, IncludeDatabases: current.IncludeDatabases, Scheduled: true, KeepLast: current.KeepLast, Actor: "scheduler", StartedAt: now})
	if err != nil {
		return err
	}
	result, err := tx.Exec(`UPDATE state_blobs SET payload=?, updated_at=unixepoch(), revision=revision+1 WHERE name=? AND revision=?`, encodedState, binding.name, revision)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("backup schedule changed concurrently; retry on next scheduler tick")
	}
	if _, err := tx.Exec(`INSERT INTO backup_schedule_outbox(id, site, operation_key, payload, created_at) VALUES(?, ?, ?, ?, ?) ON CONFLICT(operation_key) DO NOTHING`, operationKey, current.Site, operationKey, payload, now.UnixNano()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// Refresh all schedule keys from the committed image so a process that had
	// stale peer state cannot later persist an accidental deletion of another
	// process's schedule. This small read is outside the queue dispatch and
	// transaction; the mutex is held only while decoding the current image.
	if err := a.refreshBoundBackupSchedules(binding); err != nil {
		log.Printf("refresh committed backup schedule state: %v", err)
		// The outbox and schedule transition are already durable; report success
		// so this intent is not represented as uncommitted work.
	}
	return nil
}

func (a *App) refreshBoundBackupSchedules(binding controlPlaneStateBinding) error {
	a.Schedules.mu.Lock()
	defer a.Schedules.mu.Unlock()
	raw, revision, err := readControlPlaneBlobWithRevision(binding.db, binding.name)
	if err != nil {
		return err
	}
	if err := decodeBoundControlPlaneTarget(binding.target, raw); err != nil {
		return err
	}
	controlPlaneStateRevisions.Store(a.Schedules, revision)
	controlPlaneStateSnapshots.Store(a.Schedules, append([]byte(nil), raw...))
	return nil
}

func (a *App) dispatchBackupScheduleOutbox() error {
	if a.Jobs == nil || a.Jobs.db == nil {
		return errors.New("durable jobs are unavailable")
	}
	bindingValue, bound := controlPlaneStateBindings.Load(a.Schedules)
	if !bound {
		return errors.New("backup schedules are not bound to durable control-plane state")
	}
	binding := bindingValue.(controlPlaneStateBinding)
	if binding.db != a.Jobs.db || binding.name == "" {
		return errors.New("backup schedules and jobs do not share a control-plane database")
	}
	// Only each site's oldest pending intent is eligible, so a site whose
	// intents cannot be delivered occupies one slot instead of the batch.
	rows, err := a.Jobs.db.Query(`SELECT id, site, operation_key, payload FROM (
		SELECT id, site, operation_key, payload, created_at,
			ROW_NUMBER() OVER (PARTITION BY site ORDER BY created_at, id) AS position
		FROM backup_schedule_outbox WHERE delivered_at IS NULL
	) WHERE position = 1 ORDER BY created_at, id LIMIT 100`)
	if err != nil {
		return err
	}
	var batch []backupScheduleOutboxItem
	for rows.Next() {
		var item backupScheduleOutboxItem
		if err := rows.Scan(&item.id, &item.site, &item.key, &item.payload); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// A busy or failing site leaves its intent pending for the next tick and
	// never delays other sites. Errors that are not specific to one site
	// (schedule state or outbox writes) stop the batch.
	var siteFailures []error
	for _, item := range batch {
		retry, err := a.dispatchBackupScheduleItem(binding, item)
		if err != nil && !retry {
			return errors.Join(append(siteFailures, err)...)
		}
		if err != nil && !errors.Is(err, errSiteMutationBusy) {
			siteFailures = append(siteFailures, err)
		}
	}
	return errors.Join(siteFailures...)
}

// dispatchBackupScheduleItem delivers one outbox intent. retry reports that
// err is local to the item's site and the intent stays pending.
func (a *App) dispatchBackupScheduleItem(binding controlPlaneStateBinding, item backupScheduleOutboxItem) (retry bool, err error) {
	operationCtx, release, lockErr := a.tryAcquireSiteMutationLockContext(context.Background(), item.site)
	if errors.Is(lockErr, errSiteMutationBusy) {
		return true, lockErr
	}
	if lockErr != nil {
		return false, fmt.Errorf("lock scheduled backup site %s: %w", item.site, lockErr)
	}
	defer release()
	if err := a.refreshBoundBackupSchedules(binding); err != nil {
		return false, fmt.Errorf("refresh schedule before dispatch for %s: %w", item.site, err)
	}
	a.Schedules.mu.Lock()
	schedule, exists := a.Schedules.items[item.site]
	a.Schedules.mu.Unlock()
	if !exists || !schedule.Enabled {
		if _, err := a.Jobs.db.Exec(`DELETE FROM backup_schedule_outbox WHERE id=? AND delivered_at IS NULL`, item.id); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := operationCtx.Err(); err != nil {
		return true, fmt.Errorf("scheduled backup site lock for %s was lost: %w", item.site, err)
	}
	if err := a.enqueueScheduledBackupIntent(item); err != nil {
		return true, fmt.Errorf("enqueue schedule intent %s: %w", item.id, err)
	}
	if _, err := a.Jobs.db.Exec(`DELETE FROM backup_schedule_outbox WHERE id=? AND delivered_at IS NULL`, item.id); err != nil {
		return false, err
	}
	return false, nil
}

func (a *App) enqueueScheduledBackupIntent(item backupScheduleOutboxItem) error {
	_, _, err := a.Jobs.EnqueueIdempotent("site.backup", item.site, item.key, item.payload, 3)
	if !errors.Is(err, ErrIdempotencyConflict) {
		return err
	}
	// Before the outbox migration, a crash between enqueue and schedule
	// persistence could leave a job with the same schedule key but a different
	// StartedAt timestamp. Reconcile that prior intent semantically instead of
	// permanently wedging the outbox on a harmless timestamp difference.
	var existingID string
	if queryErr := a.Jobs.db.QueryRow(`SELECT id FROM jobs WHERE kind='site.backup' AND owner=? AND operation_key=?`, item.site, item.key).Scan(&existingID); queryErr != nil {
		return errors.Join(err, queryErr)
	}
	existing, found, loadErr := a.Jobs.loadDurableJob(existingID)
	if loadErr != nil || !found {
		return errors.Join(err, loadErr)
	}
	var wanted, actual durableBackupRequest
	if json.Unmarshal(item.payload, &wanted) != nil || json.Unmarshal(existing.Payload, &actual) != nil || wanted.Site != actual.Site || wanted.IncludeDatabases != actual.IncludeDatabases || wanted.Scheduled != actual.Scheduled || wanted.KeepLast != actual.KeepLast || wanted.Actor != actual.Actor {
		return err
	}
	return nil
}

// removeSiteBackupSchedule is an idempotent lifecycle cleanup operation. It
// is called from the journaled termination task-cleanup step so a deleted site
// can never leave a scheduler entry that targets a future site with the same
// name.
func (a *App) removeSiteBackupSchedule(ctx context.Context, site string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.Schedules == nil {
		return nil
	}
	a.Schedules.mu.Lock()
	_, exists := a.Schedules.items[site]
	var err error
	if exists {
		err = persistMapKeyChange(a.Schedules.items, site, (*BackupSchedule)(nil), a.Schedules.persistLocked)
	}
	a.Schedules.mu.Unlock()
	if err != nil {
		return fmt.Errorf("remove backup schedule state: %w", err)
	}
	if a.Jobs != nil && a.Jobs.db != nil {
		if _, err := a.Jobs.db.Exec(`DELETE FROM backup_schedule_outbox WHERE site=?`, site); err != nil {
			return fmt.Errorf("remove pending backup schedule intents: %w", err)
		}
	}
	return ctx.Err()
}

func (s *backupSchedules) recordResult(site string, started time.Time, resultErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, exists := s.items[site]
	if !exists {
		return
	}
	item.LastDurationMS = time.Since(started).Milliseconds()
	if resultErr != nil {
		item.LastError = resultErr.Error()
		item.ConsecutiveFails++
	} else {
		now := time.Now().UTC()
		item.LastSuccess = &now
		item.LastError = ""
		item.ConsecutiveFails = 0
	}
	if err := persistMapKeyChange(s.items, site, &item, s.persistLocked); err != nil {
		log.Printf("persist backup result for %s: %v", site, err)
	}
}
