package stepanel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/cyberducttape/StePanel/internal/rootbroker"
)

func gitDeployPublicKey(ctx context.Context, cfg Config, site string) (string, error) {
	if cfg.Production || labDirectRootBrokerEnabled() {
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
		if err != nil {
			return "", err
		}
		response, err := client.GitPublic(ctx, site)
		if err != nil {
			return "", err
		}
		if !response.OK {
			return "", errors.New(response.Error)
		}
		var details rootbroker.GitResponse
		if err := json.Unmarshal(response.Details, &details); err != nil {
			return "", err
		}
		return details.PublicKey, nil
	}
	if cfg.GitCtl == "" {
		return "", errors.New("Git helper is not configured")
	}
	output, err, _ := runAllowlistedHelperOutput(ctx, cfg, nil, cfg.GitCtl, "public", site)
	return strings.TrimSpace(string(output)), err
}

func gitDeployKeyGenerate(ctx context.Context, cfg Config, site string) (string, error) {
	if cfg.Production || labDirectRootBrokerEnabled() {
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
		if err != nil {
			return "", err
		}
		response, err := client.GitGenerate(ctx, site)
		if err != nil {
			return "", err
		}
		if !response.OK {
			return "", errors.New(response.Error)
		}
		var details rootbroker.GitResponse
		if err := json.Unmarshal(response.Details, &details); err != nil {
			return "", err
		}
		return details.PublicKey, nil
	}
	if cfg.GitCtl == "" {
		return "", errors.New("Git helper is not configured")
	}
	output, err, _ := runAllowlistedHelperOutput(ctx, cfg, nil, cfg.GitCtl, "generate", site)
	return strings.TrimSpace(string(output)), err
}

func deleteGitDeployKey(ctx context.Context, cfg Config, site string) error {
	if cfg.Production || labDirectRootBrokerEnabled() {
		client, err := rootbroker.NewClient("/usr/local/sbin/stepanel-root", cfg.WebRoot)
		if err != nil {
			return err
		}
		response, err := client.GitDelete(ctx, site)
		if err != nil {
			return err
		}
		if !response.OK {
			return errors.New(response.Error)
		}
		return nil
	}
	if cfg.GitCtl == "" {
		return errors.New("Git helper is not configured")
	}
	return runHelperCommandWithTimeout(ctx, cfg, helperServiceLifecycleTimeout, cfg.GitCtl, "delete", site)
}

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
		if !a.requireCustomerScope(w, r, "ssh:read") {
			return
		}
		publicKey, err := gitDeployPublicKey(r.Context(), a.Config, site)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"site": site, "configured": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"site": site, "configured": true, "public_key": publicKey})
	case http.MethodPost:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		if !a.requireCustomerScope(w, r, "ssh:write") {
			return
		}
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
		if lockErr != nil {
			http.Error(w, "deploy key operation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		publicKey, err := gitDeployKeyGenerate(operationCtx, a.Config, site)
		if err != nil {
			http.Error(w, "could not generate deploy key", http.StatusBadGateway)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.git-deploy-key.created", site, "public key generated")
		writeJSON(w, http.StatusCreated, map[string]any{"site": site, "public_key": publicKey})
	case http.MethodDelete:
		if !a.Auth.CSRF(r) {
			http.Error(w, "invalid CSRF token", http.StatusForbidden)
			return
		}
		if !a.requireCustomerScope(w, r, "ssh:write") {
			return
		}
		operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), site)
		if lockErr != nil {
			http.Error(w, "deploy key operation is busy", http.StatusConflict)
			return
		}
		defer releaseUnlock()
		if err := deleteGitDeployKey(operationCtx, a.Config, site); err != nil {
			http.Error(w, "could not retire deploy key", http.StatusBadGateway)
			return
		}
		recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.git-deploy-key.deleted", site, "deploy key retired")
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
