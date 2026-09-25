package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type durableSiteTerminationRequest struct {
	Site  string `json:"site"`
	Actor string `json:"actor"`
}

// enqueueSiteTermination records a destructive lifecycle operation before any
// host state is changed. The site is the serialization key, so a retry or a
// duplicate request cannot run two teardown workflows concurrently.
func (a *App) enqueueSiteTermination(site, actor string) (Job, error) {
	payload, err := json.Marshal(durableSiteTerminationRequest{Site: site, Actor: actor})
	if err != nil {
		return Job{}, err
	}
	job, _, err := a.Jobs.EnqueueIdempotent("site.terminate", site, "", payload, 5)
	return job, err
}

func (a *App) siteTermination(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}
	var input struct {
		Site         string `json:"site"`
		Confirmation string `json:"confirmation"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Site = safeUser(strings.TrimSpace(input.Site))
	if input.Site == "" || input.Confirmation != "DELETE "+input.Site {
		http.Error(w, "confirmation must exactly match DELETE "+input.Site, http.StatusUnprocessableEntity)
		return
	}
	root, err := safePath(a.Config.WebRoot, "sites", input.Site, "public")
	if err != nil {
		http.Error(w, "invalid site", http.StatusUnprocessableEntity)
		return
	}
	if _, err := os.Stat(root); err != nil {
		http.Error(w, "site document root does not exist", http.StatusNotFound)
		return
	}
	job, err := a.enqueueSiteTermination(input.Site, a.Auth.UsernameForRequest(r))
	if err != nil {
		http.Error(w, "could not persist site termination job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID})
}

// handleSiteTermination executes the destructive site-termination step
// sequence with roll-forward recovery. Each step consults a persistent
// step journal (site_lifecycle_journal.go): if the step is already
// recorded as complete, it is skipped; otherwise the step runs and, on
// success, is journaled before the next step begins. A process crash
// between steps therefore never re-runs a committed step, and a crash
// during a step retries only that step. The journal is removed only
// after every step commits — the persisted audit trail becomes the
// durable record of the termination from that point.
//
// The gate before any destructive work is BACKUP_VERIFIED. All later
// steps are roll-forward: retries after that point drive the sequence
// to completion rather than trying to un-do anything.
func (a *App) handleSiteTermination(ctx context.Context, item Job) ([]byte, error) {
	var request durableSiteTerminationRequest
	if err := json.Unmarshal(item.Payload, &request); err != nil {
		return nil, fmt.Errorf("decode site termination payload: %w", err)
	}
	if safeUser(request.Site) == "" || request.Actor == "" {
		return nil, errors.New("site termination requires site and actor")
	}
	access, err := a.authorizeDurableSiteJob(request.Site, request.Actor, false)
	if err != nil {
		return nil, err
	}
	if a.Jobs.CancellationRequested(item.ID) {
		return nil, context.Canceled
	}
	release, err := a.acquireSiteMutationLock(ctx, request.Site)
	if err != nil {
		return nil, fmt.Errorf("acquire durable site lock: %w", err)
	}
	defer release()

	journal, err := loadOrCreateTerminationJournal(a.Config.RecoveryRoot, item.ID, request.Site, request.Actor)
	if err != nil {
		return nil, fmt.Errorf("prepare termination journal: %w", err)
	}

	// Announce the operation before any destructive work, so an audit
	// entry exists even if the job then crashes before COMPLETED. This
	// is the "operation initiated" record irreversible operations should
	// leave behind — a persistence failure here fails the job because
	// the whole point is the durable record.
	if !journal.isComplete(stepBackupVerified) {
		if err := AuditAs(a.Config.AuditLog, request.Actor, "site.termination.initiated", request.Site, "job="+item.ID); err != nil {
			return nil, fmt.Errorf("record termination initiation: %w", err)
		}
	}

	// Step 1: BACKUP_VERIFIED. The verified backup is the sole
	// recovery gate; every later step is roll-forward.
	var backupPath string
	if journal.isComplete(stepBackupVerified) {
		backupPath = journal.BackupPath
	} else {
		backup, err := a.terminationBackup(access, item.StartedAt)
		if err != nil {
			return nil, err
		}
		backupPath = backup.Path
		journal.setBackupPath(backupPath)
		if err := journal.markComplete(stepBackupVerified); err != nil {
			return nil, fmt.Errorf("journal BACKUP_VERIFIED: %w", err)
		}
	}

	if a.Config.DBCtl == "" {
		return nil, errors.New("site termination requires the managed database helper")
	}

	// Step 2: DATABASES_REMOVED. runDatabaseTermination invokes the
	// managed-database helper's "drop-managed" verb, which is
	// idempotent on the DB side; a retry after a crash mid-step is safe.
	if !journal.isComplete(stepDatabasesRemoved) {
		databases, err := managedDatabaseInventory(a.Config)
		if err != nil {
			return nil, fmt.Errorf("inspect managed databases before termination: %w", err)
		}
		for _, database := range databases {
			if database.Site != request.Site {
				continue
			}
			if err := runDatabaseTermination(ctx, a.Config, database); err != nil {
				return nil, err
			}
		}
		if err := journal.markComplete(stepDatabasesRemoved); err != nil {
			return nil, fmt.Errorf("journal DATABASES_REMOVED: %w", err)
		}
	}

	// Step 3: ROUTES_REMOVED. Desired-state removal MUST precede live
	// route deletion so a crash between them cannot leave the startup
	// reconciler able to recreate a route for a site whose filesystem
	// is already gone.
	if !journal.isComplete(stepRoutesRemoved) {
		if a.Routes != nil {
			if err := a.Routes.removeSite(access); err != nil {
				return nil, fmt.Errorf("remove route desired state before termination: %w", err)
			}
		}
		for _, route := range siteRoutesFor(a.Config.VHostRoot, request.Site) {
			if err := a.deleteManagedRoute(ctx, a.Config.VHostCtl, routeConfigName(a.Config, route)); err != nil {
				return nil, err
			}
		}
		if err := journal.markComplete(stepRoutesRemoved); err != nil {
			return nil, fmt.Errorf("journal ROUTES_REMOVED: %w", err)
		}
	}

	// Step 4: PROXIES_REMOVED.
	if !journal.isComplete(stepProxiesRemoved) {
		for _, proxy := range siteProxiesFor(a.Config.ProxyRoot, request.Site) {
			if err := a.deleteManagedRoute(ctx, a.Config.ProxyCtl, filepath.Base(proxy.Config)); err != nil {
				return nil, err
			}
		}
		if err := journal.markComplete(stepProxiesRemoved); err != nil {
			return nil, fmt.Errorf("journal PROXIES_REMOVED: %w", err)
		}
	}

	// Step 5: TASKS_REMOVED.
	if !journal.isComplete(stepTasksRemoved) {
		if err := a.removeSiteTasks(ctx, access); err != nil {
			return nil, err
		}
		if err := journal.markComplete(stepTasksRemoved); err != nil {
			return nil, fmt.Errorf("journal TASKS_REMOVED: %w", err)
		}
	}

	// Step 6: SERVICES_REMOVED.
	if !journal.isComplete(stepServicesRemoved) {
		if err := a.removeSiteServices(ctx, access); err != nil {
			return nil, err
		}
		if err := journal.markComplete(stepServicesRemoved); err != nil {
			return nil, fmt.Errorf("journal SERVICES_REMOVED: %w", err)
		}
	}

	// Step 7: SITE_STATE_REMOVED.
	if !journal.isComplete(stepSiteStateRemoved) {
		if err := a.removeSiteState(ctx, access); err != nil {
			return nil, err
		}
		if err := journal.markComplete(stepSiteStateRemoved); err != nil {
			return nil, fmt.Errorf("journal SITE_STATE_REMOVED: %w", err)
		}
	}

	// Step 8: OWNERSHIP_REMOVED.
	if !journal.isComplete(stepOwnershipRemoved) {
		if err := a.detachSiteOwnership(access); err != nil {
			return nil, err
		}
		if err := journal.markComplete(stepOwnershipRemoved); err != nil {
			return nil, fmt.Errorf("journal OWNERSHIP_REMOVED: %w", err)
		}
	}

	// COMPLETED. Emit the terminal audit event durably before removing
	// the journal — if audit persistence fails here, we prefer to keep
	// the journal on disk (retries then re-emit the audit event) rather
	// than lose the record. Upgraded from the previous `_ = ShouldAudit`
	// to a checked AuditAs call so a persistence failure fails the job.
	if err := AuditAs(a.Config.AuditLog, request.Actor, "site.terminated", request.Site, "verified backup="+backupPath); err != nil {
		return nil, fmt.Errorf("record termination completion: %w", err)
	}
	if err := journal.cleanup(); err != nil {
		// The termination is complete and audited; a leftover journal
		// file is a housekeeping issue, not a correctness one. Report
		// it as a job error so the operator notices, but the returned
		// error does not undo the termination.
		return nil, fmt.Errorf("cleanup termination journal: %w", err)
	}
	return json.Marshal(map[string]any{
		"site":         request.Site,
		"backup":       map[string]string{"path": backupPath},
		"completed_at": time.Now().UTC(),
	})
}

func (a *App) terminationBackup(site SiteCapability, started time.Time) (BackupResult, error) {
	items, err := listBackupsPage(a.Config.BackupRoot, site, 500, a.Config.BackupSigningKey)
	if err != nil {
		return BackupResult{}, fmt.Errorf("inspect retained site backups: %w", err)
	}
	for _, item := range items {
		if item.VerifiedAt.IsZero() || item.VerifiedAt.Before(started) {
			continue
		}
		if err := a.ensureTerminationOffsiteBackup(item); err != nil {
			return BackupResult{}, err
		}
		return item, nil
	}
	result, err := CreateSiteBackup(a.Config, site, true)
	if err != nil {
		return BackupResult{}, fmt.Errorf("create verified termination backup: %w", err)
	}
	if err := a.ensureTerminationOffsiteBackup(result); err != nil {
		return BackupResult{}, err
	}
	return result, nil
}

// ensureTerminationOffsiteBackup enforces STEPANEL_REQUIRE_OFFSITE_BACKUP for
// the one operation where losing a site's only copy is irreversible: deleting
// its host state. A scheduled/manual backup job already blocks on offsite
// upload success (see handleSiteBackupJob), but termination can otherwise
// pick up a locally-verified backup that never left the host, silently
// defeating the operator's offsite requirement right before the data it
// protects is destroyed.
func (a *App) ensureTerminationOffsiteBackup(result BackupResult) error {
	if !a.Config.RequireOffsiteBackup {
		return nil
	}
	if err := uploadOffsite(a.Config, result); err != nil {
		return fmt.Errorf("offsite backup is required before site termination: %w", err)
	}
	return nil
}

func runDatabaseTermination(ctx context.Context, cfg Config, database DatabaseResource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	output, err := runDatabaseHelper(cfg, time.Minute, "", "drop-managed", database.Name, database.User)
	if err != nil {
		return fmt.Errorf("drop managed database %s: %w: %s", database.Name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (a *App) deleteManagedRoute(ctx context.Context, helper, name string) error {
	if helper == "" {
		return fmt.Errorf("cannot remove managed route %s: helper is unavailable", name)
	}
	if err := runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, helper, "delete", name); err != nil {
		return fmt.Errorf("remove managed route %s: %w", name, err)
	}
	return nil
}

func routeConfigName(cfg Config, route siteRoute) string {
	return siteVHostConfigName(cfg.WebServer, route.Site, route.Domain)
}

func (a *App) removeSiteServices(ctx context.Context, site SiteCapability) error {
	siteName := site.Site()
	hasApplication := false
	for _, app := range managedApps(a.Config.AppRoot) {
		if app.Site == siteName {
			hasApplication = true
			break
		}
	}
	if hasApplication && a.Config.AppCtl == "" {
		return errors.New("managed application services exist but the application helper is unavailable")
	}
	if a.Config.AppCtl != "" {
		if err := runHelperCommandWithTimeout(ctx, a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "delete", siteName); err != nil {
			return fmt.Errorf("remove managed application services for %s: %w", siteName, err)
		}
	}
	if a.Config.GitCtl != "" {
		if err := runHelperCommandWithTimeout(ctx, a.Config, helperServiceLifecycleTimeout, a.Config.GitCtl, "delete", siteName); err != nil {
			return fmt.Errorf("remove Git deploy key for %s: %w", siteName, err)
		}
	}
	if a.Config.SiteCtl == "" {
		return errors.New("site teardown helper is unavailable")
	}
	if err := runHelperCommandWithTimeout(ctx, a.Config, helperServiceLifecycleTimeout, a.Config.SiteCtl, "delete", siteName); err != nil {
		return fmt.Errorf("remove PHP, SSH, quota, and site filesystem state for %s: %w", siteName, err)
	}
	// The helper tears down host identities and services. The lifecycle
	// manager owns the final path-safe filesystem cleanup contract, so a
	// helper implementation cannot silently broaden deletion scope. The
	// operation is idempotent because the helper may already have removed the
	// directory.
	if a.siteManager != nil {
		if err := a.siteManager.Delete(ctx, siteName); err != nil {
			return fmt.Errorf("finalize managed site deletion for %s: %w", siteName, err)
		}
	}
	return nil
}

func (a *App) removeSiteTasks(ctx context.Context, site SiteCapability) error {
	if a.Tasks == nil {
		return nil
	}
	siteName := site.Site()
	a.Tasks.mu.RLock()
	tasks := make([]ScheduledTask, 0)
	for _, task := range a.Tasks.values {
		if task.Site == siteName {
			tasks = append(tasks, task)
		}
	}
	a.Tasks.mu.RUnlock()
	for _, task := range tasks {
		if err := runHelperCommandWithTimeout(ctx, a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "task-delete", siteName, task.Name); err != nil {
			return fmt.Errorf("remove scheduled task %s/%s: %w", siteName, task.Name, err)
		}
	}
	return nil
}

func (a *App) removeSiteState(_ context.Context, site SiteCapability) error {
	if a.Routes != nil {
		if err := a.Routes.removeSite(site); err != nil {
			return fmt.Errorf("remove route desired state: %w", err)
		}
	}
	if a.Domains != nil {
		if err := a.Domains.removeSite(site); err != nil {
			return fmt.Errorf("remove domain claim state: %w", err)
		}
	}
	siteName := site.Site()
	if a.Access != nil {
		a.Access.mu.Lock()
		delete(a.Access.values, siteName)
		err := a.Access.persistLocked()
		a.Access.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if a.Environments != nil {
		a.Environments.mu.Lock()
		had := a.Environments.values[siteName] != nil
		delete(a.Environments.values, siteName)
		err := a.Environments.persistLocked()
		a.Environments.mu.Unlock()
		if err != nil && had {
			return fmt.Errorf("remove site environment state: %w", err)
		}
	}
	if a.Redis != nil {
		a.Redis.mu.Lock()
		delete(a.Redis.values, siteName)
		err := a.Redis.persistLocked()
		a.Redis.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove Redis allocation state: %w", err)
		}
	}
	if a.Resources != nil {
		a.Resources.mu.Lock()
		delete(a.Resources.values, siteName)
		err := a.Resources.persistLocked()
		a.Resources.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove resource profile state: %w", err)
		}
	}
	if a.PHP != nil {
		a.PHP.mu.Lock()
		delete(a.PHP.values, siteName)
		err := persistPHPProfilesLocked(a.PHP)
		a.PHP.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove PHP profile state: %w", err)
		}
	}
	if a.Composer != nil {
		a.Composer.mu.Lock()
		delete(a.Composer.latest, siteName)
		err := persistComposerLocked(a.Composer)
		a.Composer.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove Composer state: %w", err)
		}
	}
	if a.Workers != nil {
		a.Workers.mu.Lock()
		for key, value := range a.Workers.values {
			if value.Site == siteName {
				delete(a.Workers.values, key)
			}
		}
		err := a.Workers.persistLocked()
		a.Workers.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove worker state: %w", err)
		}
	}
	if a.Tasks != nil {
		a.Tasks.mu.Lock()
		for key, value := range a.Tasks.values {
			if value.Site == siteName {
				delete(a.Tasks.values, key)
			}
		}
		err := a.Tasks.persistLocked()
		a.Tasks.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove scheduled task state: %w", err)
		}
	}
	return nil
}

func persistPHPProfilesLocked(store *PHPProfileStore) error {
	data, err := json.Marshal(store.values)
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(store, data); bound {
		return err
	}
	return writeAtomic(store.path, append(data, '\n'), 0600)
}

func persistComposerLocked(store *ComposerStore) error {
	data, err := json.Marshal(store.latest)
	if err != nil {
		return err
	}
	if bound, err := persistBoundControlPlaneState(store, data); bound {
		return err
	}
	return writeAtomic(store.path, append(data, '\n'), 0600)
}

func (a *App) detachSiteOwnership(site SiteCapability) error {
	if a.Accounts == nil {
		return nil
	}
	siteName := site.Site()
	owner, ok := a.Accounts.OwnerOfSite(siteName)
	if !ok {
		return nil
	}
	account, ok := a.Accounts.Get(owner)
	if !ok {
		return fmt.Errorf("site %s has missing owning account %s", siteName, owner)
	}
	remaining := make([]string, 0, len(account.Sites))
	for _, assigned := range account.Sites {
		if assigned != siteName {
			remaining = append(remaining, assigned)
		}
	}
	if _, err := a.Accounts.Update(owner, account.Plan, remaining); err != nil {
		return fmt.Errorf("detach site ownership: %w", err)
	}
	if a.Resources != nil && a.Config.AppCtl != "" {
		if plan, exists := hostingPlans[account.Plan]; exists && remaining != nil {
			if err := a.applyAccountResourceEnvelope(owner, plan); err != nil {
				return fmt.Errorf("reconcile remaining account resources: %w", err)
			}
		}
	}
	return nil
}
