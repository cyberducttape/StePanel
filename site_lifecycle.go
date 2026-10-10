package stepanel

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

	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"github.com/cyberducttape/StePanel/internal/sitelifecycle"
)

type durableSiteTerminationRequest struct {
	Site  string `json:"site"`
	Actor string `json:"actor"`
}

type terminationPlanCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass, warning, blocker
	Detail string `json:"detail"`
}

type terminationPlan struct {
	Operation     string                 `json:"operation"`
	Site          string                 `json:"site"`
	Ready         bool                   `json:"ready"`
	Preconditions []terminationPlanCheck `json:"preconditions"`
	Changes       []string               `json:"changes"`
	Rollback      string                 `json:"rollback"`
}

func (a *App) buildTerminationPlan(site string) terminationPlan {
	plan := terminationPlan{
		Operation: "terminate",
		Site:      site,
		Changes: []string{
			"Acquire the site mutation lease",
			"Create or retain a verified termination backup",
			"Disable routes and remove managed services",
			"Remove managed databases and site state",
			"Preserve the recovery journal until every step commits",
			"Remove the canonical site tree",
		},
		Rollback: "Available through the retained verified backup; termination itself is not automatically reversible.",
	}
	checks := []terminationPlanCheck{{Name: "Site document root", Status: "pass", Detail: "site document root exists"}}
	if a.Config.DBCtl == "" {
		checks = append(checks, terminationPlanCheck{Name: "Managed database helper", Status: "blocker", Detail: "no database helper is configured"})
	} else {
		checks = append(checks, terminationPlanCheck{Name: "Managed database helper", Status: "pass", Detail: "configured helper will perform idempotent database cleanup"})
	}
	checks = append(checks, terminationPlanCheck{Name: "Verified backup", Status: "warning", Detail: "no existing recovery artifact is asserted; execution will create and verify a final backup before destructive steps"})
	if a.Jobs != nil {
		jobs, err := a.Jobs.ListActiveForSite(site)
		if err != nil {
			checks = append(checks, terminationPlanCheck{Name: "Conflicting jobs", Status: "blocker", Detail: "durable job state is unavailable: " + err.Error()})
		} else {
			for _, job := range jobs {
				if job.Kind != "site.terminate" {
					checks = append(checks, terminationPlanCheck{Name: "Conflicting jobs", Status: "blocker", Detail: fmt.Sprintf("job %s (%s) is %s", job.ID, job.Kind, job.State)})
				}
			}
		}
	}
	plan.Preconditions = checks
	plan.Ready = true
	for _, check := range checks {
		if check.Status == "blocker" {
			plan.Ready = false
			break
		}
	}
	return plan
}

// enqueueSiteTermination records a destructive lifecycle operation before any
// host state is changed. The site is the serialization key, so a retry or a
// duplicate request cannot run two teardown workflows concurrently.
func (a *App) enqueueSiteTermination(site, actor string, operationKeys ...string) (Job, error) {
	operationKey := ""
	if len(operationKeys) > 0 {
		operationKey = operationKeys[0]
	}
	payload, err := json.Marshal(durableSiteTerminationRequest{Site: site, Actor: actor})
	if err != nil {
		return Job{}, err
	}
	job, _, err := a.Jobs.EnqueueIdempotent("site.terminate", site, operationKey, payload, 5)
	return job, err
}

func (a *App) siteTermination(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}
	operationKey, err := requestOperationKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	var input struct {
		Site         string `json:"site"`
		Confirmation string `json:"confirmation"`
		DryRun       bool   `json:"dry_run,omitempty"`
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
	if input.DryRun {
		writeJSON(w, http.StatusOK, buildTerminationPlanResponse(a.buildTerminationPlan(input.Site)))
		return
	}
	job, err := a.enqueueSiteTermination(input.Site, a.Auth.UsernameForRequest(r), operationKey)
	if err != nil {
		http.Error(w, "could not persist site termination job", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID})
}

