package stepanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

// Reversible site suspension (ADR-0008). A row in site_suspensions is the
// desired state: while it exists the site may not publish or run code, and
// site mutations are refused where they take the site's mutation lock. The
// site.suspend and site.resume jobs converge host state. Every host step is
// idempotent, so a retry or startup reconciliation simply runs the whole
// sequence again.

const siteSuspensionTable = `CREATE TABLE IF NOT EXISTS site_suspensions (
	site TEXT PRIMARY KEY,
	state TEXT NOT NULL CHECK (state IN ('suspending', 'suspended', 'resuming')),
	reason TEXT NOT NULL,
	actor TEXT NOT NULL,
	requested_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	last_error TEXT NOT NULL DEFAULT ''
)`

const (
	siteSuspending = "suspending"
	siteSuspended  = "suspended"
	siteResuming   = "resuming"

	siteSuspendJobKind = "site.suspend"
	siteResumeJobKind  = "site.resume"

	siteSuspensionReasonMax = 500
)

// errSiteSuspended refuses a site mutation while the site is suspended. It is
// never worth retrying: the operator must resume the site first.
var errSiteSuspended = errors.New("site is suspended; an administrator must resume it first")

type siteSuspension struct {
	Site        string    `json:"site"`
	State       string    `json:"state"`
	Reason      string    `json:"reason"`
	Actor       string    `json:"actor"`
	RequestedAt time.Time `json:"requested_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastError   string    `json:"last_error,omitempty"`
}

type siteSuspensionStore struct {
	db *sql.DB
}

func newSiteSuspensionStore(db *sql.DB) (*siteSuspensionStore, error) {
	if db == nil {
		return nil, errors.New("site suspension store requires the control-plane database")
	}
	var exists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='site_suspensions'`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check site suspension schema: %w", err)
	}
	if exists != 1 {
		return nil, errors.New("site suspension schema is not initialized")
	}
	return &siteSuspensionStore{db: db}, nil
}

func scanSiteSuspension(row interface{ Scan(...any) error }) (siteSuspension, error) {
	var s siteSuspension
	var requested, updated int64
	if err := row.Scan(&s.Site, &s.State, &s.Reason, &s.Actor, &requested, &updated, &s.LastError); err != nil {
		return siteSuspension{}, err
	}
	s.RequestedAt, s.UpdatedAt = time.Unix(requested, 0).UTC(), time.Unix(updated, 0).UTC()
	return s, nil
}

const siteSuspensionColumns = `site, state, reason, actor, requested_at, updated_at, last_error`

func (s *siteSuspensionStore) get(site string) (siteSuspension, bool, error) {
	record, err := scanSiteSuspension(s.db.QueryRow(`SELECT `+siteSuspensionColumns+` FROM site_suspensions WHERE site = ?`, site))
	if errors.Is(err, sql.ErrNoRows) {
		return siteSuspension{}, false, nil
	}
	if err != nil {
		return siteSuspension{}, false, fmt.Errorf("read site suspension: %w", err)
	}
	return record, true, nil
}

