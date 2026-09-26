package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var siteVHostNamePattern = regexp.MustCompile(`^site-[a-z0-9_-]{1,32}-[a-z0-9_-]+\.(conf|caddy)$`)

type siteRoute struct {
	Site   string `json:"site"`
	Domain string `json:"domain"`
}

// siteOverview is the read-only, site-centric view consumed by the dashboard
// and API clients. It deliberately reports paths and states, never credentials
// or environment values.
type siteOverview struct {
	Site          string        `json:"site"`
	DocumentRoot  string        `json:"document_root"`
	Routes        []siteRoute   `json:"routes"`
	Applications  []AppManifest `json:"applications"`
	Proxies       []proxyInfo   `json:"proxies"`
	DatabaseCount int           `json:"database_count"`
	Exists        bool          `json:"exists"`
}

func (a *App) canAccessSite(r *http.Request, site string) bool {
	return a.Auth.IsAdministrator(r) || a.Accounts != nil && a.Accounts.OwnsSite(a.Auth.UsernameForRequest(r), site)
}

func (a *App) siteOverviewList(w http.ResponseWriter, r *http.Request) {
	sites := map[string]*siteOverview{}
	entries, _ := os.ReadDir(filepath.Join(a.Config.WebRoot, "sites"))
	for _, entry := range entries {
		if entry.IsDir() && safeUser(entry.Name()) != "" {
			sites[entry.Name()] = newSiteOverview(a.Config, entry.Name())
		}
	}
	for _, app := range managedApps(a.Config.AppRoot) {
		if sites[app.Site] == nil {
			sites[app.Site] = newSiteOverview(a.Config, app.Site)
		}
		sites[app.Site].Applications = append(sites[app.Site].Applications, app)
	}
	if databases, err := managedDatabaseInventory(a.Config); err == nil {
		for _, database := range databases {
			if sites[database.Site] == nil {
				sites[database.Site] = newSiteOverview(a.Config, database.Site)
			}
			sites[database.Site].DatabaseCount++
		}
	}
	result := make([]*siteOverview, 0, len(sites))
	for _, site := range sites {
		if !a.canAccessSite(r, site.Site) {
			continue
		}
		site.Routes = siteRoutesFor(a.Config.VHostRoot, site.Site)
		site.Proxies = siteProxiesFor(a.Config.ProxyRoot, site.Site)
		sort.Slice(site.Routes, func(i, j int) bool { return site.Routes[i].Domain < site.Routes[j].Domain })
		sort.Slice(site.Applications, func(i, j int) bool { return site.Applications[i].Domain < site.Applications[j].Domain })
		result = append(result, site)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Site < result[j].Site })
	writeJSON(w, http.StatusOK, map[string]any{"sites": result, "time": time.Now().UTC()})
}

func (a *App) siteOverviewResource(w http.ResponseWriter, r *http.Request) {
	site := strings.TrimPrefix(r.URL.Path, "/api/sites/overview/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", http.StatusUnprocessableEntity)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	overview := newSiteOverview(a.Config, site)
	overview.Routes = siteRoutesFor(a.Config.VHostRoot, site)
	overview.Proxies = siteProxiesFor(a.Config.ProxyRoot, site)
	for _, app := range managedApps(a.Config.AppRoot) {
		if app.Site == site {
			overview.Applications = append(overview.Applications, app)
		}
	}
	if databases, err := managedDatabaseInventory(a.Config); err == nil {
		for _, database := range databases {
			if database.Site == site {
				overview.DatabaseCount++
			}
		}
	}
	if !overview.Exists && len(overview.Routes) == 0 && len(overview.Applications) == 0 && len(overview.Proxies) == 0 {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func newSiteOverview(cfg Config, site string) *siteOverview {
	root, pathErr := safePath(cfg.WebRoot, "sites", site, "public")
	if pathErr != nil {
		return &siteOverview{Site: site, Routes: []siteRoute{}, Applications: []AppManifest{}, Proxies: []proxyInfo{}}
	}
	_, err := os.Stat(root)
	return &siteOverview{Site: site, DocumentRoot: root, Routes: []siteRoute{}, Applications: []AppManifest{}, Proxies: []proxyInfo{}, Exists: err == nil}
}

func siteRoutesFor(root, site string) []siteRoute {
	entries, _ := os.ReadDir(root)
	routes := []siteRoute{}
	prefix := "site-" + site + "-"
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !siteVHostNamePattern.MatchString(name) || !strings.HasPrefix(name, prefix) {
			continue
		}
		domain := strings.TrimPrefix(name, prefix)
		domain = strings.TrimSuffix(strings.TrimSuffix(domain, ".conf"), ".caddy")
		domain = strings.ReplaceAll(domain, "_", ".")
		routes = append(routes, siteRoute{Site: site, Domain: domain})
	}
	return routes
}

func siteProxiesFor(root, site string) []proxyInfo {
	entries, _ := os.ReadDir(root)
	proxies := []proxyInfo{}
	prefix := site + "-"
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".conf") && !strings.HasSuffix(name, ".caddy") {
			continue
		}
		proxies = append(proxies, proxyInfo{Name: strings.TrimSuffix(strings.TrimSuffix(name, ".conf"), ".caddy"), Config: filepath.Join(root, name)})
	}
	sort.Slice(proxies, func(i, j int) bool { return proxies[i].Name < proxies[j].Name })
	return proxies
}

func managedApps(root string) []AppManifest {
	entries, _ := os.ReadDir(root)
	apps := []AppManifest{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		var app AppManifest
		if err == nil && json.Unmarshal(data, &app) == nil && safeUser(app.Site) != "" {
			apps = append(apps, app)
		}
	}
	return apps
}

