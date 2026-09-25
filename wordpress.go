package main

import (
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// wordpressAction is intentionally a closed set. Repository-provided or
// user-supplied shell commands must run in a separately sandboxed runner.
type wordpressAction struct {
	Action string `json:"action"`
}

var wordpressActions = map[string][]string{
	"status":          {"core", "version", "--format=json"},
	"update_core":     {"core", "update"},
	"update_plugins":  {"plugin", "update", "--all"},
	"update_themes":   {"theme", "update", "--all"},
	"maintenance_on":  {"maintenance-mode", "activate"},
	"maintenance_off": {"maintenance-mode", "deactivate"},
	"cron":            {"cron", "event", "run", "--due-now"},
}

func (a *App) wordpressAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/wordpress/"), "/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403); !ok {
		return
	}
	var input wordpressAction
	if err := decodeJSON(w, r, 2048, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	args, ok := wordpressActions[input.Action]
	if !ok {
		http.Error(w, "unsupported WordPress action", 422)
		return
	}
	if !commandAvailable(a.Config.WPCLI) {
		http.Error(w, "wp-cli is not installed", 503)
		return
	}
	root, err := existingManagedSitePublicRoot(a.Config.WebRoot, site)
	if err != nil {
		http.Error(w, "invalid site root", 422)
		return
	}
	installed, err := managedSiteFileExists(a.Config.WebRoot, site, "", "wp-config.php")
	if err != nil || !installed {
		http.Error(w, "WordPress is not installed for this site", 422)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
	if lockErr != nil {
		http.Error(w, "WordPress operation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	ctx, cancel := context.WithTimeout(operationCtx, 10*time.Minute)
	defer cancel()
	commandArgs := append([]string{"--path=" + root, "--no-color"}, args...)
	output, err := runBoundedCommand(ctx, exec.CommandContext(ctx, a.Config.WPCLI, commandArgs...))
	if err != nil {
		http.Error(w, "WordPress action failed: "+strings.TrimSpace(string(output)), 502)
		return
	}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "wordpress."+input.Action, site, "WP-CLI action completed")
	writeJSON(w, http.StatusAccepted, map[string]any{"site": site, "action": input.Action, "output": strings.TrimSpace(string(output))})
}

func (a *App) wordpressStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/wordpress/status/"), "/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", 422)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", 403); !ok {
		return
	}
	installed, err := managedSiteFileExists(a.Config.WebRoot, site, "", "wp-config.php")
	if err != nil {
		installed = false
	}
	writeJSON(w, http.StatusOK, map[string]any{"site": site, "installed": installed, "wp_cli": commandAvailable(a.Config.WPCLI)})
}