func (s *siteSuspensionStore) list() ([]siteSuspension, error) {
	rows, err := s.db.Query(`SELECT ` + siteSuspensionColumns + ` FROM site_suspensions ORDER BY site`)
	if err != nil {
		return nil, fmt.Errorf("list site suspensions: %w", err)
	}
	defer rows.Close()
	result := []siteSuspension{}
	for rows.Next() {
		record, err := scanSiteSuspension(rows)
		if err != nil {
			return nil, fmt.Errorf("read site suspension: %w", err)
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

// requestSuspend records the desired suspension. A site that is already
// being suspended may be requested again to re-drive a failed job; any other
// existing state conflicts.
func (s *siteSuspensionStore) requestSuspend(site, reason, actor string) error {
	now := time.Now().UTC().Unix()
	result, err := s.db.Exec(`INSERT INTO site_suspensions(site, state, reason, actor, requested_at, updated_at)
		VALUES(?, 'suspending', ?, ?, ?, ?)
		ON CONFLICT(site) DO UPDATE SET reason = excluded.reason, actor = excluded.actor, updated_at = excluded.updated_at, last_error = ''
		WHERE site_suspensions.state = 'suspending'`, site, reason, actor, now, now)
	if err != nil {
		return fmt.Errorf("record site suspension: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return errSiteSuspensionConflict
	}
	return nil
}

// requestResume moves a suspended (or partly suspended) site to resuming.
// The suspension record stays until the resume job has restored the host.
func (s *siteSuspensionStore) requestResume(site, actor string) error {
	result, err := s.db.Exec(`UPDATE site_suspensions SET state = 'resuming', actor = ?, updated_at = ?, last_error = ''
		WHERE site = ?`, actor, time.Now().UTC().Unix(), site)
	if err != nil {
		return fmt.Errorf("record site resume: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return errSiteNotSuspended
	}
	return nil
}

// transition moves a record from one state to another, so a stale job can
// never overwrite a newer request.
func (s *siteSuspensionStore) transition(site, from, to string) error {
	result, err := s.db.Exec(`UPDATE site_suspensions SET state = ?, updated_at = ?, last_error = '' WHERE site = ? AND state = ?`,
		to, time.Now().UTC().Unix(), site, from)
	if err != nil {
		return fmt.Errorf("record site suspension state: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return fmt.Errorf("site %s is no longer %s", site, from)
	}
	return nil
}

func (s *siteSuspensionStore) finishResume(site string) error {
	result, err := s.db.Exec(`DELETE FROM site_suspensions WHERE site = ? AND state = 'resuming'`, site)
	if err != nil {
		return fmt.Errorf("clear site suspension: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return fmt.Errorf("site %s is no longer resuming", site)
	}
	return nil
}

// remove forgets any suspension of a site that no longer exists.
func (s *siteSuspensionStore) remove(site string) error {
	if _, err := s.db.Exec(`DELETE FROM site_suspensions WHERE site = ?`, site); err != nil {
		return fmt.Errorf("clear suspension of the terminated site: %w", err)
	}
	return nil
}

func (s *siteSuspensionStore) recordError(site string, cause error) {
	message := cause.Error()
	if len(message) > 1000 {
		message = message[:1000]
	}
	if _, err := s.db.Exec(`UPDATE site_suspensions SET last_error = ?, updated_at = ? WHERE site = ?`, message, time.Now().UTC().Unix(), site); err != nil {
		// The caller is already reporting cause; keep the lost diagnostic visible.
		log.Printf("record suspension error for site %s: %v (original error: %s)", site, err, message)
	}
}

var (
	errSiteSuspensionConflict = errors.New("site is already suspended or being resumed")
	errSiteNotSuspended       = errors.New("site is not suspended")
)

// suspensionExemptKey marks the few operations that may take a suspended
// site's mutation lock: suspend/resume themselves, termination, and backup
// schedule changes. Everything else is refused.
type suspensionExemptKey struct{}

func withSuspensionExempt(ctx context.Context) context.Context {
	return context.WithValue(ctx, suspensionExemptKey{}, true)
}

func suspensionExempt(ctx context.Context) bool {
	exempt, _ := ctx.Value(suspensionExemptKey{}).(bool)
	return exempt
}

// checkSiteNotSuspended is called with a site's mutation lock held, so a
// suspension recorded before the lock was granted is always seen. Lock keys
// of other resources (vhost:, database:, certificate:, ...) are not sites.
// A failed read refuses the mutation rather than risk running code for a
// suspended site.
func (a *App) checkSiteNotSuspended(ctx context.Context, key string) error {
	if a == nil || a.Suspensions == nil || strings.Contains(key, ":") || suspensionExempt(ctx) {
		return nil
	}
	_, suspended, err := a.Suspensions.get(key)
	if err != nil {
		return fmt.Errorf("check site suspension: %w", err)
	}
	if suspended {
		return doNotRetry(errSiteSuspended)
	}
	return nil
}

// siteIsSuspended reports whether a site has any suspension record. Read
// paths and reconcilers use it; mutations rely on the lock-time check.
func (a *App) siteIsSuspended(site string) bool {
	if a == nil || a.Suspensions == nil {
		return false
	}
	_, suspended, err := a.Suspensions.get(site)
	return err != nil || suspended
}

type durableSiteSuspensionRequest struct {
	Site  string `json:"site"`
	Actor string `json:"actor"`
}

// siteSuspend handles POST /api/sites/suspend {"site", "reason"}.
func (a *App) siteSuspend(w http.ResponseWriter, r *http.Request) {
	a.siteSuspensionRequest(w, r, true)
}

// siteResume handles POST /api/sites/resume {"site", "reason"}.
func (a *App) siteResume(w http.ResponseWriter, r *http.Request) {
	a.siteSuspensionRequest(w, r, false)
}

func (a *App) siteSuspensionRequest(w http.ResponseWriter, r *http.Request, suspend bool) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator CSRF request required", http.StatusForbidden)
		return
	}
	if a.Suspensions == nil || a.Jobs == nil {
		http.Error(w, "site suspension is unavailable", http.StatusServiceUnavailable)
		return
	}
	operationKey, err := requestOperationKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	var input struct {
		Site   string `json:"site"`
		Reason string `json:"reason"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	site := safeUser(strings.TrimSpace(input.Site))
	reason := strings.TrimSpace(input.Reason)
	if site == "" || site != strings.TrimSpace(input.Site) {
		http.Error(w, "invalid site", http.StatusUnprocessableEntity)
		return
	}
	if reason == "" || len(reason) > siteSuspensionReasonMax || strings.ContainsAny(reason, "\x00\r\n") {
		http.Error(w, fmt.Sprintf("a single-line reason of at most %d characters is required", siteSuspensionReasonMax), http.StatusUnprocessableEntity)
		return
	}
	if _, err := existingManagedDocRoot(a.Config, site); err != nil {
		http.Error(w, "site does not exist", http.StatusNotFound)
		return
	}
	if capability := a.checkSiteSuspensionCapability(); capability.Mode != CapabilityAvailable && capability.Mode != CapabilityLocal {
		http.Error(w, "site suspension is unavailable: "+capability.Reason, http.StatusServiceUnavailable)
		return
	}
	actor := a.Auth.AuditActor(r)
	action, kind := "site.suspend", siteSuspendJobKind
	if !suspend {
		action, kind = "site.resume", siteResumeJobKind
	}
	audit, err := BeginSecurityAudit(a.Config.AuditLog, actor, action, site, "reason="+reason)
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	outcome := ""
	failure := "request refused"
	defer func() { _ = audit.Finish(&outcome, failure) }()
	if suspend {
		err = a.Suspensions.requestSuspend(site, reason, actor)
	} else {
		err = a.Suspensions.requestResume(site, actor)
	}
	switch {
	case errors.Is(err, errSiteSuspensionConflict), errors.Is(err, errSiteNotSuspended):
		failure = err.Error()
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		failure = err.Error()
		http.Error(w, "could not record the request", http.StatusServiceUnavailable)
		return
	}
	payload, err := json.Marshal(durableSiteSuspensionRequest{Site: site, Actor: actor})
	if err != nil {
		failure = err.Error()
		http.Error(w, "could not queue the request", http.StatusInternalServerError)
		return
	}
	job, _, err := a.Jobs.EnqueueIdempotent(kind, site, operationKey, payload, 5)
	if err != nil {
		// The desired state is recorded; the startup reconciler or a repeated
		// request converges the host.
		failure = "desired state recorded but the job could not be queued: " + err.Error()
		writeJobEnqueueError(w, err, "could not persist the site suspension job")
		return
	}
	outcome = fmt.Sprintf("desired state recorded; job %s queued", job.ID)
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID, "status_url": "/api/jobs/" + job.ID})
}

// siteSuspensions handles GET /api/sites/suspensions for administrators.
func (a *App) siteSuspensions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !a.Auth.IsAdministrator(r) {
		http.Error(w, "administrator request required", http.StatusForbidden)
		return
	}
	if a.Suspensions == nil {
		writeJSON(w, http.StatusOK, map[string]any{"suspensions": []siteSuspension{}})
		return
	}
	records, err := a.Suspensions.list()
	if err != nil {
		http.Error(w, "site suspensions are unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suspensions": records})
}

func decodeSiteSuspensionJob(item Job) (durableSiteSuspensionRequest, error) {
	var request durableSiteSuspensionRequest
	if err := json.Unmarshal(item.Payload, &request); err != nil {
		return request, doNotRetry(fmt.Errorf("decode site suspension payload: %w", err))
	}
	if safeUser(request.Site) == "" || request.Site != item.User || request.Actor == "" {
		return request, doNotRetry(errors.New("site suspension requires its site and actor"))
	}
	return request, nil
}

func (a *App) handleSiteSuspendJob(ctx context.Context, item Job) ([]byte, error) {
	return a.runSiteSuspensionJob(ctx, item, true)
}

func (a *App) handleSiteResumeJob(ctx context.Context, item Job) ([]byte, error) {
	return a.runSiteSuspensionJob(ctx, item, false)
}

func (a *App) runSiteSuspensionJob(ctx context.Context, item Job, suspend bool) ([]byte, error) {
	request, err := decodeSiteSuspensionJob(item)
	if err != nil {
		return nil, err
	}
	if a.Suspensions == nil {
		return nil, errors.New("site suspension store is unavailable")
	}
	operationCtx, release, err := a.acquireSiteMutationLockContext(withSuspensionExempt(ctx), request.Site)
	if err != nil {
		return nil, fmt.Errorf("acquire durable site lock: %w", err)
	}
	defer release()
	record, exists, err := a.Suspensions.get(request.Site)
	if err != nil {
		return nil, err
	}
	want := siteSuspending
	if !suspend {
		want = siteResuming
	}
	// A later request superseded this job (for example a resume of a site
	// whose suspension had failed); that request's own job converges it.
	if !exists || record.State != want && !(suspend && record.State == siteSuspended) {
		return json.Marshal(map[string]any{"site": request.Site, "superseded": true})
	}
	if suspend {
		err = a.suspendSiteHost(operationCtx, request.Site)
	} else {
		err = a.resumeSiteHost(operationCtx, request.Site)
	}
	if err != nil {
		a.Suspensions.recordError(request.Site, err)
		return nil, err
	}
	if suspend {
		if record.State == siteSuspending {
			if err := a.Suspensions.transition(request.Site, siteSuspending, siteSuspended); err != nil {
				return nil, err
			}
		}
		if err := SecurityAuditRequired(a.Config.AuditLog, request.Actor, "site.suspended", request.Site, "reason="+record.Reason); err != nil {
			return nil, err
		}
	} else {
		if err := a.Suspensions.finishResume(request.Site); err != nil {
			return nil, err
		}
		if err := SecurityAuditRequired(a.Config.AuditLog, request.Actor, "site.resumed", request.Site, ""); err != nil {
			return nil, err
		}
	}
	state := "active"
	if suspend {
		state = siteSuspended
	}
	return json.Marshal(map[string]any{"site": request.Site, "state": state, "completed_at": time.Now().UTC()})
}

// suspendSiteHost applies every suspension step. Routes come first so that
// visitors get the 503 page even if a later step fails and is retried.
func (a *App) suspendSiteHost(ctx context.Context, site string) error {
	routes, err := siteRoutesForWithError(a.Config.VHostRoot, site)
	if err != nil {
		return fmt.Errorf("inspect routes: %w", err)
	}
	for _, route := range routes {
		if err := runVhostMutation(ctx, a.Config, "suspend", site, route.Domain); err != nil {
			return fmt.Errorf("serve the suspension page for %s: %w", route.Domain, err)
		}
	}
	if err := a.runSiteSuspensionHelper(ctx, "php-suspend", site); err != nil {
		return fmt.Errorf("disable PHP-FPM: %w", err)
	}
	if err := a.runAppSuspensionHelper(ctx, "suspend", site); err != nil {
		return fmt.Errorf("stop managed applications, workers and tasks: %w", err)
	}
	if placed, err := sitePlacedInAccount(site); err != nil {
		return err
	} else if !placed {
		if access, configured := a.storedSiteAccess(site); configured {
			revoked := SiteAccess{Site: access.Site, Keys: []SSHKey{}}
			if err := a.applySiteAccess(ctx, revoked); err != nil {
				return fmt.Errorf("revoke SSH/SFTP access: %w", err)
			}
		}
		if err := a.runSiteSuspensionHelper(ctx, "login-lock", site); err != nil {
			return fmt.Errorf("lock the site account password: %w", err)
		}
	}
	if err := a.setSiteDatabaseLogins(ctx, site, "suspend-logins"); err != nil {
		return err
	}
	return ctx.Err()
}

// resumeSiteHost reverses suspendSiteHost in the opposite order, restoring
// routes last so the site is served only once it can run.
func (a *App) resumeSiteHost(ctx context.Context, site string) error {
	if err := a.setSiteDatabaseLogins(ctx, site, "resume-logins"); err != nil {
		return err
	}
	if placed, err := sitePlacedInAccount(site); err != nil {
		return err
	} else if !placed {
		if err := a.runSiteSuspensionHelper(ctx, "login-unlock", site); err != nil {
			return fmt.Errorf("unlock the site account password: %w", err)
		}
		if access, configured := a.storedSiteAccess(site); configured {
			if err := a.applySiteAccess(ctx, access); err != nil {
				return fmt.Errorf("restore SSH/SFTP access: %w", err)
			}
		}
	}
	if err := a.runAppSuspensionHelper(ctx, "resume", site); err != nil {
		return fmt.Errorf("restore managed applications, workers and tasks: %w", err)
	}
	if err := a.runSiteSuspensionHelper(ctx, "php-resume", site); err != nil {
		return fmt.Errorf("re-enable PHP-FPM: %w", err)
	}
	routes, err := siteRoutesForWithError(a.Config.VHostRoot, site)
	if err != nil {
		return fmt.Errorf("inspect routes: %w", err)
	}
	for _, route := range routes {
		if err := runVhostMutation(ctx, a.Config, "resume", site, route.Domain); err != nil {
			return fmt.Errorf("restore the route for %s: %w", route.Domain, err)
		}
	}
	return ctx.Err()
}

// storedSiteAccess returns the site's recorded SSH/SFTP policy. A site with
// no record has had no keys installed, so there is nothing to revoke or
// restore beyond the account password.
func (a *App) storedSiteAccess(site string) (SiteAccess, bool) {
	if a.Access == nil {
		return SiteAccess{}, false
	}
	a.Access.mu.RLock()
	defer a.Access.mu.RUnlock()
	access, ok := a.Access.values[site]
	if !ok || access.Site == "" {
		return SiteAccess{}, false
	}
	access.Keys = append([]SSHKey(nil), access.Keys...)
	return access, true
}

func sitePlacedInAccount(site string) (bool, error) {
	db := sitePlacementDB.Load()
	if db == nil {
		return false, nil
	}
	_, err := sitelayout.Get(db, site)
	if errors.Is(err, sitelayout.ErrNotPlaced) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read site placement: %w", err)
	}
	return true, nil
}

func (a *App) setSiteDatabaseLogins(ctx context.Context, site, action string) error {
	if a.Config.DBCtl == "" {
		return nil
	}
	databases, err := managedDatabaseInventory(a.Config)
	if err != nil {
		return fmt.Errorf("inspect managed databases: %w", err)
	}
	for _, database := range databases {
		// Only databases with a managed account have logins to lock; an
		// imported cPanel database has no account of its own.
		if database.Site != site || database.User == "" {
			continue
		}
		if output, err := runDatabaseHelperContext(ctx, a.Config, time.Minute, "", action, database.Name, site); err != nil {
			return fmt.Errorf("%s for database %s: %w: %s", action, database.Name, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func (a *App) runSiteSuspensionHelper(ctx context.Context, action, site string) error {
	if a.Config.Production {
		return runTypedSiteMutation(ctx, a.Config, rootbroker.SiteRequest{Action: action, Site: site})
	}
	return runHelperCommandWithTimeout(ctx, a.Config, helperConfigMutationTimeout, a.Config.SiteCtl, action, site)
}

func (a *App) runAppSuspensionHelper(ctx context.Context, action, site string) error {
	if handled, err := runAppBroker(ctx, a.Config, rootbroker.AppRequest{Action: action, Site: site}); handled {
		return err
	}
	return runHelperCommandWithTimeout(ctx, a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, action, site)
}

// reconcileSiteSuspensions re-applies host suspension for every suspended
// site after a restart, so a reboot or a hand edit cannot bring one back.
// Sites still suspending or resuming belong to their durable jobs.
func (a *App) reconcileSiteSuspensions(ctx context.Context) (reconciled []string, failed map[string]string) {
	failed = map[string]string{}
	if a.Suspensions == nil {
		return nil, failed
	}
	records, err := a.Suspensions.list()
	if err != nil {
		return nil, map[string]string{"state": err.Error()}
	}
	for _, record := range records {
		if record.State != siteSuspended {
			continue
		}
		operationCtx, release, err := a.acquireSiteMutationLockContext(withSuspensionExempt(ctx), record.Site)
		if err != nil {
			failed[record.Site] = err.Error()
			continue
		}
		if err := a.suspendSiteHost(operationCtx, record.Site); err != nil {
			a.Suspensions.recordError(record.Site, err)
			failed[record.Site] = err.Error()
			release()
			continue
		}
		reconciled = append(reconciled, record.Site)
		release()
	}
	return reconciled, failed
}

func (a *App) checkSiteSuspensionCapability() Capability {
	if a.Suspensions == nil || a.Jobs == nil {
		return newCapability(CapabilityUnsupported, "the durable site suspension store is not initialized")
	}
	if a.Config.WebServer != "caddy" && a.Config.WebServer != "apache" {
		return newCapability(CapabilityUnsupported, "site suspension supports Caddy and Apache only")
	}
	for name, helper := range map[string]string{"site": a.Config.SiteCtl, "application": a.Config.AppCtl, "web server": a.Config.VHostCtl} {
		if helper == "" {
			return newCapability(CapabilityUnsupported, "the "+name+" helper is required")
		}
		if !isExecutableRegularFile(helper) {
			return newCapability(CapabilityConfigured, "the "+name+" helper path must be an executable regular file")
		}
	}
	return newCapability(CapabilityLocal, "site, application and web server helpers are configured; a full suspend and resume is not verified by this probe")
}
