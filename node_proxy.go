package stepanel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
)

var nodeVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

type proxyRequest struct {
	Site    string `json:"site"`
	Domain  string `json:"domain"`
	Backend string `json:"backend"`
}

type proxyInfo struct {
	Name   string `json:"name"`
	Config string `json:"config"`
}

func (a *App) nodeVersions(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(filepath.Join(a.Config.NVMDir, "versions", "node"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "unable to inspect NVM versions", 500)
		return
	}
	versions := []string{}
	for _, entry := range entries {
		if entry.IsDir() && nodeVersionPattern.MatchString(entry.Name()) {
			versions = append(versions, entry.Name())
		}
	}
	sort.Strings(versions)
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (a *App) selectNode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input struct{ Site, Version string }
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if safeUser(input.Site) == "" || !nodeVersionPattern.MatchString(input.Version) {
		http.Error(w, "invalid site or Node version", 422)
		return
	}
	if _, err := existingManagedSiteState(a.Config, input.Site); err != nil {
		http.Error(w, "site document root does not exist", http.StatusUnprocessableEntity)
		return
	}
	operationCtx := r.Context()
	var releaseUnlock func()
	var lockErr error
	operationCtx, releaseUnlock, lockErr = a.acquireSiteMutationLockContext(operationCtx, input.Site)
	if lockErr != nil {
		http.Error(w, "Node version mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "Node version mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	principal := siteOperationPrincipalFromRequest(a.Auth, r)
	if err := a.applyNodeSelection(operationCtx, principal, input.Site, input.Version); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"site": input.Site, "version": "v" + strings.TrimPrefix(input.Version, "v")})
}

func siteOperationPrincipalFromRequest(auth Auth, r *http.Request) siteOperationPrincipal {
	principal := siteOperationPrincipal{Actor: auth.UsernameForRequest(r), Token: auth.IsAPITokenRequest(r)}
	if scopes, ok := r.Context().Value(apiTokenScopesKey{}).([]string); ok {
		principal.Scopes = append([]string(nil), scopes...)
	}
	return principal
}

func (a *App) applyNodeSelection(ctx context.Context, principal siteOperationPrincipal, site, requestedVersion string) error {
	if safeUser(site) == "" || !nodeVersionPattern.MatchString(requestedVersion) {
		return errors.New("invalid site or Node version")
	}
	if !a.durableSiteOperationScopeAllowed(principal, "site:deploy") {
		return errors.New("Node selection is not authorized for this principal")
	}
	version := strings.TrimPrefix(requestedVersion, "v")
	installed, err := safePath(a.Config.NVMDir, "versions", "node", "v"+version)
	if err != nil {
		return errors.New("invalid Node version")
	}
	if info, err := os.Stat(installed); err != nil || !info.IsDir() {
		return errors.New("requested Node version is not installed")
	}
	siteRoot, err := existingManagedSiteState(a.Config, site)
	if err != nil {
		return errors.New("site document root does not exist")
	}
	nvmrc, err := safePath(siteRoot, ".nvmrc")
	if err != nil {
		return errors.New("invalid Node version path")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeAtomic(nvmrc, []byte("v"+version+"\n"), 0640); err != nil {
		return fmt.Errorf("unable to select Node version: %w", err)
	}
	TelemetryAudit(a.Config.AuditLog, principal.Actor, "node.version.selected", site, "Node v"+version)
	return ctx.Err()
}

func (a *App) deployProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input proxyRequest
	if err := decodeJSON(w, r, 8192, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if safeUser(input.Site) == "" || len(input.Domain)+len(input.Site) > 220 || !domainname.Valid(strings.ToLower(input.Domain)) {
		http.Error(w, "invalid site or domain", 422)
		return
	}
	backend, err := localBackend(input.Backend)
	if err != nil {
		writePublicError(w, r, http.StatusUnprocessableEntity, publicError("invalid_backend", "the proxy backend is invalid", err))
		return
	}
	name := proxyConfigName(a.Config.WebServer, input.Site, input.Domain)
	operationCtx := r.Context()
	var releaseUnlock func()
	var lockErr error
	operationCtx, releaseUnlock, lockErr = a.acquireSiteMutationLocksContext(operationCtx, input.Site, "proxy:"+name)
	if lockErr != nil {
		http.Error(w, "proxy mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "proxy mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	principal := siteOperationPrincipalFromRequest(a.Auth, r)
	path, err := a.applyProxyDeployment(operationCtx, principal, input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"site": input.Site, "domain": input.Domain, "backend": backend, "config": path, "reloaded": true})
}

func (a *App) applyProxyDeployment(ctx context.Context, principal siteOperationPrincipal, input proxyRequest) (string, error) {
	if safeUser(input.Site) == "" || len(input.Domain)+len(input.Site) > 220 || !domainname.Valid(strings.ToLower(input.Domain)) {
		return "", errors.New("invalid site or domain")
	}
	backend, err := localBackend(input.Backend)
	if err != nil {
		return "", fmt.Errorf("invalid proxy backend: %w", err)
	}
	if !a.durableSiteOperationScopeAllowed(principal, "site:deploy") {
		return "", errors.New("proxy deployment is not authorized for this principal")
	}
	name := proxyConfigName(a.Config.WebServer, input.Site, input.Domain)
	path, err := safePath(a.Config.ProxyRoot, name)
	if err != nil {
		return "", errors.New("invalid proxy path")
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, principal.Actor, "proxy.deployed", input.Site, input.Domain+" -> "+backend)
	if err != nil {
		return "", errors.New("security audit unavailable; proxy deployment refused")
	}
	if err := runProxyMutation(ctx, a.Config, "apply", input.Site, strings.ToLower(input.Domain), backend); err != nil {
		intent.Failed(err.Error())
		return "", errors.New("proxy helper rejected the configuration or webserver reload failed")
	}
	intent.Completed(input.Domain + " -> " + backend)
	if err := ctx.Err(); err != nil {
		return "", errors.New("proxy deployment cancelled because the mutation lock was lost")
	}
	return path, nil
}

func (a *App) proxyList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(a.Config.ProxyRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "unable to inspect proxy configurations", http.StatusInternalServerError)
		return
	}
	items := []proxyInfo{}
	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".conf") && !strings.HasSuffix(entry.Name(), ".caddy")) {
			continue
		}
		items = append(items, proxyInfo{Name: strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".conf"), ".caddy"), Config: filepath.Join(a.Config.ProxyRoot, entry.Name())})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"proxies": items})
}

