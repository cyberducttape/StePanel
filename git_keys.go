package main

import (
	"net/http"
	"strings"
)

// siteGitKey exposes only the public half of a site-scoped deploy key.  The
// private half is generated, stored and consumed exclusively by stepanel-gitctl
// under /etc/stepanel/git-keys; neither the API nor the panel process reads it.
func (a *App) siteGitKey(w http.ResponseWriter, r *http.Request) {
	site := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sites/git-key/"), "/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid or inaccessible site", http.StatusForbidden)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "invalid or inaccessible site", http.StatusForbidden); !ok {
		return
	}
	if a.Config.GitCtl == "" {
		http.Error(w, "Git deploy-key helper is not configured", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		output, err := runBoundedCommand(r.Context(), helperCommandContext(r.Context(), a.Config, a.Config.GitCtl, "public", site))
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"site": site, "configured": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"site": site, "configured": true, "public_key": strings.TrimSpace(string(output))})
	case http.MethodPost:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		releaseUnlock := a.siteOperations.Acquire(site)
		defer releaseUnlock()
		output, err := runBoundedCommand(r.Context(), helperCommandContext(r.Context(), a.Config, a.Config.GitCtl, "generate", site))
		if err != nil {
			http.Error(w, "could not generate deploy key", http.StatusBadGateway)
			return
		}
		_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.git-deploy-key.created", site, "public key generated")
		writeJSON(w, http.StatusCreated, map[string]any{"site": site, "public_key": strings.TrimSpace(string(output))})
	case http.MethodDelete:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		releaseUnlock := a.siteOperations.Acquire(site)
		defer releaseUnlock()
		if err := runHelperCommandWithTimeout(r.Context(), a.Config, helperServiceLifecycleTimeout, a.Config.GitCtl, "delete", site); err != nil {
			http.Error(w, "could not retire deploy key", http.StatusBadGateway)
			return
		}
		_ = ShouldAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.git-deploy-key.deleted", site, "deploy key retired")
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
