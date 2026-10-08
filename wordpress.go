package stepanel

import (
	"net/http"
	"strings"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

// wordpressAction is intentionally a closed set. Repository-provided or
// user-supplied shell commands must run in a separately sandboxed runner.
type wordpressAction struct {
	Action string `json:"action"`
}

// wordpressActions maps the public action names to broker WordPress
// operations, which run wp-cli as the site's isolated user.
var wordpressActions = map[string]string{
	"status":          "status",
	"update_core":     "core-update",
	"update_plugins":  "plugin-update-all",
	"update_themes":   "theme-update-all",
	"maintenance_on":  "maintenance-activate",
	"maintenance_off": "maintenance-deactivate",
	"cron":            "cron-run-due",
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
	if !a.requireCustomerScope(w, r, "site:deploy") {
		return
	}
	var input wordpressAction
	if err := decodeJSON(w, r, 2048, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	operation, ok := wordpressActions[input.Action]
	if !ok {
		http.Error(w, "unsupported WordPress action", 422)
		return
	}
	if !commandAvailable(a.Config.WPCLI) {
		http.Error(w, "wp-cli is not installed", 503)
		return
	}
	if _, err := existingManagedSitePublicRoot(a.Config.WebRoot, site); err != nil {
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
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "WordPress operation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	result, err := runWordPress(operationCtx, a.Config, rootbroker.WordPressRequest{Action: operation, Site: site})
	if err != nil {
		http.Error(w, "WordPress action failed: "+err.Error(), 502)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "WordPress operation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	TelemetryAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "wordpress."+input.Action, site, "WP-CLI action completed")
	writeJSON(w, http.StatusAccepted, map[string]any{"site": site, "action": input.Action, "output": strings.TrimSpace(result.Output)})
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
	if !a.requireCustomerScope(w, r, "site:read") {
		return
	}
	installed, err := managedSiteFileExists(a.Config.WebRoot, site, "", "wp-config.php")
	if err != nil {
		installed = false
	}
	writeJSON(w, http.StatusOK, map[string]any{"site": site, "installed": installed, "wp_cli": commandAvailable(a.Config.WPCLI)})
}