func buildTerminationPlanResponse(plan terminationPlan) map[string]any {
	return map[string]any{"plan": plan}
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
	operationCtx, release, err := a.acquireSiteMutationLockContext(ctx, request.Site)
	if err != nil {
		return nil, fmt.Errorf("acquire durable site lock: %w", err)
	}
	defer release()

	journal, err := loadOrCreateTerminationJournal(a.Config.RecoveryRoot, item.ID, request.Site, request.Actor)
	if err != nil {
		return nil, fmt.Errorf("prepare termination journal: %w", err)
	}

	// The step sequence, its recovery gate and its audit rules live in the
	// sitelifecycle domain package; this handler only supplies authority
	// (the authorized site capability under the site lock) and host access.
	backupPath, err := sitelifecycle.Termination{
		JobID:   item.ID,
		Journal: lifecycleJournal{journal},
		Host:    &terminationHost{app: a, access: access, site: request.Site, startedAt: item.StartedAt},
		Audit:   lifecycleAuditor{log: a.Config.AuditLog, actor: request.Actor, site: request.Site},
		Faults:  lifecycleFaults{operation: "terminate"},
	}.Run(operationCtx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"site":         request.Site,
		"backup":       map[string]string{"path": backupPath},
		"completed_at": time.Now().UTC(),
	})
}

// lifecycleAuditor persists sitelifecycle audit events, failing closed.
type lifecycleAuditor struct {
	log, actor, site string
}

func (l lifecycleAuditor) Audit(action, detail string) error {
	return SecurityAuditRequired(l.log, l.actor, action, l.site, detail)
}

// lifecycleFaults routes sitelifecycle stages to the failure-drill hooks.
type lifecycleFaults struct{ operation string }

func (l lifecycleFaults) Fail(stage string) error { return failureInjection(l.operation, stage) }
func (l lifecycleFaults) Kill(stage string)       { processKillInjection(l.operation, stage) }

// terminationHost implements sitelifecycle.TerminationHost for one
// authorized site. It is the privilege seam of termination: every method
// reaches host state only through the configured helpers and stores.
type terminationHost struct {
	app       *App
	access    SiteCapability
	site      string
	startedAt time.Time
}

func (h *terminationHost) VerifiedBackup(ctx context.Context) (string, error) {
	backup, err := h.app.terminationBackup(ctx, h.access, h.startedAt)
	if err != nil {
		return "", err
	}
	return backup.Path, nil
}

func (h *terminationHost) CheckTeardownPrerequisites() error {
	if h.app.Config.DBCtl == "" {
		return errors.New("site termination requires the managed database helper")
	}
	return nil
}

// RemoveDatabases invokes the managed-database helper's idempotent
// "drop-managed" verb for each of the site's databases.
func (h *terminationHost) RemoveDatabases(ctx context.Context) error {
	databases, err := managedDatabaseInventory(h.app.Config)
	if err != nil {
		return fmt.Errorf("inspect managed databases before termination: %w", err)
	}
	for _, database := range databases {
		if database.Site != h.site {
			continue
		}
		if err := runDatabaseTermination(ctx, h.app.Config, database); err != nil {
			return err
		}
	}
	return nil
}

func (h *terminationHost) RemoveRoutes(ctx context.Context) error {
	if h.app.Routes != nil {
		if err := h.app.Routes.removeSite(h.access); err != nil {
			return fmt.Errorf("remove route desired state before termination: %w", err)
		}
	}
	routes, err := siteRoutesForWithError(h.app.Config.VHostRoot, h.site)
	if err != nil {
		return fmt.Errorf("inspect managed routes before termination: %w", err)
	}
	for _, route := range routes {
		if err := h.app.deleteManagedRoute(ctx, h.app.Config.VHostCtl, routeConfigName(h.app.Config, route)); err != nil {
			return err
		}
	}
	return nil
}

func (h *terminationHost) RemoveProxies(ctx context.Context) error {
	proxies, err := siteProxiesForWithError(h.app.Config.ProxyRoot, h.site)
	if err != nil {
		return fmt.Errorf("inspect managed proxies before termination: %w", err)
	}
	for _, proxy := range proxies {
		if err := h.app.deleteManagedRoute(ctx, h.app.Config.ProxyCtl, filepath.Base(proxy.Config)); err != nil {
			return err
		}
	}
	return nil
}

func (h *terminationHost) RemoveTasks(ctx context.Context) error {
	if err := h.app.removeSiteTasks(ctx, h.access); err != nil {
		return err
	}
	if err := h.app.removeSiteBackupSchedule(ctx, h.site); err != nil {
		return err
	}
	return nil
}

func (h *terminationHost) RemoveServices(ctx context.Context) error {
	return h.app.removeSiteServices(ctx, h.access)
}

func (h *terminationHost) RemoveSiteState(ctx context.Context) error {
	return h.app.removeSiteState(ctx, h.access)
}

func (h *terminationHost) DetachOwnership(ctx context.Context) error {
	return h.app.detachSiteOwnership(ctx, h.access)
}

