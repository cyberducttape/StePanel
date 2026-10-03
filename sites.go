package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
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
	if !a.requireCustomerScope(w, r, "site:read") {
		return
	}
	sites := map[string]*siteOverview{}

	// Get site list (with caching)
	var siteNames []string
	if a.MetadataCache != nil {
		if cached, ok := a.MetadataCache.GetSites(); ok {
			siteNames = cached
		} else {
			var err error
			siteNames, err = managedSiteNames(filepath.Join(a.Config.WebRoot, "sites"))
			if err != nil {
				http.Error(w, "unable to inspect managed sites", http.StatusInternalServerError)
				return
			}
			a.MetadataCache.SetSites(siteNames)
		}
	} else {
		// Fallback for tests without initialized cache
		var err error
		siteNames, err = managedSiteNames(filepath.Join(a.Config.WebRoot, "sites"))
		if err != nil {
			http.Error(w, "unable to inspect managed sites", http.StatusInternalServerError)
			return
		}
	}

	siteRoots, rootProblems, rootsErr := existingManagedSiteRoots(a.Config.WebRoot)
	if rootsErr != nil && !errors.Is(rootsErr, os.ErrNotExist) {
		http.Error(w, "unable to inspect managed sites", http.StatusInternalServerError)
		return
	}
	for _, siteName := range siteNames {
		siteRoot, resolveErr := siteRoots[siteName], rootProblems[siteName]
		if siteRoot == "" && resolveErr == nil {
			resolveErr = os.ErrNotExist
		}
		overview, err := siteOverviewAt(a.Config, siteName, siteRoot, resolveErr)
		if err != nil {
			http.Error(w, "unable to inspect managed site document roots", http.StatusInternalServerError)
			return
		}
		sites[siteName] = overview
	}

	// Get apps (with caching)
	var apps []AppManifest
	if a.MetadataCache != nil {
		if cached, ok := a.MetadataCache.GetApps(); ok {
			apps = cached
		} else {
			var err error
			apps, err = managedAppsWithError(a.Config.AppRoot)
			if err != nil {
				http.Error(w, "unable to inspect application manifests", http.StatusInternalServerError)
				return
			}
			a.MetadataCache.SetApps(apps)
		}
	} else {
		// Fallback for tests without initialized cache
		var err error
		apps, err = managedAppsWithError(a.Config.AppRoot)
		if err != nil {
			http.Error(w, "unable to inspect application manifests", http.StatusInternalServerError)
			return
		}
	}

	for _, app := range apps {
		if sites[app.Site] == nil {
			overview, err := newSiteOverview(a.Config, app.Site)
			if err != nil {
				http.Error(w, "unable to inspect managed site document roots", http.StatusInternalServerError)
				return
			}
			sites[app.Site] = overview
		}
		sites[app.Site].Applications = append(sites[app.Site].Applications, app)
	}

	if strings.TrimSpace(a.Config.DBCtl) != "" {
		databases, err := a.cachedManagedDatabaseInventory()
		if err != nil {
			http.Error(w, "unable to inspect managed database inventory", http.StatusInternalServerError)
			return
		}
		for _, database := range databases {
			if sites[database.Site] == nil {
				overview, err := newSiteOverview(a.Config, database.Site)
				if err != nil {
					http.Error(w, "unable to inspect managed site document roots", http.StatusInternalServerError)
					return
				}
				sites[database.Site] = overview
			}
			sites[database.Site].DatabaseCount++
		}
	}

	result := make([]*siteOverview, 0, len(sites))
	for _, site := range sites {
		if !a.canAccessSite(r, site.Site) {
			continue
		}

		// Get routes and proxies (with caching)
		if a.MetadataCache != nil {
			if cached, ok := a.MetadataCache.GetRoutes(site.Site); ok {
				site.Routes = cached
			} else {
				routes, err := siteRoutesForWithError(a.Config.VHostRoot, site.Site)
				if err != nil {
					http.Error(w, "unable to inspect managed site routes", http.StatusInternalServerError)
					return
				}
				site.Routes = routes
				a.MetadataCache.SetRoutes(site.Site, routes)
			}

			if cached, ok := a.MetadataCache.GetProxies(site.Site); ok {
				site.Proxies = cached
			} else {
				proxies, err := siteProxiesForWithError(a.Config.ProxyRoot, site.Site)
				if err != nil {
					http.Error(w, "unable to inspect proxy configurations", http.StatusInternalServerError)
					return
				}
				site.Proxies = proxies
				a.MetadataCache.SetProxies(site.Site, proxies)
			}
		} else {
			// Fallback for tests without initialized cache
			var err error
			site.Routes, err = siteRoutesForWithError(a.Config.VHostRoot, site.Site)
			if err != nil {
				http.Error(w, "unable to inspect managed site routes", http.StatusInternalServerError)
				return
			}
			site.Proxies, err = siteProxiesForWithError(a.Config.ProxyRoot, site.Site)
			if err != nil {
				http.Error(w, "unable to inspect proxy configurations", http.StatusInternalServerError)
				return
			}
		}

		sort.Slice(site.Routes, func(i, j int) bool { return site.Routes[i].Domain < site.Routes[j].Domain })
		sort.Slice(site.Applications, func(i, j int) bool { return site.Applications[i].Domain < site.Applications[j].Domain })
		result = append(result, site)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Site < result[j].Site })

	response := map[string]any{
		"sites": result,
		"time":  time.Now().UTC(),
	}
	if a.MetadataCache != nil {
		response["metadata_age_sec"] = a.MetadataCache.StalenessSeconds() // How many seconds old the cached metadata is
	}
	writeJSON(w, http.StatusOK, response)
}

