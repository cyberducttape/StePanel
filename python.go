package stepanel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

var pythonVersionPattern = regexp.MustCompile(`^3\.(12|13)$`)
var pythonEntryPattern = regexp.MustCompile(`^[A-Za-z0-9_./:-]{1,160}$`)

func normalizePythonApp(app PythonApp) (PythonApp, error) {
	app.Site = safeUser(app.Site)
	app.Version = strings.TrimPrefix(strings.TrimSpace(app.Version), "v")
	app.EntryPoint = strings.TrimSpace(app.EntryPoint)
	if app.Site == "" || !pythonVersionPattern.MatchString(app.Version) || !pythonEntryPattern.MatchString(app.EntryPoint) || app.Port < 1024 || app.Port > 65535 || app.Workers < 1 || app.Workers > 64 {
		return PythonApp{}, errors.New("invalid Python application definition")
	}
	// Root and lifecycle status are server-owned values, never caller intent.
	app.Root, app.State, app.LastError = "", "", ""
	return app, nil
}

func decodePythonApp(body []byte) (PythonApp, error) {
	if len(body) > 8192 {
		return PythonApp{}, errors.New("Python application request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var app PythonApp
	if err := decoder.Decode(&app); err != nil {
		return PythonApp{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return PythonApp{}, errors.New("request body must contain one JSON value")
	}
	return normalizePythonApp(app)
}

func validatePythonAppPayload(body []byte) error {
	_, err := decodePythonApp(body)
	return err
}

type pythonDeploymentFailure struct {
	status  int
	message string
}

func (e *pythonDeploymentFailure) Error() string { return e.message }

func pythonDeploymentStatus(err error) int {
	var failure *pythonDeploymentFailure
	if errors.As(err, &failure) {
		return failure.status
	}
	return http.StatusBadGateway
}

type PythonApp struct {
	Site       string `json:"site"`
	Version    string `json:"version"`
	EntryPoint string `json:"entrypoint"`
	Port       int    `json:"port"`
	Workers    int    `json:"workers"`
	Root       string `json:"root"`
	State      string `json:"state"`
	LastError  string `json:"last_error,omitempty"`
}

func pythonManifestPath(root, site string) (string, error) {
	return safePath(root, site+"-python.json")
}

func savePythonApp(root string, app PythonApp) error {
	if err := os.MkdirAll(root, 0750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(app, "", "  ")
	if err != nil {
		return err
	}
	manifestPath, err := pythonManifestPath(root, app.Site)
	if err != nil {
		return err
	}
	return writeAtomic(manifestPath, append(data, '\n'), 0600)
}

func (a *App) pythonDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
	if err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	app, err := decodePythonApp(body)
	if err != nil {
		http.Error(w, "invalid Python application definition", 422)
		return
	}
	access, ok := a.requireSiteAccess(w, r, app.Site, "site is not assigned to this account", 403)
	if !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "deploy:write") {
		http.Error(w, "API token lacks the deploy:write scope", 403)
		return
	}
	app, err = a.applyPythonDeployment(r.Context(), a.Auth.AuditActor(r), access, app)
	if err != nil {
		http.Error(w, err.Error(), pythonDeploymentStatus(err))
		return
	}
	writeJSON(w, http.StatusAccepted, app)
}

// executePythonDeploymentCommand is the durable worker adapter. It repeats
// authorization and command validation at execution time, then invokes the
// same lifecycle service as the HTTP adapter without replaying an HTTP route.
func executePythonDeploymentCommand(a *App, ctx context.Context, command siteOperationCommand, principal siteOperationPrincipal) ([]byte, error) {
	app, err := decodePythonApp(command.Body)
	if err != nil || app.Site != command.Site {
		return nil, errors.New("invalid Python deployment command")
	}
	access, err := a.authorizeDurableSiteJob(app.Site, principal.Actor, false)
	if err != nil {
		return nil, fmt.Errorf("Python deployment authorization: %w", err)
	}
	if !a.durableSiteOperationScopeAllowed(principal, "deploy:write") {
		return nil, errors.New("Python deployment authorization: required deploy:write permission is no longer active")
	}
	app, err = a.applyPythonDeployment(ctx, principal.Actor, access, app)
	if err != nil {
		return nil, err
	}
	return json.Marshal(app)
}

func (a *App) applyPythonDeployment(ctx context.Context, actor string, access SiteCapability, app PythonApp) (PythonApp, error) {
	if err := ctx.Err(); err != nil {
		return PythonApp{}, &pythonDeploymentFailure{http.StatusConflict, "Python application mutation cancelled because the mutation lock was lost"}
	}
	root, err := siteDocRoot(a.Config, access.Site())
	if err != nil {
		return PythonApp{}, &pythonDeploymentFailure{http.StatusUnprocessableEntity, "invalid site root"}
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return PythonApp{}, &pythonDeploymentFailure{http.StatusUnprocessableEntity, "site document root does not exist"}
	}
	operationCtx, release, err := a.acquireSiteMutationLockContext(ctx, access.Site())
	if err != nil {
		return PythonApp{}, &pythonDeploymentFailure{http.StatusConflict, "Python application mutation is busy"}
	}
	defer release()
	if err := operationCtx.Err(); err != nil {
		return PythonApp{}, &pythonDeploymentFailure{http.StatusConflict, "Python application mutation cancelled because the mutation lock was lost"}
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, actor, "python.deployed", app.Site, app.EntryPoint)
	if err != nil {
		return PythonApp{}, &pythonDeploymentFailure{http.StatusServiceUnavailable, "Python deployment refused because the security audit could not be recorded"}
	}
	var outcome string
	defer intent.Finish(&outcome, "desired Python application was not persisted")
	app.Root, app.State, app.LastError = root, "pending", ""
	if err := savePythonApp(a.Config.AppRoot, app); err != nil {
		return PythonApp{}, &pythonDeploymentFailure{http.StatusServiceUnavailable, "could not persist desired Python application"}
	}
	outcome = app.EntryPoint + "; persisted, reconciliation pending"
	if err := a.applyPythonApp(operationCtx, app); err != nil {
		app.LastError = err.Error()
		if saveErr := savePythonApp(a.Config.AppRoot, app); saveErr != nil {
			TelemetryAudit(a.Config.AuditLog, actor, "python.state_save_failed", app.Site, saveErr.Error())
		}
		return PythonApp{}, &pythonDeploymentFailure{http.StatusBadGateway, "Python application is pending reconciliation"}
	}
	if err := operationCtx.Err(); err != nil {
		app.State, app.LastError = "pending", err.Error()
		if saveErr := savePythonApp(a.Config.AppRoot, app); saveErr != nil {
			TelemetryAudit(a.Config.AuditLog, actor, "python.state_save_failed", app.Site, saveErr.Error())
		}
		return PythonApp{}, &pythonDeploymentFailure{http.StatusConflict, "Python application is pending reconciliation"}
	}
	app.State, app.LastError = "running", ""
	if err := savePythonApp(a.Config.AppRoot, app); err != nil {
		return PythonApp{}, &pythonDeploymentFailure{http.StatusServiceUnavailable, "Python application applied but state update is pending"}
	}
	outcome = app.EntryPoint
	return app, nil
}

func (a *App) applyPythonApp(ctx context.Context, app PythonApp) error {
	// Python setup can take 10-30 minutes depending on dependencies.
	// Note: reconciliation context may have shorter deadline; consider moving
	// to async jobs for long-running operations (see docs/STARTUP_READINESS.md).
	if a.Config.Production {
		return runTypedAppOperation(ctx, a.Config, rootbroker.AppRequest{Action: "python-apply", Site: app.Site, Version: app.Version, Root: app.Root, EntryPoint: app.EntryPoint, Port: app.Port, Workers: app.Workers})
	}
	return runHelperCommandWithTimeout(ctx, a.Config, helperPackageBuildTimeout, a.Config.AppCtl, "python-apply", app.Site, app.Version, app.Root, app.EntryPoint, strconv.Itoa(app.Port), strconv.Itoa(app.Workers))
}

func (a *App) reconcilePythonApps(ctx context.Context) (reconciled []string, failed map[string]string) {
	failed = map[string]string{}
	entries, err := os.ReadDir(a.Config.AppRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, failed
		}
		return nil, map[string]string{"state": err.Error()}
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "-python.json") {
			continue
		}
		data, readErr := readStateFile(filepath.Join(a.Config.AppRoot, entry.Name()), maxAppStateFileBytes)
		if readErr != nil {
			failed[entry.Name()] = readErr.Error()
			continue
		}
		var app PythonApp
		if err := json.Unmarshal(data, &app); err != nil {
			failed[entry.Name()] = fmt.Sprintf("decode Python manifest: %v", err)
			continue
		}
		if safeUser(app.Site) == "" {
			failed[entry.Name()] = "Python manifest has an invalid site"
			continue
		}
		if app.State != "pending" {
			continue
		}
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(ctx, app.Site)
		if lockErr != nil {
			failed[app.Site] = lockErr.Error()
			continue
		}
		if err := a.applyPythonApp(operationCtx, app); err != nil {
			app.LastError = err.Error()
			if saveErr := savePythonApp(a.Config.AppRoot, app); saveErr != nil {
				// Log state save failure alongside application error
				failed[app.Site] = fmt.Sprintf("apply failed: %v; state save failed: %v", err, saveErr)
				releaseUnlock()
				continue
			}
			failed[app.Site] = err.Error()
			releaseUnlock()
			continue
		}
		if err := operationCtx.Err(); err != nil {
			app.LastError = err.Error()
			if saveErr := savePythonApp(a.Config.AppRoot, app); saveErr != nil {
				failed[app.Site] = fmt.Sprintf("apply cancelled: %v; state save failed: %v", err, saveErr)
				releaseUnlock()
				continue
			}
			failed[app.Site] = err.Error()
			releaseUnlock()
			continue
		}
		app.State, app.LastError = "running", ""
		if err := savePythonApp(a.Config.AppRoot, app); err != nil {
			failed[app.Site] = err.Error()
			releaseUnlock()
			continue
		}
		if err := operationCtx.Err(); err != nil {
			app.State, app.LastError = "pending", err.Error()
			if saveErr := savePythonApp(a.Config.AppRoot, app); saveErr != nil {
				failed[app.Site] = fmt.Sprintf("apply cancelled: %v; state save failed: %v", err, saveErr)
			} else {
				failed[app.Site] = err.Error()
			}
			releaseUnlock()
			continue
		}
		reconciled = append(reconciled, app.Site)
		releaseUnlock()
	}
	return reconciled, failed
}

func (a *App) pythonAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/python/"), "/"), "/")
	if len(parts) != 2 || safeUser(parts[0]) == "" || (parts[1] != "start" && parts[1] != "stop" && parts[1] != "restart") {
		http.Error(w, "invalid Python action", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, parts[0], "site is not assigned to this account", 403); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for Python operations", http.StatusForbidden)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), parts[0])
	if lockErr != nil {
		http.Error(w, "Python operation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	var actionErr error
	if a.Config.Production {
		actionErr = runTypedAppOperation(operationCtx, a.Config, rootbroker.AppRequest{Action: "python-" + parts[1], Site: parts[0]})
	} else {
		actionErr = runHelperCommandWithTimeout(operationCtx, a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "python-"+parts[1], parts[0])
	}
	if err := actionErr; err != nil {
		http.Error(w, "Python action failed", 502)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "Python action cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "python."+parts[1], parts[0], "systemd action")
	writeJSON(w, 202, map[string]string{"site": parts[0], "action": parts[1]})
}