func (a *App) terminationBackup(ctx context.Context, site SiteCapability, started time.Time) (BackupResult, error) {
	if err := ctx.Err(); err != nil {
		return BackupResult{}, err
	}
	items, err := listBackupsPage(a.Config.BackupRoot, site, 500, a.Config.backupVerificationKeys()...)
	if err != nil {
		return BackupResult{}, fmt.Errorf("inspect retained site backups: %w", err)
	}
	for _, item := range items {
		if item.VerifiedAt.IsZero() || item.VerifiedAt.Before(started) {
			continue
		}
		if err := a.ensureTerminationOffsiteBackup(ctx, item); err != nil {
			return BackupResult{}, err
		}
		return item, nil
	}
	result, err := CreateSiteBackupContext(ctx, a.Config, site, true)
	if err != nil {
		return BackupResult{}, fmt.Errorf("create verified termination backup: %w", err)
	}
	if err := a.ensureTerminationOffsiteBackup(ctx, result); err != nil {
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
func (a *App) ensureTerminationOffsiteBackup(ctx context.Context, result BackupResult) error {
	if !a.Config.RequireOffsiteBackup {
		return nil
	}
	if err := a.uploadOffsiteBackup(ctx, result); err != nil {
		return fmt.Errorf("offsite backup is required before site termination: %w", err)
	}
	return nil
}

func runDatabaseTermination(ctx context.Context, cfg Config, database DatabaseResource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	output, err := runDatabaseHelperContext(ctx, cfg, time.Minute, "", "drop-managed", database.Name, database.User)
	if err != nil {
		return fmt.Errorf("drop managed database %s: %w: %s", database.Name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (a *App) deleteManagedRoute(ctx context.Context, helper, name string) error {
	if helper == "" {
		return fmt.Errorf("cannot remove managed route %s: helper is unavailable", name)
	}
	if a.Config.Production {
		var err error
		switch filepath.Clean(helper) {
		case filepath.Clean(a.Config.VHostCtl):
			err = runVhostMutation(ctx, a.Config, "delete", name)
		case filepath.Clean(a.Config.ProxyCtl):
			err = runProxyMutation(ctx, a.Config, "delete", name)
		default:
			err = errors.New("managed route helper has no typed root-broker operation")
		}
		if err != nil {
			return fmt.Errorf("remove managed route %s: %w", name, err)
		}
		return nil
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
	labBroker, err := a.labRootBrokerClient()
	if err != nil {
		return err
	}
	hasApplication := false
	apps, err := managedAppsWithError(a.Config.AppRoot)
	if err != nil {
		return fmt.Errorf("inspect application manifests before termination: %w", err)
	}
	for _, app := range apps {
		if app.Site == siteName {
			hasApplication = true
			break
		}
	}
	if hasApplication && a.Config.AppCtl == "" {
		return errors.New("managed application services exist but the application helper is unavailable")
	}
	if a.Config.AppCtl != "" {
		var err error
		if labBroker != nil {
			resp, callErr := labBroker.AppDelete(ctx, siteName)
			if callErr != nil {
				err = callErr
			} else if !resp.OK {
				err = errors.New(resp.Error)
			}
		} else {
			err = runAppLifecycle(ctx, a.Config, "delete", siteName)
		}
		if err != nil {
			return fmt.Errorf("remove managed application services for %s: %w", siteName, err)
		}
	}
	if a.Config.GitCtl != "" {
		if err := deleteGitDeployKey(ctx, a.Config, siteName); err != nil {
			return fmt.Errorf("remove Git deploy key for %s: %w", siteName, err)
		}
	}
	if a.Config.SiteCtl == "" {
		return errors.New("site teardown helper is unavailable")
	}
	var helperErr error
	if labBroker != nil {
		resp, callErr := labBroker.SiteDelete(ctx, siteName)
		if callErr != nil {
			helperErr = callErr
		} else if !resp.OK {
			helperErr = errors.New(resp.Error)
		}
	} else {
		helperErr = runHelperCommandWithTimeout(ctx, a.Config, helperServiceLifecycleTimeout, a.Config.SiteCtl, "delete", siteName)
	}
	if helperErr != nil {
		return fmt.Errorf("remove PHP, SSH, quota, and site filesystem state for %s: %w", siteName, helperErr)
	}
	// The helper tears down host identities and services. The lifecycle
	// manager owns the final path-safe filesystem cleanup contract, so a
	// helper implementation cannot silently broaden deletion scope. The
	// operation is idempotent because the helper may already have removed the
	// directory. In the lab socket mode, the root helper has already removed
	// the complete tree; an unprivileged second walk would fail on root-owned
	// entries such as the PHP session directory.
	if a.siteManager != nil && labBroker == nil {
		if err := a.siteManager.Delete(ctx, siteName); err != nil {
			return fmt.Errorf("finalize managed site deletion for %s: %w", siteName, err)
		}
	}
	// Invalidate site-related metadata caches after deletion
	if a.MetadataCache != nil {
		a.MetadataCache.InvalidateSite(siteName)
	}
	return nil
}

func (a *App) labRootBrokerClient() (*rootbroker.Client, error) {
	if !labDirectRootBrokerEnabled() || strings.TrimSpace(os.Getenv("STEPANEL_LAB_ROOT_BROKER_SOCKET")) == "" {
		return nil, nil
	}
	return rootbroker.NewClient("/usr/local/sbin/stepanel-root", a.Config.WebRoot)
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
		task.Deleted = true
		if err := a.applyTask(ctx, task); err != nil {
			return fmt.Errorf("remove scheduled task %s/%s: %w", siteName, task.Name, err)
		}
	}
	return nil
}

func (a *App) removeSiteState(ctx context.Context, site SiteCapability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.Routes != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.Routes.removeSite(site); err != nil {
			return fmt.Errorf("remove route desired state: %w", err)
		}
	}
	if a.Domains != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.Domains.removeSite(site); err != nil {
			return fmt.Errorf("remove domain claim state: %w", err)
		}
	}
	siteName := site.Site()
	if a.Access != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.Access.mu.Lock()
		err := persistMapKeyChange(a.Access.values, siteName, (*SiteAccess)(nil), a.Access.persistLocked)
		a.Access.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if a.Environments != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.Environments.mu.Lock()
		err := persistMapKeyChange(a.Environments.values, siteName, (*map[string]environmentValue)(nil), a.Environments.persistLocked)
		a.Environments.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove site environment state: %w", err)
		}
	}
	if a.Redis != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.Redis.mu.Lock()
		err := persistMapKeyChange(a.Redis.values, siteName, (*RedisAllocation)(nil), a.Redis.persistLocked)
		a.Redis.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove Redis allocation state: %w", err)
		}
	}
	if a.Resources != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.Resources.mu.Lock()
		err := persistMapKeyChange(a.Resources.values, siteName, (*ResourceProfile)(nil), a.Resources.persistLocked)
		a.Resources.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove resource profile state: %w", err)
		}
	}
	if a.PHP != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.PHP.mu.Lock()
		err := persistMapKeyChange(a.PHP.values, siteName, (*PHPProfile)(nil), func() error { return persistPHPProfilesLocked(a.PHP) })
		a.PHP.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove PHP profile state: %w", err)
		}
	}
	if a.Composer != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.Composer.mu.Lock()
		err := persistMapKeyChange(a.Composer.latest, siteName, (*ComposerOperation)(nil), func() error { return persistComposerLocked(a.Composer) })
		a.Composer.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove Composer state: %w", err)
		}
	}
	if a.Workers != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.Workers.mu.Lock()
		err := persistMapFilter(a.Workers.values, func(_ string, value Worker) bool { return value.Site == siteName }, a.Workers.persistLocked)
		a.Workers.mu.Unlock()
		if err != nil {
			return fmt.Errorf("remove worker state: %w", err)
		}
	}
	if a.Tasks != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		a.Tasks.mu.Lock()
		err := persistMapFilter(a.Tasks.values, func(_ string, value ScheduledTask) bool { return value.Site == siteName }, a.Tasks.persistLocked)
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

func (a *App) detachSiteOwnership(ctx context.Context, site SiteCapability) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.Accounts == nil {
		return nil
	}
	siteName := site.Site()
	owner, ok, err := a.Accounts.OwnerOfSiteWithError(siteName)
	if err != nil {
		return fmt.Errorf("read ownership for site %s: %w", siteName, err)
	}
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := a.Accounts.Update(owner, account.Plan, remaining); err != nil {
		return fmt.Errorf("detach site ownership: %w", err)
	}
	if a.Resources != nil && a.Config.AppCtl != "" {
		if plan, exists := hostingPlans[account.Plan]; exists && remaining != nil {
			if err := a.applyAccountResourceEnvelopeContext(ctx, owner, plan); err != nil {
				return fmt.Errorf("reconcile remaining account resources: %w", err)
			}
		}
	}
	return nil
}
