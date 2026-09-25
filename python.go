package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var pythonVersionPattern = regexp.MustCompile(`^3\.(12|13)$`)
var pythonEntryPattern = regexp.MustCompile(`^[A-Za-z0-9_./:-]{1,160}$`)

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

func pythonManifestPath(root, site string) string { return filepath.Join(root, site+"-python.json") }

func savePythonApp(root string, app PythonApp) error {
	if err := os.MkdirAll(root, 0750); err != nil {
		return err
	}
	data, err := json.MarshalIndent(app, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(pythonManifestPath(root, app.Site), append(data, '\n'), 0600)
}

func (a *App) pythonDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", 403)
		return
	}
	var app PythonApp
	if err := decodeJSON(w, r, 8192, &app); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	app.Site = safeUser(app.Site)
	app.Version = strings.TrimPrefix(strings.TrimSpace(app.Version), "v")
	app.EntryPoint = strings.TrimSpace(app.EntryPoint)
	if app.Site == "" || !pythonVersionPattern.MatchString(app.Version) || !pythonEntryPattern.MatchString(app.EntryPoint) || app.Port < 1024 || app.Port > 65535 || app.Workers < 1 || app.Workers > 64 {
		http.Error(w, "invalid Python application definition", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, app.Site, "site is not assigned to this account", 403); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "deploy:write") {
		http.Error(w, "API token lacks the deploy:write scope", 403)
		return
	}
	app.Root = filepath.Join(a.Config.WebRoot, "sites", app.Site, "public")
	if err := ensureInside(a.Config.WebRoot, app.Root); err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	if info, err := os.Stat(app.Root); err != nil || !info.IsDir() {
		http.Error(w, "site document root does not exist", 422)
		return
	}
	releaseUnlock := a.siteOperations.Acquire(app.Site)
	defer releaseUnlock()
	app.State, app.LastError = "pending", ""
	if err := savePythonApp(a.Config.AppRoot, app); err != nil {
		http.Error(w, "could not persist desired Python application", 503)
		return
	}
	if err := a.applyPythonApp(r.Context(), app); err != nil {
		app.LastError = err.Error()
		_ = savePythonApp(a.Config.AppRoot, app)
		http.Error(w, "Python application is pending reconciliation", 502)
		return
	}
	app.State, app.LastError = "running", ""
	if err := savePythonApp(a.Config.AppRoot, app); err != nil {
		http.Error(w, "Python application applied but state update is pending", 503)
		return
	}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "python.deployed", app.Site, app.EntryPoint)
	writeJSON(w, 202, app)
}

func (a *App) applyPythonApp(ctx context.Context, app PythonApp) error {
	// Python setup can take 10-30 minutes depending on dependencies.
	// Note: reconciliation context may have shorter deadline; consider moving
	// to async jobs for long-running operations (see docs/STARTUP_READINESS.md).
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
		data, readErr := os.ReadFile(filepath.Join(a.Config.AppRoot, entry.Name()))
		if readErr != nil {
			failed[entry.Name()] = readErr.Error()
			continue
		}
		var app PythonApp
		if json.Unmarshal(data, &app) != nil || safeUser(app.Site) == "" || app.State != "pending" {
			continue
		}
		releaseUnlock := a.siteOperations.Acquire(app.Site)
		if err := a.applyPythonApp(ctx, app); err != nil {
			app.LastError = err.Error()
			_ = savePythonApp(a.Config.AppRoot, app)
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
	releaseUnlock := a.siteOperations.Acquire(parts[0])
	defer releaseUnlock()
	if err := runHelperCommandWithTimeout(r.Context(), a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "python-"+parts[1], parts[0]); err != nil {
		http.Error(w, "Python action failed", 502)
		return
	}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "python."+parts[1], parts[0], "systemd action")
	writeJSON(w, 202, map[string]string{"site": parts[0], "action": parts[1]})
}
