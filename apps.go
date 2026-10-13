package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
	"golang.org/x/sys/unix"
)

type AppManifest struct {
	Site    string `json:"site"`
	Domain  string `json:"domain"`
	Version string `json:"node_version"`
	Port    int    `json:"port"`
	Root    string `json:"root"`
	State   string `json:"state"`
}

func (a *App) appList(w http.ResponseWriter, r *http.Request) {
	apps, err := managedAppsWithError(a.Config.AppRoot)
	if err != nil {
		http.Error(w, "unable to inspect application manifests", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

func (a *App) appDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var app AppManifest
	if err := decodeJSON(w, r, 8192, &app); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	app.Domain = strings.ToLower(strings.TrimSpace(app.Domain))
	if safeUser(app.Site) == "" || len(app.Domain)+len(app.Site) > 220 || !domainname.Valid(app.Domain) || !nodeVersionPattern.MatchString(app.Version) || app.Port < 1024 || app.Port > 65535 {
		http.Error(w, "invalid site, Node version, or port", 422)
		return
	}
	if _, err := localBackend("http://127.0.0.1:" + strconv.Itoa(app.Port)); err != nil {
		writePublicError(w, r, http.StatusUnprocessableEntity, publicError("invalid_backend", "the application backend is invalid", err))
		return
	}
	if _, ok := a.requireSiteAccess(w, r, app.Site, "site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for app deployment", http.StatusForbidden)
		return
	}
	var rootErr error
	app.Root, rootErr = safePath(a.Config.WebRoot, "sites", app.Site, "public")
	if rootErr != nil {
		http.Error(w, "invalid site root", 422)
		return
	}
	if info, err := os.Stat(app.Root); err != nil || !info.IsDir() {
		http.Error(w, "site document root does not exist", 422)
		return
	}
	operationCtx := r.Context()
	if _, internal := operationCtx.Value(nodeDeploymentContextKey{}).(bool); !internal {
		var releaseUnlock func()
		var lockErr error
		operationCtx, releaseUnlock, lockErr = a.acquireSiteMutationLockContext(operationCtx, app.Site)
		if lockErr != nil {
			http.Error(w, "app deployment is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "app deployment cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	if err := os.MkdirAll(a.Config.AppRoot, 0750); err != nil {
		http.Error(w, "unable to create app state directory", 500)
		return
	}
	previous, hadPrevious, previousErr := readAppStateFile(a.Config, app.Site, ".json")
	if previousErr != nil {
		http.Error(w, "unable to read existing app manifest", 500)
		return
	}
	var previousManifest *AppManifest
	if hadPrevious {
		var parsed AppManifest
		if err := json.Unmarshal(previous, &parsed); err != nil || !validAppManifest(a.Config, parsed, app.Site) {
			http.Error(w, "existing app manifest is invalid; refusing to replace unmanaged runtime state", http.StatusConflict)
			return
		}
		previousManifest = &parsed
	}
	previousBackup, hadPreviousBackup, backupReadErr := readAppStateFile(a.Config, app.Site, ".json.bak")
	if backupReadErr != nil {
		http.Error(w, "unable to read existing app rollback state", 500)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "app deployment cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	deployment := app.Domain + " on port " + strconv.Itoa(app.Port)
	intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "app.deployed", app.Site, deployment)
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	var outcome string
	defer intent.Finish(&outcome, "application service was not applied; previous configuration kept")
	app.State = "running"
	cleanupPending, err := activateAppManifest(operationCtx, a.Config, app, previous, previousManifest, previousBackup, hadPreviousBackup)
	if err != nil {
		var activationErr *appActivationFailure
		if errors.As(err, &activationErr) && activationErr.RecoveryErr != nil {
			outcome = deployment + "; rollback incomplete: " + activationErr.RecoveryErr.Error()
			log.Printf("app deploy %s: activation failed and recovery is pending: %v", app.Site, err)
			http.Error(w, "application activation failed and rollback is incomplete; operator recovery is required", http.StatusServiceUnavailable)
			return
		}
		log.Printf("app deploy %s: activation failed; previous generation restored: %v", app.Site, err)
		http.Error(w, "application service could not be applied; the previous configuration was restored", http.StatusBadGateway)
		return
	}
	if cleanupPending {
		log.Printf("app deploy %s: committed activation journal cleanup is pending startup recovery", app.Site)
	}
	outcome = deployment
	a.invalidateAppsCache()
	writeJSON(w, http.StatusAccepted, app)
}

func restoreAppActivation(cfg Config, ctx context.Context, journal *appActivationJournal) error {
	if journal == nil {
		return errors.New("application activation journal is missing")
	}
	manifestPath, err := safePath(cfg.AppRoot, journal.Site+".json")
	if err != nil || filepath.Clean(journal.ManifestPath) != filepath.Clean(manifestPath) {
		return errors.New("application activation journal contains an invalid manifest path")
	}
	if journal.HadPrevious && journal.RestoreManifest != nil {
		err = restoreAppProcess(ctx, cfg, *journal.RestoreManifest)
	} else if !journal.HadPrevious {
		err = runAppLifecycle(ctx, cfg, "stop", journal.Site)
	}
	if err != nil {
		return err
	}
	if journal.HadPrevious {
		if err := writeAtomic(manifestPath, journal.Previous, 0600); err != nil {
			return err
		}
	} else if err := removeAppManifest(cfg, journal.Site, manifestPath); err != nil {
		return err
	}
	if journal.Version >= 2 {
		if err := restoreAppBackup(cfg, journal.Site, journal.PreviousBackup, journal.HadPreviousBackup); err != nil {
			return err
		}
	}
	return journal.cleanup()
}

type appActivationFailure struct {
	Cause       error
	RecoveryErr error
}

func (e *appActivationFailure) Error() string {
	if e.RecoveryErr != nil {
		return fmt.Sprintf("activation failed: %v; recovery failed: %v", e.Cause, e.RecoveryErr)
	}
	return fmt.Sprintf("activation failed and previous state was restored: %v", e.Cause)
}

func (e *appActivationFailure) Unwrap() error { return e.Cause }

// activateAppManifest journals both runtime activation and manifest/rollback
// metadata publication. The committed state is durable before journal cleanup,
// so startup can distinguish a completed activation from work to compensate.
func activateAppManifest(ctx context.Context, cfg Config, target AppManifest, previous []byte, previousManifest *AppManifest, previousBackup []byte, hadPreviousBackup bool) (bool, error) {
	manifestPath, err := safePath(cfg.AppRoot, target.Site+".json")
	if err != nil {
		return false, err
	}
	target.State = "running"
	data, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return false, err
	}
	targetBytes := append(data, '\n')
	journal, err := newAppActivationJournalWithBackup(cfg, target.Site, manifestPath, previous, previousManifest, previousBackup, hadPreviousBackup)
	if err != nil {
		return false, err
	}
	if err := journal.setTarget(targetBytes, previous, previousManifest != nil); err != nil {
		return false, err
	}
	if err := journal.setState("runtime_applying"); err != nil {
		return false, err
	}
	compensate := func(cause error) (bool, error) {
		recoveryErr := restoreAppActivation(cfg, ctx, journal)
		return false, &appActivationFailure{Cause: cause, RecoveryErr: recoveryErr}
	}
	if err := ctx.Err(); err != nil {
		return compensate(err)
	}
	if err := applyAppProcess(ctx, cfg, target); err != nil {
		return compensate(err)
	}
	if err := journal.setState("runtime_applied"); err != nil {
		return compensate(err)
	}
	if err := ctx.Err(); err != nil {
		return compensate(err)
	}
	backupPath, err := safePath(cfg.AppRoot, target.Site+".json.bak")
	if err != nil {
		return compensate(err)
	}
	if previousManifest != nil {
		if err := writeAtomic(backupPath, previous, 0600); err != nil {
			return compensate(err)
		}
	} else if err := removeAppBackup(cfg, target.Site); err != nil {
		return compensate(err)
	}
	if err := ctx.Err(); err != nil {
		return compensate(err)
	}
	if err := writeAtomic(manifestPath, targetBytes, 0600); err != nil {
		return compensate(err)
	}
	if err := ctx.Err(); err != nil {
		return compensate(err)
	}
	if err := journal.setState("committed"); err != nil {
		return compensate(err)
	}
	if err := journal.cleanup(); err != nil {
		return true, nil
	}
	return false, nil
}

const maxAppStateFileBytes = 1 << 20

func readAppStateFile(cfg Config, site, suffix string) ([]byte, bool, error) {
	if safeUser(site) == "" || (suffix != ".json" && suffix != ".json.bak") {
		return nil, false, errors.New("invalid application state path")
	}
	path, err := safePath(cfg.AppRoot, site+suffix)
	if err != nil {
		return nil, false, err
	}
	f, _, err := openRegularNoFollow(path, nil)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxAppStateFileBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return nil, false, readErr
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	if len(data) > maxAppStateFileBytes {
		return nil, false, errors.New("application state file exceeds size limit")
	}
	return data, true, nil
}

func restoreAppBackup(cfg Config, site string, previous []byte, existed bool) error {
	if safeUser(site) == "" {
		return errors.New("application rollback site is invalid")
	}
	path, err := safePath(cfg.AppRoot, site+".json.bak")
	if err != nil {
		return err
	}
	if existed {
		return writeAtomic(path, previous, 0600)
	}
	return removeAppBackup(cfg, site)
}

func removeAppBackup(cfg Config, site string) error {
	if safeUser(site) == "" {
		return errors.New("application rollback site is invalid")
	}
	path, err := safePath(cfg.AppRoot, site+".json.bak")
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// removeAppManifest removes only the manifest for a validated site identity.
// Opening the configured app directory without following symlinks and unlinking
// by basename prevents a persisted journal from turning recovery into a
// pathname-based delete outside the app root.
func removeAppManifest(cfg Config, site, expectedPath string) error {
	if safeUser(site) == "" {
		return errors.New("application manifest site is invalid")
	}
	manifestPath, err := safePath(cfg.AppRoot, site+".json")
	if err != nil || filepath.Clean(manifestPath) != filepath.Clean(expectedPath) {
		return errors.New("application manifest path is invalid")
	}
	root, err := filepath.Abs(cfg.AppRoot)
	if err != nil {
		return err
	}
	dirFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(dirFD)
	err = unix.Unlinkat(dirFD, site+".json", 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

func (a *App) appAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/apps/"), "/")
	allowed := map[string]bool{"start": true, "stop": true, "restart": true, "rollback": true}
	if len(parts) != 2 || safeUser(parts[0]) == "" || !allowed[parts[1]] {
		http.Error(w, "invalid app action", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, parts[0], "site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for app operations", http.StatusForbidden)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), parts[0])
	if lockErr != nil {
		http.Error(w, "app operation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if parts[1] == "rollback" {
		backup, hadBackup, err := readAppStateFile(a.Config, parts[0], ".json.bak")
		if err != nil || !hadBackup {
			http.Error(w, "no previous app release is available", 409)
			return
		}
		var previous AppManifest
		if json.Unmarshal(backup, &previous) != nil || !validAppManifest(a.Config, previous, parts[0]) {
			http.Error(w, "invalid previous app release", 500)
			return
		}
		current, hadCurrent, err := readAppStateFile(a.Config, parts[0], ".json")
		if err != nil || !hadCurrent {
			http.Error(w, "current app manifest is unavailable", 500)
			return
		}
		var currentManifest AppManifest
		if json.Unmarshal(current, &currentManifest) != nil || !validAppManifest(a.Config, currentManifest, parts[0]) {
			http.Error(w, "current app manifest is invalid", 500)
			return
		}
		// Applying a release always leaves the service enabled and running.
		previous.State = "running"
		cleanupPending, activationErr := activateAppManifest(operationCtx, a.Config, previous, current, &currentManifest, backup, hadBackup)
		if activationErr != nil {
			log.Printf("app rollback %s: %v", parts[0], activationErr)
			var rollbackErr *appActivationFailure
			if errors.As(activationErr, &rollbackErr) && rollbackErr.RecoveryErr != nil {
				http.Error(w, "app rollback failed and recovery is incomplete; operator intervention is required", http.StatusServiceUnavailable)
			} else {
				http.Error(w, "app rollback failed; previous runtime and manifests were restored", http.StatusBadGateway)
			}
			return
		}
		if cleanupPending {
			log.Printf("app rollback %s: committed activation journal cleanup is pending startup recovery", parts[0])
		}
	} else if status, message, err := a.runAppStateAction(operationCtx, parts[0], parts[1]); err != nil {
		log.Printf("app %s %s: %v", parts[1], parts[0], err)
		a.invalidateAppsCache()
		http.Error(w, message, status)
		return
	}
	a.invalidateAppsCache()
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "app."+parts[1], parts[0], "systemd action")
	writeJSON(w, http.StatusAccepted, map[string]string{"site": parts[0], "action": parts[1]})
}

func validAppManifest(cfg Config, app AppManifest, site string) bool {
	if app.Site != site || safeUser(app.Site) == "" || !domainname.Valid(strings.ToLower(app.Domain)) || !nodeVersionPattern.MatchString(app.Version) || app.Port < 1024 || app.Port > 65535 {
		return false
	}
	expected, err := safePath(cfg.WebRoot, "sites", site, "public")
	if err != nil {
		return false
	}
	return filepath.Clean(app.Root) == filepath.Clean(expected)
}

// applyAppProcess publishes the Node process configuration for app.
func applyAppProcess(ctx context.Context, cfg Config, app AppManifest) error {
	version := strings.TrimPrefix(app.Version, "v")
	if handled, err := runAppBroker(ctx, cfg, rootbroker.AppRequest{Action: "apply", Site: app.Site, Version: version, Root: app.Root, Port: app.Port}); handled {
		return err
	}
	return runHelperCommandWithTimeout(ctx, cfg, helperServiceLifecycleTimeout, cfg.AppCtl, "apply", app.Site, version, app.Root, strconv.Itoa(app.Port))
}

// restoreAppProcess re-applies manifest after a failed rollback and returns the
// service to the recorded state, since apply always leaves it running.
func restoreAppProcess(ctx context.Context, cfg Config, manifest AppManifest) error {
	if err := applyAppProcess(ctx, cfg, manifest); err != nil {
		return err
	}
	if manifest.State == "stopped" {
		return runAppLifecycle(ctx, cfg, "stop", manifest.Site)
	}
	return nil
}

// runAppStateAction starts, stops or restarts a deployed application. A
// durable activation journal keeps the runtime and manifest generation
// recoverable if the process exits between the service action and state write.
func (a *App) runAppStateAction(ctx context.Context, site, action string) (int, string, error) {
	current, exists, err := readAppStateFile(a.Config, site, ".json")
	if errors.Is(err, os.ErrNotExist) || (err == nil && !exists) {
		return http.StatusConflict, "application is not deployed", os.ErrNotExist
	}
	if err != nil {
		return http.StatusInternalServerError, "current app manifest is unavailable", err
	}
	var manifest AppManifest
	if err := json.Unmarshal(current, &manifest); err != nil {
		return http.StatusInternalServerError, "current app manifest is invalid", err
	}
	if !validAppManifest(a.Config, manifest, site) {
		return http.StatusInternalServerError, "current app manifest is invalid", errors.New("manifest failed validation")
	}
	previousManifest := manifest
	previousBackup, hadPreviousBackup, err := readAppStateFile(a.Config, site, ".json.bak")
	if err != nil {
		return http.StatusInternalServerError, "app rollback state is unavailable", err
	}
	manifest.State = "running"
	if action == "stop" {
		manifest.State = "stopped"
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return http.StatusInternalServerError, "unable to encode app manifest", err
	}
	cleanupPending, transitionErr := applyAppStateTransition(ctx, a.Config, manifest, current, &previousManifest, previousBackup, hadPreviousBackup, append(data, '\n'), action)
	if transitionErr != nil {
		var activationErr *appActivationFailure
		if errors.As(transitionErr, &activationErr) && activationErr.RecoveryErr != nil {
			return http.StatusServiceUnavailable, "app action failed and recovery is incomplete; operator intervention is required", transitionErr
		}
		return http.StatusBadGateway, "app action failed; prior runtime and state were restored", transitionErr
	}
	if cleanupPending {
		log.Printf("app action %s %s: committed activation journal cleanup is pending startup recovery", action, site)
	}
	return 0, "", nil
}

func applyAppStateTransition(ctx context.Context, cfg Config, target AppManifest, previous []byte, previousManifest *AppManifest, previousBackup []byte, hadPreviousBackup bool, targetBytes []byte, action string) (bool, error) {
	manifestPath, err := safePath(cfg.AppRoot, target.Site+".json")
	if err != nil {
		return false, err
	}
	journal, err := newAppActivationJournalWithBackup(cfg, target.Site, manifestPath, previous, previousManifest, previousBackup, hadPreviousBackup)
	if err != nil {
		return false, err
	}
	if err := journal.setTarget(targetBytes, previousBackup, hadPreviousBackup); err != nil {
		return false, err
	}
	if err := journal.setState("runtime_applying"); err != nil {
		return false, err
	}
	compensate := func(cause error) (bool, error) {
		recoveryErr := restoreAppActivation(cfg, ctx, journal)
		return false, &appActivationFailure{Cause: cause, RecoveryErr: recoveryErr}
	}
	if err := ctx.Err(); err != nil {
		return compensate(err)
	}
	if err := runAppLifecycle(ctx, cfg, action, target.Site); err != nil {
		return compensate(err)
	}
	if err := journal.setState("runtime_applied"); err != nil {
		return compensate(err)
	}
	if err := ctx.Err(); err != nil {
		return compensate(err)
	}
	if err := writeAtomic(manifestPath, targetBytes, 0600); err != nil {
		return compensate(err)
	}
	if err := ctx.Err(); err != nil {
		return compensate(err)
	}
	if err := journal.setState("committed"); err != nil {
		return compensate(err)
	}
	if err := journal.cleanup(); err != nil {
		return true, nil
	}
	return false, nil
}

func (a *App) invalidateAppsCache() {
	if a.MetadataCache != nil {
		a.MetadataCache.InvalidateApps()
	}
}

// runAppLifecycle performs a start, stop, restart or delete of a site's Node process.
func runAppLifecycle(ctx context.Context, cfg Config, action, site string) error {
	if handled, err := runAppBroker(ctx, cfg, rootbroker.AppRequest{Action: action, Site: site}); handled {
		return err
	}
	return runHelperCommandWithTimeout(ctx, cfg, helperServiceLifecycleTimeout, cfg.AppCtl, action, site)
}

// runAppBroker routes an application operation through the typed root broker
// in production and direct-broker lab installs. It reports handled=false when
// the legacy helper path should be used instead.
func runAppBroker(ctx context.Context, cfg Config, app rootbroker.AppRequest) (bool, error) {
	if !cfg.Production && !labDirectRootBrokerEnabled() {
		return false, nil
	}
	client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
	if err != nil {
		return true, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, helperServiceLifecycleTimeout)
	defer cancel()
	resp, err := client.AppOperation(operationCtx, app)
	if err != nil {
		return true, err
	}
	if !resp.OK {
		return true, errors.New(resp.Error)
	}
	return true, nil
}
