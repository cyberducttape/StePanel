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
	a.appLifecycleMu.Lock()
	defer a.appLifecycleMu.Unlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "app deployment cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	if err := os.MkdirAll(a.Config.AppRoot, 0750); err != nil {
		http.Error(w, "unable to create app state directory", 500)
		return
	}
	manifestPath, err := safePath(a.Config.AppRoot, app.Site+".json")
	if err != nil {
		http.Error(w, "invalid app manifest path", 422)
		return
	}
	previous, previousErr := os.ReadFile(manifestPath)
	if previousErr != nil && !os.IsNotExist(previousErr) {
		http.Error(w, "unable to read existing app manifest", 500)
		return
	}
	hadPrevious := previousErr == nil
	var previousManifest *AppManifest
	if hadPrevious {
		var parsed AppManifest
		if err := json.Unmarshal(previous, &parsed); err == nil && validAppManifest(a.Config, parsed, app.Site) {
			previousManifest = &parsed
		}
	}
	if hadPrevious {
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "app deployment cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if err := writeAtomic(manifestPath+".bak", previous, 0600); err != nil {
			http.Error(w, "unable to save app rollback state", 500)
			return
		}
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
	journal, err := newAppActivationJournal(a.Config, app.Site, manifestPath, previous, previousManifest)
	if err != nil {
		http.Error(w, "unable to prepare app recovery journal", http.StatusInternalServerError)
		return
	}
	if err := journal.setState("runtime_applying"); err != nil {
		http.Error(w, "unable to persist app recovery journal", http.StatusInternalServerError)
		return
	}
	app.State = "running"
	data, err := json.MarshalIndent(app, "", "  ")
	if err != nil {
		http.Error(w, "unable to encode app manifest", 500)
		return
	}
	if err := applyAppProcess(operationCtx, a.Config, app); err != nil {
		log.Printf("app deploy %s: apply service configuration: %v", app.Site, err)
		rollbackErr := restoreAppActivation(a.Config, operationCtx, journal)
		if rollbackErr != nil {
			outcome = deployment + "; helper failed and manifest rollback failed: " + rollbackErr.Error()
			log.Printf("app deploy %s: restore previous manifest: %v", app.Site, rollbackErr)
			http.Error(w, "app helper failed and manifest rollback failed: "+rollbackErr.Error(), http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "application service could not be applied; the previous configuration was kept", http.StatusServiceUnavailable)
		return
	}
	if err := journal.setState("runtime_applied"); err != nil {
		_ = restoreAppActivation(a.Config, operationCtx, journal)
		http.Error(w, "application recovery state could not be persisted", http.StatusServiceUnavailable)
		return
	}
	if err := writeAtomic(manifestPath, append(data, '\n'), 0600); err != nil {
		rollbackErr := restoreAppActivation(a.Config, operationCtx, journal)
		if rollbackErr != nil {
			http.Error(w, "app manifest could not be saved and runtime recovery failed", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "unable to save app manifest", 500)
		return
	}
	if err := journal.setState("manifest_committed"); err != nil {
		http.Error(w, "app recovery state could not be finalized", http.StatusServiceUnavailable)
		return
	}
	if err := journal.cleanup(); err != nil {
		http.Error(w, "app recovery journal could not be removed", http.StatusServiceUnavailable)
		return
	}
	outcome = deployment
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "app deployment cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	a.invalidateAppsCache()
	writeJSON(w, http.StatusAccepted, app)
}

func restoreAppActivation(cfg Config, ctx context.Context, journal *appActivationJournal) error {
	if journal == nil {
		return errors.New("application activation journal is missing")
	}
	var err error
	if journal.HadPrevious && journal.RestoreManifest != nil {
		err = restoreAppProcess(ctx, cfg, *journal.RestoreManifest)
	} else if !journal.HadPrevious {
		err = runAppLifecycle(ctx, cfg, "stop", journal.Site)
	}
	if err != nil {
		return err
	}
	if journal.HadPrevious {
		return writeAtomic(journal.ManifestPath, journal.Previous, 0600)
	}
	err = os.Remove(journal.ManifestPath)
	if errors.Is(err, os.ErrNotExist) {
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
	a.appLifecycleMu.Lock()
	defer a.appLifecycleMu.Unlock()
	if parts[1] == "rollback" {
		manifestPath := filepath.Join(a.Config.AppRoot, parts[0]+".json")
		backup, err := os.ReadFile(manifestPath + ".bak")
		if err != nil {
			http.Error(w, "no previous app release is available", 409)
			return
		}
		var previous AppManifest
		if json.Unmarshal(backup, &previous) != nil || !validAppManifest(a.Config, previous, parts[0]) {
			http.Error(w, "invalid previous app release", 500)
			return
		}
		current, err := os.ReadFile(manifestPath)
		if err != nil {
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
		restored, err := json.MarshalIndent(previous, "", "  ")
		if err != nil {
			http.Error(w, "unable to encode previous app release", 500)
			return
		}
		restored = append(restored, '\n')
		if err := applyAppProcess(operationCtx, a.Config, previous); err != nil {
			log.Printf("app rollback %s: apply previous release: %v", parts[0], err)
			http.Error(w, "rollback failed", 502)
			return
		}
		if err := operationCtx.Err(); err != nil {
			http.Error(w, "app rollback cancelled because the mutation lock was lost", http.StatusConflict)
			return
		}
		if err := writeAtomic(manifestPath+".bak", current, 0600); err != nil {
			restoreErr := restoreAppProcess(operationCtx, a.Config, currentManifest)
			if restoreErr != nil {
				http.Error(w, fmt.Sprintf("rollback state could not be persisted and the previous process configuration could not be restored: %v", restoreErr), http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "rollback state could not be persisted; the previous process configuration was restored", http.StatusInternalServerError)
			return
		}
		if err := writeAtomic(manifestPath, restored, 0600); err != nil {
			backupRestoreErr := writeAtomic(manifestPath+".bak", backup, 0600)
			restoreErr := restoreAppProcess(operationCtx, a.Config, currentManifest)
			if backupRestoreErr != nil || restoreErr != nil {
				http.Error(w, fmt.Sprintf("rollback manifest failed and recovery was incomplete (manifest backup: %v; process configuration: %v)", backupRestoreErr, restoreErr), http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "rollback manifest could not be persisted; the previous process configuration was restored", http.StatusInternalServerError)
			return
		}
	} else if status, message, err := a.runAppStateAction(operationCtx, parts[0], parts[1]); err != nil {
		log.Printf("app %s %s: %v", parts[1], parts[0], err)
		a.invalidateAppsCache()
		http.Error(w, message, status)
		return
	}
	a.invalidateAppsCache()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "app action cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
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

// runAppStateAction starts, stops or restarts a deployed application and
// records the resulting state in its manifest. The manifest is written first
// and restored if the action fails, so it never claims a state the service was
// not put into. On failure it returns the HTTP status and message to report.
func (a *App) runAppStateAction(ctx context.Context, site, action string) (int, string, error) {
	manifestPath, err := safePath(a.Config.AppRoot, site+".json")
	if err != nil {
		return http.StatusInternalServerError, "app manifest path is invalid", err
	}
	manifestFile, _, err := openRegularNoFollow(manifestPath, nil)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return http.StatusConflict, "application is not deployed", err
		}
		return http.StatusInternalServerError, "current app manifest is unavailable", err
	}
	current, readErr := io.ReadAll(manifestFile)
	closeErr := manifestFile.Close()
	if readErr == nil {
		readErr = closeErr
	}
	err = readErr
	if errors.Is(err, os.ErrNotExist) {
		return http.StatusConflict, "application is not deployed", err
	}
	if err != nil {
		return http.StatusInternalServerError, "current app manifest is unavailable", err
	}
	var manifest AppManifest
	if err := json.Unmarshal(current, &manifest); err != nil {
		return http.StatusInternalServerError, "current app manifest is invalid", err
	}
	if !validAppManifest(a.Config, manifest, site) {
		return http.StatusInternalServerError, "current app manifest is invalid", fmt.Errorf("manifest %s failed validation", manifestPath)
	}
	manifest.State = "running"
	if action == "stop" {
		manifest.State = "stopped"
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return http.StatusInternalServerError, "unable to encode app manifest", err
	}
	if err := writeAtomic(manifestPath, append(data, '\n'), 0600); err != nil {
		return http.StatusInternalServerError, "unable to save app manifest", err
	}
	if err := runAppLifecycle(ctx, a.Config, action, site); err != nil {
		if restoreErr := writeAtomic(manifestPath, current, 0600); restoreErr != nil {
			return http.StatusServiceUnavailable, "app action failed and the app manifest could not be restored", errors.Join(err, restoreErr)
		}
		return http.StatusBadGateway, "app action failed", err
	}
	return 0, "", nil
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