func managedSiteNames(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && safeUser(entry.Name()) != "" {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func (a *App) siteOverviewResource(w http.ResponseWriter, r *http.Request) {
	if !a.requireCustomerScope(w, r, "site:read") {
		return
	}
	site := strings.TrimPrefix(r.URL.Path, "/api/sites/overview/")
	if site == "" || strings.Contains(site, "/") || safeUser(site) == "" {
		http.Error(w, "invalid site", http.StatusUnprocessableEntity)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, site, "site is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	overview, err := newSiteOverview(a.Config, site)
	if err != nil {
		http.Error(w, "unable to inspect managed site document root", http.StatusInternalServerError)
		return
	}
	overview.Routes, err = siteRoutesForWithError(a.Config.VHostRoot, site)
	if err != nil {
		http.Error(w, "unable to inspect managed site routes", http.StatusInternalServerError)
		return
	}
	overview.Proxies, err = siteProxiesForWithError(a.Config.ProxyRoot, site)
	if err != nil {
		http.Error(w, "unable to inspect proxy configurations", http.StatusInternalServerError)
		return
	}
	apps, err := managedAppsWithError(a.Config.AppRoot)
	if err != nil {
		http.Error(w, "unable to inspect application manifests", http.StatusInternalServerError)
		return
	}
	for _, app := range apps {
		if app.Site == site {
			overview.Applications = append(overview.Applications, app)
		}
	}
	if strings.TrimSpace(a.Config.DBCtl) != "" {
		databases, err := a.cachedManagedDatabaseInventory()
		if err != nil {
			http.Error(w, "unable to inspect managed database inventory", http.StatusInternalServerError)
			return
		}
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

func newSiteOverview(cfg Config, site string) (*siteOverview, error) {
	siteRoot, err := existingManagedSiteRoot(cfg.WebRoot, site)
	return siteOverviewAt(cfg, site, siteRoot, err)
}

// siteOverviewAt builds an overview from an already resolved site root (or
// the error resolving it), so the list handler can resolve every site from
// one directory scan instead of rescanning the sites directory per site.
func siteOverviewAt(cfg Config, site, siteRoot string, err error) (*siteOverview, error) {
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			missingRoot, pathErr := safePath(cfg.WebRoot, "sites", site, "public")
			if pathErr != nil {
				return &siteOverview{Site: site, Routes: []siteRoute{}, Applications: []AppManifest{}, Proxies: []proxyInfo{}}, pathErr
			}
			return &siteOverview{Site: site, DocumentRoot: missingRoot, Routes: []siteRoute{}, Applications: []AppManifest{}, Proxies: []proxyInfo{}, Exists: false}, nil
		}
		return nil, err
	}
	root, pathErr := safePath(siteRoot, "public")
	if pathErr != nil {
		return &siteOverview{Site: site, Routes: []siteRoute{}, Applications: []AppManifest{}, Proxies: []proxyInfo{}}, pathErr
	}
	info, err := os.Stat(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	exists := err == nil && info.IsDir()
	return &siteOverview{Site: site, DocumentRoot: root, Routes: []siteRoute{}, Applications: []AppManifest{}, Proxies: []proxyInfo{}, Exists: exists}, nil
}

func siteRoutesFor(root, site string) []siteRoute {
	routes, _ := siteRoutesForWithError(root, site)
	return routes
}

func siteRoutesForWithError(root, site string) ([]siteRoute, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []siteRoute{}, nil
		}
		return nil, err
	}
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
	return routes, nil
}

func siteProxiesFor(root, site string) []proxyInfo {
	proxies, _ := siteProxiesForWithError(root, site)
	return proxies
}

func siteProxiesForWithError(root, site string) ([]proxyInfo, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []proxyInfo{}, nil
		}
		return nil, err
	}
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
	return proxies, nil
}

