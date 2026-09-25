package main

import (
	"errors"
	"log"
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
)

var nodeVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)
var domainPattern = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))+$`)

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
	version := strings.TrimPrefix(input.Version, "v")
	installed, err := safePath(a.Config.NVMDir, "versions", "node", "v"+version)
	if err != nil {
		http.Error(w, "invalid Node version", 422)
		return
	}
	if info, err := os.Stat(installed); err != nil || !info.IsDir() {
		http.Error(w, "requested Node version is not installed", 422)
		return
	}
	siteRoot, err := safePath(a.Config.WebRoot, "sites", input.Site)
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	if err := os.MkdirAll(siteRoot, 0750); err != nil {
		http.Error(w, "unable to prepare site root", 500)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "Node version mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "Node version mutation cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	nvmrc, err := safePath(siteRoot, ".nvmrc")
	if err != nil {
		http.Error(w, "invalid Node version path", 422)
		return
	}
	if err := writeAtomic(nvmrc, []byte("v"+version+"\n"), 0640); err != nil {
		http.Error(w, "unable to select Node version", 500)
		return
	}
	if err := AuditAs(a.Config.AuditLog, a.Auth.Username, "node.version.selected", input.Site, "Node v"+version); err != nil {
		log.Printf("node version selected but audit persistence is unavailable: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"site": input.Site, "version": "v" + version})
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
	if safeUser(input.Site) == "" || len(input.Domain)+len(input.Site) > 220 || !domainPattern.MatchString(strings.ToLower(input.Domain)) {
		http.Error(w, "invalid site or domain", 422)
		return
	}
	backend, err := localBackend(input.Backend)
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	name := proxyConfigName(a.Config.WebServer, input.Site, input.Domain)
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLocksContext(r.Context(), input.Site, "proxy:"+name)
	if lockErr != nil {
		http.Error(w, "proxy mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	path, err := safePath(a.Config.ProxyRoot, name)
	if err != nil {
		http.Error(w, "invalid proxy path", 422)
		return
	}
	if err := runHelperCommandWithTimeout(operationCtx, a.Config, helperConfigMutationTimeout, a.Config.ProxyCtl, "apply", input.Site, strings.ToLower(input.Domain), backend); err != nil {
		http.Error(w, "proxy helper rejected the configuration or webserver reload failed", http.StatusServiceUnavailable)
		return
	}
	if err := AuditAs(a.Config.AuditLog, a.Auth.Username, "proxy.deployed", input.Site, input.Domain+" -> "+backend); err != nil {
		log.Printf("proxy deployed but audit persistence is unavailable: %v", err)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"site": input.Site, "domain": input.Domain, "backend": backend, "config": path, "reloaded": true})
}

func (a *App) proxyList(w http.ResponseWriter, r *http.Request) {
	entries, _ := os.ReadDir(a.Config.ProxyRoot)
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
		http.Error(w, err.Error(), 422)
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
	path, err := safePath(a.Config.ProxyRoot, name)
	if err != nil {
		http.Error(w, "invalid proxy path", 422)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "proxy not found", http.StatusNotFound)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "proxy:"+name)
	if lockErr != nil {
		http.Error(w, "proxy mutation is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := runHelperCommandWithTimeout(operationCtx, a.Config, helperConfigMutationTimeout, a.Config.ProxyCtl, "delete", name); err != nil {
		http.Error(w, "proxy was not removed because the helper or webserver reload failed", http.StatusServiceUnavailable)
		return
	}
	if err := AuditAs(a.Config.AuditLog, a.Auth.Username, "proxy.deleted", name, "managed proxy removed"); err != nil {
		log.Printf("proxy deleted but audit persistence is unavailable: %v", err)
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
