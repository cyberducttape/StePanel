package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	entries, _ := os.ReadDir(a.Config.AppRoot)
	apps := []AppManifest{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(a.Config.AppRoot, entry.Name()))
		if err != nil {
			continue
		}
		var app AppManifest
		if json.Unmarshal(data, &app) == nil {
			apps = append(apps, app)
		}
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Site < apps[j].Site })
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
	if safeUser(app.Site) == "" || len(app.Domain)+len(app.Site) > 220 || !domainPattern.MatchString(app.Domain) || !nodeVersionPattern.MatchString(app.Version) || app.Port < 1024 || app.Port > 65535 {
		http.Error(w, "invalid site, Node version, or port", 422)
		return
	}
	if _, err := localBackend("http://127.0.0.1:" + strconv.Itoa(app.Port)); err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, app.Site, "site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for app deployment", http.StatusForbidden)
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
	a.appLifecycleMu.Lock()
	defer a.appLifecycleMu.Unlock()
	if err := os.MkdirAll(a.Config.AppRoot, 0750); err != nil {
		http.Error(w, "unable to create app state directory", 500)
		return
	}
	manifestPath := filepath.Join(a.Config.AppRoot, app.Site+".json")
	previous, previousErr := os.ReadFile(manifestPath)
	if previousErr != nil && !os.IsNotExist(previousErr) {
		http.Error(w, "unable to read existing app manifest", 500)
		return
	}
	hadPrevious := previousErr == nil
	if hadPrevious {
		if err := writeAtomic(manifestPath+".bak", previous, 0600); err != nil {
			http.Error(w, "unable to save app rollback state", 500)
			return
		}
	}
	app.State = "running"
	data, err := json.MarshalIndent(app, "", "  ")
	if err != nil {
		http.Error(w, "unable to encode app manifest", 500)
		return
	}
	if err := writeAtomic(manifestPath, append(data, '\n'), 0600); err != nil {
		http.Error(w, "unable to save app manifest", 500)
		return
	}
	if err := runHelperCommandWithTimeout(r.Context(), a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "apply", app.Site, strings.TrimPrefix(app.Version, "v"), app.Root, strconv.Itoa(app.Port)); err != nil {
		var rollbackErr error
		if hadPrevious {
			rollbackErr = writeAtomic(manifestPath, previous, 0600)
		} else {
			rollbackErr = os.Remove(manifestPath)
			if errors.Is(rollbackErr, os.ErrNotExist) {
				rollbackErr = nil
			}
		}
		if rollbackErr != nil {
			http.Error(w, "app helper failed and manifest rollback failed: "+rollbackErr.Error(), http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "app manifest saved but systemd helper failed", 503)
		return
	}
	if err := AuditAs(a.Config.AuditLog, a.Auth.Username, "app.deployed", app.Site, app.Domain+" on port "+strconv.Itoa(app.Port)); err != nil {
		log.Printf("application deployed but audit persistence is unavailable: %v", err)
	}
	writeJSON(w, http.StatusAccepted, app)
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
	releaseUnlock := a.siteOperations.Acquire(parts[0])
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
		if err := runHelperCommandWithTimeout(r.Context(), a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "apply", previous.Site, strings.TrimPrefix(previous.Version, "v"), previous.Root, strconv.Itoa(previous.Port)); err != nil {
			http.Error(w, "rollback failed", 502)
			return
		}
		if err := writeAtomic(manifestPath+".bak", current, 0600); err != nil {
			_ = runHelperCommandWithTimeout(r.Context(), a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "apply", currentManifest.Site, strings.TrimPrefix(currentManifest.Version, "v"), currentManifest.Root, strconv.Itoa(currentManifest.Port))
			http.Error(w, "rollback state could not be persisted; the previous process configuration was restored", 500)
			return
		}
		if err := writeAtomic(manifestPath, backup, 0600); err != nil {
			_ = writeAtomic(manifestPath+".bak", backup, 0600)
			if runHelperCommandWithTimeout(r.Context(), a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, "apply", currentManifest.Site, strings.TrimPrefix(currentManifest.Version, "v"), currentManifest.Root, strconv.Itoa(currentManifest.Port)) != nil {
				http.Error(w, "rollback manifest failed and the prior process configuration could not be restored", 503)
				return
			}
			http.Error(w, "rollback manifest could not be persisted; the previous process configuration was restored", 500)
			return
		}
	} else if err := runHelperCommandWithTimeout(r.Context(), a.Config, helperServiceLifecycleTimeout, a.Config.AppCtl, parts[1], parts[0]); err != nil {
		http.Error(w, "app action failed", 502)
		return
	}
	if err := AuditAs(a.Config.AuditLog, a.Auth.Username, "app."+parts[1], parts[0], "systemd action"); err != nil {
		log.Printf("application action completed but audit persistence is unavailable: %v", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"site": parts[0], "action": parts[1]})
}

func validAppManifest(cfg Config, app AppManifest, site string) bool {
	if app.Site != site || safeUser(app.Site) == "" || !domainPattern.MatchString(strings.ToLower(app.Domain)) || !nodeVersionPattern.MatchString(app.Version) || app.Port < 1024 || app.Port > 65535 {
		return false
	}
	expected := filepath.Join(cfg.WebRoot, "sites", site, "public")
	return filepath.Clean(app.Root) == filepath.Clean(expected) && ensureInside(cfg.WebRoot, app.Root) == nil
}