func (a *App) siteList(w http.ResponseWriter, _ *http.Request) {
	entries, err := os.ReadDir(a.Config.VHostRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "unable to inspect managed site routes", http.StatusInternalServerError)
		return
	}
	routes := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && siteVHostNamePattern.MatchString(entry.Name()) {
			routes = append(routes, entry.Name())
		}
	}
	sort.Strings(routes)
	writeJSON(w, http.StatusOK, map[string]any{"sites": routes})
}

func (a *App) siteDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input siteRoute
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	input.Domain = strings.ToLower(strings.TrimSpace(input.Domain))
	if safeUser(input.Site) == "" || len(input.Domain)+len(input.Site) > 220 || !domainPattern.MatchString(input.Domain) {
		http.Error(w, "invalid site or domain", http.StatusUnprocessableEntity)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, input.Site, "site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for site deployment", http.StatusForbidden)
		return
	}
	if !a.Auth.IsAdministrator(r) {
		if a.Domains == nil {
			http.Error(w, "domain ownership must be verified before route activation", http.StatusConflict)
			return
		}
		verifyCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		verifyErr := a.verifyCustomerDomain(verifyCtx, input.Site, input.Domain)
		cancel()
		if verifyErr != nil {
			http.Error(w, "domain ownership must be verified before route activation", http.StatusConflict)
			return
		}
	}
	publicRoot, err := safePath(a.Config.WebRoot, "sites", input.Site, "public")
	if err != nil {
		http.Error(w, "invalid site root", http.StatusUnprocessableEntity)
		return
	}
	if info, err := os.Stat(publicRoot); err != nil || !info.IsDir() {
		http.Error(w, "site document root does not exist", http.StatusUnprocessableEntity)
		return
	}
	name := siteVHostConfigName(a.Config.WebServer, input.Site, input.Domain)
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), input.Site)
	if lockErr != nil {
		http.Error(w, "site is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if a.Routes != nil {
		route := routeState(name, input.Site, input.Domain, "pending")
		if err := a.Routes.save(route); err != nil {
			http.Error(w, "could not persist route desired state", http.StatusServiceUnavailable)
			return
		}
	}
	if err := runHelperCommand(operationCtx, a.Config, a.Config.VHostCtl, "apply", input.Site, input.Domain); err != nil {
		if a.Routes != nil {
			route := routeState(name, input.Site, input.Domain, "pending")
			route.LastError = err.Error()
			if persistErr := a.Routes.save(route); persistErr != nil {
				log.Printf("route helper failed and desired-state update failed: %v", persistErr)
			}
		}
		http.Error(w, "site helper rejected the route or webserver reload failed", http.StatusServiceUnavailable)
		return
	}
	if a.Routes != nil {
		route := routeState(name, input.Site, input.Domain, "applied")
		if err := a.Routes.save(route); err != nil {
			http.Error(w, "route applied but desired-state persistence failed", http.StatusServiceUnavailable)
			return
		}
	}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.deployed", input.Site, input.Domain)
	writeJSON(w, http.StatusAccepted, map[string]string{"site": input.Site, "domain": input.Domain, "config": filepath.Join(a.Config.VHostRoot, name)})
}

func siteVHostConfigName(webserver, site, domain string) string {
	extension := ".conf"
	if webserver == "caddy" {
		extension = ".caddy"
	}
	return "site-" + site + "-" + strings.ReplaceAll(strings.ToLower(domain), ".", "_") + extension
}

func (a *App) siteManage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/sites/")
	if !siteVHostNamePattern.MatchString(name) {
		http.Error(w, "invalid site route", http.StatusUnprocessableEntity)
		return
	}
	path, err := safePath(a.Config.VHostRoot, name)
	if err != nil {
		http.Error(w, "invalid site route", http.StatusUnprocessableEntity)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "site route not found", http.StatusNotFound)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLockContext(r.Context(), "vhost:"+name)
	if lockErr != nil {
		http.Error(w, "route is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	var desired RouteDesired
	hasDesired := false
	if a.Routes != nil {
		for _, route := range a.Routes.list() {
			if route.Name == name {
				desired, hasDesired = route, true
				break
			}
		}
		if hasDesired {
			if _, ok := a.requireSiteAccess(w, r, desired.Site, "site route is not assigned to this account", http.StatusForbidden); !ok {
				return
			}
			if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
				http.Error(w, "insufficient token scope for site deployment", http.StatusForbidden)
				return
			}
			desired.State, desired.LastError, desired.UpdatedAt = "delete-pending", "", time.Now().UTC()
			if err := a.Routes.save(desired); err != nil {
				http.Error(w, "could not persist route deletion state", http.StatusServiceUnavailable)
				return
			}
		}
	}
	if !a.Auth.IsAdministrator(r) && !hasDesired {
		http.Error(w, "site route is not assigned to this account", http.StatusForbidden)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, desired.Site, "site route is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	if err := runHelperCommand(operationCtx, a.Config, a.Config.VHostCtl, "delete", name); err != nil {
		if hasDesired {
			desired.LastError = err.Error()
			a.SaveRouteState(desired)
		}
		http.Error(w, "site route was not removed because validation or webserver reload failed", http.StatusServiceUnavailable)
		return
	}
	if hasDesired {
		if err := a.Routes.remove(name); err != nil {
			http.Error(w, "site route removed but desired-state cleanup failed", http.StatusServiceUnavailable)
			return
		}
	}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.deleted", name, "managed PHP vhost removed")
	writeJSON(w, http.StatusOK, map[string]string{"deleted": name})
}
