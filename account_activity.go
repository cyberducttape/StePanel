package stepanel

import (
	"net/http"
	"strconv"
	"strings"

	auditpkg "github.com/cyberducttape/StePanel/internal/audit"
)

// accountActivity returns verified history scoped to the authenticated
// customer's assigned sites and own account. It never accepts a customer
// supplied target or action filter.
func (a *App) accountActivity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	username := a.Auth.UsernameForRequest(r)
	if username == "" || a.Auth.IsAdministrator(r) || a.Auth.IsAPITokenRequest(r) || a.Accounts == nil {
		http.Error(w, "customer account required", http.StatusForbidden)
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			http.Error(w, "limit must be between 1 and 100", http.StatusUnprocessableEntity)
			return
		}
		limit = parsed
	}
	sites, err := a.Accounts.GetSitesWithError(username)
	if err != nil {
		http.Error(w, "unable to inspect site assignments", http.StatusInternalServerError)
		return
	}
	targets := append([]string{username}, sites...)
	events, err := auditpkg.ReadScopedEvents(a.Config.AuditLog, targets, username, limit)
	if err != nil {
		http.Error(w, "audit integrity verification failed", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "integrity": "verified"})
}