func managedApps(root string) []AppManifest {
	apps, _ := managedAppsWithError(root)
	return apps
}

func managedAppsWithError(root string) ([]AppManifest, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []AppManifest{}, nil
		}
		return nil, err
	}
	apps := []AppManifest{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read application manifest %s: %w", entry.Name(), err)
		}
		var app AppManifest
		if err := json.Unmarshal(data, &app); err != nil {
			return nil, fmt.Errorf("decode application manifest %s: %w", entry.Name(), err)
		}
		if safeUser(app.Site) == "" {
			return nil, fmt.Errorf("application manifest %s has an invalid site", entry.Name())
		}
		apps = append(apps, app)
	}
	return apps, nil
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
	if safeUser(input.Site) == "" || len(input.Domain)+len(input.Site) > 220 || !domainname.Valid(input.Domain) {
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
	// Route publication changes both site-owned state and the vhost helper
	// boundary.  Use the same compound lock set as route reconciliation and
	// deletion so publication cannot race a concurrent route removal.
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLocksContext(r.Context(), input.Site, "vhost:"+name)
	if lockErr != nil {
		http.Error(w, "site is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "route publication cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
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
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "route publication cancelled because the mutation lock was lost", http.StatusConflict)
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
	var desired RouteDesired
	hasDesired := false
	if a.Routes != nil {
		for _, route := range a.Routes.list() {
			if route.Name == name {
				desired, hasDesired = route, true
				break
			}
		}
	}
	lockKeys := []string{"vhost:" + name}
	if hasDesired {
		lockKeys = append(lockKeys, desired.Site)
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLocksContext(r.Context(), lockKeys...)
	if lockErr != nil {
		http.Error(w, "route is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "route deletion cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	if a.Routes != nil {
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
			if saveErr := a.SaveRouteState(desired); saveErr != nil {
				http.Error(w, "site route removal failed and pending state could not be persisted", http.StatusServiceUnavailable)
				return
			}
		}
		http.Error(w, "site route was not removed because validation or webserver reload failed", http.StatusServiceUnavailable)
		return
	}
	if err := operationCtx.Err(); err != nil {
		if hasDesired {
			desired.LastError = err.Error()
			if saveErr := a.SaveRouteState(desired); saveErr != nil {
				http.Error(w, "route deletion was cancelled and pending state could not be persisted", http.StatusServiceUnavailable)
				return
			}
		}
		http.Error(w, "route deletion cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	if hasDesired {
		if err := a.Routes.remove(name); err != nil {
			http.Error(w, "site route removed but desired-state cleanup failed", http.StatusServiceUnavailable)
			return
		}
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "route deletion cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	recordAudit(a.Config.AuditLog, a.Auth.UsernameForRequest(r), "site.deleted", name, "managed PHP vhost removed")
	writeJSON(w, http.StatusOK, map[string]string{"deleted": name})
}