func (a *App) proxyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input proxyRequest
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	backend, err := localBackend(input.Backend)
	if err != nil {
		writePublicError(w, r, http.StatusUnprocessableEntity, publicError("invalid_backend", "the proxy backend is invalid", err))
		return
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get("http://" + backend)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"reachable": false, "backend": backend, "error": err.Error()})
		return
	}
	defer response.Body.Close()
	writeJSON(w, http.StatusOK, map[string]any{"reachable": true, "backend": backend, "status": response.StatusCode})
}

func (a *App) proxyManage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/proxy/")
	if name == "" || strings.Contains(name, "/") || (!strings.HasSuffix(name, ".conf") && !strings.HasSuffix(name, ".caddy")) {
		http.Error(w, "invalid proxy", 422)
		return
	}
	if _, err := existingRegularEntry(a.Config.ProxyRoot, name); err != nil {
		http.Error(w, "proxy not found", http.StatusNotFound)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "proxy:"+name)
	if lockErr != nil {
		http.Error(w, "proxy mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "proxy mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "proxy.deleted", name, "managed proxy removal")
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	if err := runProxyMutation(operationCtx, a.Config, "delete", name); err != nil {
		intent.Failed(err.Error())
		http.Error(w, "proxy was not removed because the helper or webserver reload failed", http.StatusServiceUnavailable)
		return
	}
	intent.Completed("managed proxy removed")
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "proxy deletion cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name, "reloaded": true})
}

func proxyConfigName(webserver, site, domain string) string {
	ext := ".conf"
	if webserver == "caddy" {
		ext = ".caddy"
	}
	return site + "-" + strings.ReplaceAll(strings.ToLower(domain), ".", "_") + ext
}

func localBackend(value string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("backend must be a plain http URL")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("backend must include a port from 1 through 65535")
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		return net.JoinHostPort("localhost", strconv.Itoa(port)), nil
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || isCloudMetadataIP(ip) || !(ip.IsLoopback() || ip.IsPrivate()) {
		return "", errors.New("backend must target localhost or a private IP")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}

func isCloudMetadataIP(ip net.IP) bool {
	return ip.Equal(net.ParseIP("169.254.169.254")) || ip.Equal(net.ParseIP("100.100.100.200")) || ip.Equal(net.ParseIP("fd00:ec2::254"))
}
