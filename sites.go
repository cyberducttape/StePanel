package stepanel

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/cyberducttape/StePanel/internal/domainname"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

var siteVHostNamePattern = regexp.MustCompile(`^site-[a-z0-9_-]{1,32}-[a-z0-9_-]+\.(conf|caddy)$`)

type siteRoute struct {
	Site         string `json:"site"`
	Domain       string `json:"domain"`
	PreviewToken string `json:"preview_token,omitempty"`
}

type vhostSnapshot struct {
	Exists bool   `json:"exists"`
	SHA256 string `json:"sha256,omitempty"`
	Bytes  int64  `json:"bytes,omitempty"`
}

const (
	maxVhostRecoverySnapshots = 256
	maxVhostRecoveryBytes     = int64(256 << 20)
)

func (a *App) snapshotVhost(name string) (vhostSnapshot, error) {
	file, info, err := openManagedVhost(a.Config.VHostRoot, name)
	if os.IsNotExist(err) {
		return vhostSnapshot{}, nil
	}
	if err != nil {
		return vhostSnapshot{}, err
	}
	defer file.Close()
	if info.Size() > 2<<20 {
		return vhostSnapshot{}, errors.New("managed vhost is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return vhostSnapshot{}, err
	}
	if len(data) > 2<<20 {
		return vhostSnapshot{}, errors.New("managed vhost exceeds 2 MiB")
	}
	digest := sha256.Sum256(data)
	return vhostSnapshot{Exists: true, SHA256: fmt.Sprintf("%x", digest), Bytes: info.Size()}, nil
}

func (a *App) preserveVhostForRecovery(name string, expected vhostSnapshot) (string, error) {
	if !expected.Exists {
		return "", nil
	}
	file, info, err := openManagedVhost(a.Config.VHostRoot, name)
	if err != nil || info.Size() > 2<<20 {
		return "", errors.New("current vhost is unavailable for recovery snapshot")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (2<<20)+1))
	if err != nil {
		return "", err
	}
	if len(data) > 2<<20 {
		return "", errors.New("current vhost exceeds 2 MiB")
	}
	digest := sha256.Sum256(data)
	if fmt.Sprintf("%x", digest) != expected.SHA256 {
		return "", errors.New("current vhost changed after preview")
	}
	root := a.Config.RecoveryRoot
	if root == "" {
		return "", errors.New("recovery root is not configured")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("recovery root is not a real directory")
	}
	dir := filepath.Join(root, "webserver-config")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("web-server recovery directory is not a real directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var retainedBytes int64
	retainedCount := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".snapshot") {
			continue
		}
		info, err := os.Lstat(filepath.Join(dir, entry.Name()))
		if err != nil || !info.Mode().IsRegular() {
			return "", errors.New("web-server recovery directory contains an invalid snapshot entry")
		}
		retainedCount++
		retainedBytes += info.Size()
	}
	if retainedCount >= maxVhostRecoverySnapshots || retainedBytes+int64(len(data)) > maxVhostRecoveryBytes {
		return "", errors.New("web-server recovery snapshot capacity is full; archive or remove old snapshots before changing configuration")
	}
	random, err := randomSecret()
	if err != nil {
		return "", err
	}
	filename := fmt.Sprintf("%d-%s-%x-%s.snapshot", time.Now().UTC().UnixNano(), random[:12], digest[:6], name)
	if err := writeAtomic(filepath.Join(dir, filename), data, 0600); err != nil {
		return "", err
	}
	return filepath.Join("webserver-config", filename), nil
}

// openManagedVhost opens a single managed configuration file relative to the
// configured vhost directory, rejecting path components and replacement races.
func openManagedVhost(rootPath, name string) (*os.File, os.FileInfo, error) {
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." || !siteVHostNamePattern.MatchString(name) {
		return nil, nil, errors.New("invalid managed vhost name")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("managed vhost is not a regular file")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = file.Close()
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, errors.New("managed vhost changed while opening")
	}
	return file, after, nil
}

func (a *App) routeStateFor(name string) (RouteDesired, bool) {
	if a.Routes != nil {
		for _, route := range a.Routes.list() {
			if route.Name == name {
				return route, true
			}
		}
	}
	return RouteDesired{}, false
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
	// Account, Kind and Directory describe a site placed in a hosting
	// account (ADR-0007): its main public_html or an addon/subdomain root.
	Account   string `json:"account,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Directory string `json:"directory,omitempty"`
}

func (a *App) canAccessSite(r *http.Request, site string) bool {
	return a.Auth.IsAdministrator(r) || a.Accounts != nil && a.Accounts.OwnsSite(a.Auth.UsernameForRequest(r), site)
}

func (a *App) siteOverviewList(w http.ResponseWriter, r *http.Request) {
	if !a.requireCustomerScope(w, r, "site:read") {
		return
	}
	var result []siteOverview
	var err error
	if a.MetadataCache != nil {
		result, err = a.MetadataCache.GetOrBuildSiteOverviews(a.buildSiteOverviewSnapshot)
	} else {
		result, err = a.buildSiteOverviewSnapshot()
	}
	if err != nil {
		http.Error(w, "unable to inspect managed site overview", http.StatusInternalServerError)
		return
	}

	// The cached snapshot is deliberately unscoped. Apply the current principal's
	// authorization on every request; never cache tenant access decisions.
	username := a.Auth.UsernameForRequest(r)
	isAdministrator := subtle.ConstantTimeCompare([]byte(username), []byte(a.Auth.Username)) == 1
	authorized := make([]siteOverview, 0, len(result))
	for _, site := range result {
		if isAdministrator || a.Accounts != nil && a.Accounts.OwnsSite(username, site.Site) {
			authorized = append(authorized, site)
		}
	}

	response := map[string]any{
		"sites": authorized,
		"time":  time.Now().UTC(),
	}
	if a.MetadataCache != nil {
		response["metadata_age_sec"] = a.MetadataCache.StalenessSeconds() // How many seconds old the cached metadata is
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) buildSiteOverviewSnapshot() ([]siteOverview, error) {
	sites := make(map[string]*siteOverview)
	var siteNames []string
	if a.MetadataCache != nil {
		if cached, ok := a.MetadataCache.GetSites(); ok {
			siteNames = cached
		} else {
			var err error
			siteNames, err = allManagedSiteNames(a.Config)
			if err != nil {
				return nil, fmt.Errorf("inspect managed sites: %w", err)
			}
			a.MetadataCache.SetSites(siteNames)
		}
	} else {
		var err error
		siteNames, err = allManagedSiteNames(a.Config)
		if err != nil {
			return nil, fmt.Errorf("inspect managed sites: %w", err)
		}
	}

	siteRoots, rootProblems, rootsErr := existingManagedSiteRoots(a.Config.WebRoot)
	if rootsErr != nil && !errors.Is(rootsErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect managed sites: %w", rootsErr)
	}
	for _, siteName := range siteNames {
		siteRoot, resolveErr := siteRoots[siteName], rootProblems[siteName]
		if siteRoot == "" && resolveErr == nil {
			resolveErr = os.ErrNotExist
		}
		overview, err := siteOverviewAt(a.Config, siteName, siteRoot, resolveErr)
		if err != nil {
			return nil, fmt.Errorf("inspect managed site document roots: %w", err)
		}
		sites[siteName] = overview
	}

	var apps []AppManifest
	if a.MetadataCache != nil {
		if cached, ok := a.MetadataCache.GetApps(); ok {
			apps = cached
		} else {
			var err error
			apps, err = managedAppsWithError(a.Config.AppRoot)
			if err != nil {
				return nil, fmt.Errorf("inspect application manifests: %w", err)
			}
			a.MetadataCache.SetApps(apps)
		}
	} else {
		var err error
		apps, err = managedAppsWithError(a.Config.AppRoot)
		if err != nil {
			return nil, fmt.Errorf("inspect application manifests: %w", err)
		}
	}
	for _, app := range apps {
		if sites[app.Site] == nil {
			overview, err := newSiteOverview(a.Config, app.Site)
			if err != nil {
				return nil, fmt.Errorf("inspect managed site document roots: %w", err)
			}
			sites[app.Site] = overview
		}
		sites[app.Site].Applications = append(sites[app.Site].Applications, app)
	}

	if strings.TrimSpace(a.Config.DBCtl) != "" {
		databases, err := a.cachedManagedDatabaseInventory()
		if err != nil {
			return nil, fmt.Errorf("inspect managed database inventory: %w", err)
		}
		for _, database := range databases {
			if sites[database.Site] == nil {
				overview, err := newSiteOverview(a.Config, database.Site)
				if err != nil {
					return nil, fmt.Errorf("inspect managed site document roots: %w", err)
				}
				sites[database.Site] = overview
			}
			sites[database.Site].DatabaseCount++
		}
	}

	result := make([]siteOverview, 0, len(sites))
	for _, site := range sites {
		if a.MetadataCache != nil {
			if cached, ok := a.MetadataCache.GetRoutes(site.Site); ok {
				site.Routes = cached
			} else {
				routes, err := siteRoutesForWithError(a.Config.VHostRoot, site.Site)
				if err != nil {
					return nil, fmt.Errorf("inspect managed site routes: %w", err)
				}
				site.Routes = routes
				a.MetadataCache.SetRoutes(site.Site, routes)
			}
			if cached, ok := a.MetadataCache.GetProxies(site.Site); ok {
				site.Proxies = cached
			} else {
				proxies, err := siteProxiesForWithError(a.Config.ProxyRoot, site.Site)
				if err != nil {
					return nil, fmt.Errorf("inspect proxy configurations: %w", err)
				}
				site.Proxies = proxies
				a.MetadataCache.SetProxies(site.Site, proxies)
			}
		} else {
			var err error
			site.Routes, err = siteRoutesForWithError(a.Config.VHostRoot, site.Site)
			if err != nil {
				return nil, fmt.Errorf("inspect managed site routes: %w", err)
			}
			site.Proxies, err = siteProxiesForWithError(a.Config.ProxyRoot, site.Site)
			if err != nil {
				return nil, fmt.Errorf("inspect proxy configurations: %w", err)
			}
		}
		sort.Slice(site.Routes, func(i, j int) bool { return site.Routes[i].Domain < site.Routes[j].Domain })
		sort.Slice(site.Applications, func(i, j int) bool { return site.Applications[i].Domain < site.Applications[j].Domain })
		result = append(result, *site)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Site < result[j].Site })
	return result, nil
}

// allManagedSiteNames lists legacy sites under the web root and sites placed
// in hosting accounts (ADR-0007), without duplicates.
func allManagedSiteNames(cfg Config) ([]string, error) {
	names, err := managedSiteNames(filepath.Join(cfg.WebRoot, "sites"))
	if err != nil {
		return nil, err
	}
	db := sitePlacementDB.Load()
	if db == nil {
		return names, nil
	}
	placements, err := sitelayout.AllSites(db)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		seen[name] = true
	}
	for _, placement := range placements {
		if !seen[placement.Site] {
			names = append(names, placement.Site)
			seen[placement.Site] = true
		}
	}
	return names, nil
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
	if location, locationErr := siteLocationFor(cfg, site); locationErr != nil {
		return nil, locationErr
	} else if location.Placed {
		info, statErr := os.Lstat(location.DocRoot)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
		return &siteOverview{
			Site: site, DocumentRoot: location.DocRoot, Routes: []siteRoute{}, Applications: []AppManifest{}, Proxies: []proxyInfo{},
			Exists:  statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0,
			Account: location.Placement.Account, Kind: string(location.Placement.Kind), Directory: location.Placement.Directory,
		}, nil
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			missingRoot, pathErr := siteDocRoot(cfg, site)
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
		data, err := readStateFile(filepath.Join(root, entry.Name()), maxAppStateFileBytes)
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

func (a *App) siteList(w http.ResponseWriter, r *http.Request) {
	if !a.Auth.IsAdministrator(r) {
		if a.Accounts == nil {
			http.Error(w, "shared-hosting accounts are unavailable", http.StatusServiceUnavailable)
			return
		}
		account, ok := a.Accounts.Get(a.Auth.UsernameForRequest(r))
		if !ok || a.Accounts.TenantSuspended(account.Username) {
			http.Error(w, "account is unavailable", http.StatusForbidden)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sites": append([]string(nil), account.Sites...)})
		return
	}
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
	publicRoot, err := siteDocRoot(a.Config, input.Site)
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
	beforeFile, err := a.snapshotVhost(name)
	if err != nil {
		http.Error(w, "could not safely inspect current web-server configuration", http.StatusServiceUnavailable)
		return
	}
	previousRoute, hasPreviousRoute := a.routeStateFor(name)
	if beforeFile.Exists && !hasPreviousRoute {
		http.Error(w, "a vhost file already exists without managed route state; refusing to overwrite it", http.StatusConflict)
		return
	}
	before := struct {
		File  vhostSnapshot `json:"vhost"`
		Route *RouteDesired `json:"route,omitempty"`
	}{File: beforeFile}
	if hasPreviousRoute {
		before.Route = &previousRoute
	}
	proposedChange := struct {
		Site      string `json:"site"`
		Domain    string `json:"domain"`
		WebServer string `json:"web_server"`
		VHostPath string `json:"vhost_path"`
	}{input.Site, input.Domain, a.Config.WebServer, filepath.Join(a.Config.VHostRoot, name)}
	if r.Method == http.MethodPost && input.PreviewToken == "" {
		recovery := []string{"If activation fails, inspect the route's pending state and web-server error log before retrying."}
		if hasPreviousRoute {
			recovery = append(recovery, "A mode-0600 copy of the prior vhost will be saved under STEPANEL_RECOVERY_ROOT/webserver-config before mutation; restore that file, validate the web server, and reload it to roll back.")
		} else {
			recovery = append(recovery, "Roll back by deleting this route; DNS and the site files remain unchanged.")
		}
		preview, err := a.makeChangePreview("domain.route.apply:"+name, before, proposedChange,
			[]string{"Write the managed virtual-host file for this site and domain.", "Validate and reload the selected web server; the domain may begin serving this site.", "DNS records are not changed by this operation."}, recovery)
		if err != nil {
			http.Error(w, "could not create domain change preview", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, preview)
		return
	}
	if err := changePreviewRequired(input.PreviewToken); err != nil || !a.verifyChangePreview(input.PreviewToken, "domain.route.apply:"+name, before, proposedChange) {
		http.Error(w, "route configuration changed or its preview expired; generate a new preview", http.StatusConflict)
		return
	}
	intent, err := BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "site.deployed", input.Site, input.Domain)
	if err != nil {
		refuseWithoutSecurityAudit(w)
		return
	}
	var outcome string
	defer intent.Finish(&outcome, "route was not published")
	recoverySnapshot, err := a.preserveVhostForRecovery(name, beforeFile)
	if err != nil {
		intent.Failed("prior vhost recovery snapshot could not be created")
		http.Error(w, "could not preserve the prior vhost for recovery; route was not changed", http.StatusServiceUnavailable)
		return
	}
	if a.Routes != nil {
		route := routeState(name, input.Site, input.Domain, "pending")
		if err := a.Routes.save(route); err != nil {
			http.Error(w, "could not persist route desired state", http.StatusServiceUnavailable)
			return
		}
	}
	if err := runVhostMutation(operationCtx, a.Config, "apply", input.Site, input.Domain); err != nil {
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
	outcome = input.Domain + " config=" + name
	if recoverySnapshot != "" {
		outcome += " recovery_snapshot=" + recoverySnapshot
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
	writeJSON(w, http.StatusAccepted, map[string]string{"site": input.Site, "domain": input.Domain, "config": filepath.Join(a.Config.VHostRoot, name), "recovery_snapshot": recoverySnapshot})
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
	desired, hasDesired := a.routeStateFor(name)
	if hasDesired {
		if _, ok := a.requireSiteAccess(w, r, desired.Site, "site route is not assigned to this account", http.StatusForbidden); !ok {
			return
		}
	} else if !a.Auth.IsAdministrator(r) {
		http.Error(w, "site route is not assigned to this account", http.StatusForbidden)
		return
	}
	var input struct {
		PreviewToken string `json:"preview_token"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil {
		http.Error(w, "a current route deletion preview is required", http.StatusConflict)
		return
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
	if !hasDesired {
		http.Error(w, "route has no durable ownership record; deletion preview is unavailable", http.StatusConflict)
		return
	}
	currentFile, snapshotErr := a.snapshotVhost(name)
	if snapshotErr != nil {
		http.Error(w, "could not safely inspect current web-server configuration", http.StatusServiceUnavailable)
		return
	}
	currentRoute, routeExists := a.routeStateFor(name)
	before := struct {
		File  vhostSnapshot `json:"vhost"`
		Route *RouteDesired `json:"route,omitempty"`
	}{File: currentFile}
	if routeExists {
		before.Route = &currentRoute
	}
	proposed := struct {
		Action    string `json:"action"`
		Name      string `json:"name"`
		WebServer string `json:"web_server"`
		VHostPath string `json:"vhost_path"`
	}{"delete", name, a.Config.WebServer, filepath.Join(a.Config.VHostRoot, name)}
	if !routeExists || !a.verifyChangePreview(input.PreviewToken, "domain.route.delete:"+name, before, proposed) {
		http.Error(w, "route changed or its preview expired; generate a new preview", http.StatusConflict)
		return
	}
	// Route removal is Class A. The intent is recorded immediately before
	// the first mutation, which differs between managed and unmanaged routes.
	var intent *SecurityAudit
	var outcome string
	beginDeletion := func() bool {
		if intent != nil {
			return true
		}
		var err error
		if intent, err = BeginSecurityAudit(a.Config.AuditLog, a.Auth.AuditActor(r), "site.deleted", name, "managed PHP vhost removal"); err != nil {
			refuseWithoutSecurityAudit(w)
			return false
		}
		return true
	}
	defer func() {
		if intent != nil {
			intent.Finish(&outcome, "route was not removed")
		}
	}()
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for site deployment", http.StatusForbidden)
		return
	}
	if !beginDeletion() {
		return
	}
	recoverySnapshot, err := a.preserveVhostForRecovery(name, currentFile)
	if err != nil {
		intent.Failed("prior vhost recovery snapshot could not be created")
		http.Error(w, "could not preserve the prior vhost for recovery; route was not removed", http.StatusServiceUnavailable)
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
	if err := runVhostMutation(operationCtx, a.Config, "delete", name); err != nil {
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
	outcome = "managed PHP vhost removed recovery_snapshot=" + recoverySnapshot
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
	writeJSON(w, http.StatusOK, map[string]string{"deleted": name, "recovery_snapshot": recoverySnapshot})
}

func (a *App) siteRouteDeletePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !a.Auth.CSRF(r) {
		http.Error(w, "invalid request", http.StatusForbidden)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, 4096, &input); err != nil || !siteVHostNamePattern.MatchString(input.Name) {
		http.Error(w, "invalid site route", http.StatusUnprocessableEntity)
		return
	}
	route, ok := a.routeStateFor(input.Name)
	if !ok {
		http.Error(w, "route has no durable ownership record", http.StatusConflict)
		return
	}
	if _, ok := a.requireSiteAccess(w, r, route.Site, "site route is not assigned to this account", http.StatusForbidden); !ok {
		return
	}
	if !a.Auth.HasRequiredCustomerScope(r, "site:deploy") && !a.Auth.IsAdministrator(r) {
		http.Error(w, "insufficient token scope for site deployment", http.StatusForbidden)
		return
	}
	operationCtx, releaseUnlock, lockErr := a.acquireSiteMutationLocksContext(r.Context(), route.Site, "vhost:"+input.Name)
	if lockErr != nil {
		http.Error(w, "route is busy", http.StatusConflict)
		return
	}
	defer releaseUnlock()
	file, err := a.snapshotVhost(input.Name)
	if err != nil {
		http.Error(w, "could not safely inspect current web-server configuration", http.StatusServiceUnavailable)
		return
	}
	if !file.Exists {
		http.Error(w, "route configuration is missing", http.StatusConflict)
		return
	}
	current, ok := a.routeStateFor(input.Name)
	if !ok {
		http.Error(w, "route changed during preview", http.StatusConflict)
		return
	}
	before := struct {
		File  vhostSnapshot `json:"vhost"`
		Route RouteDesired  `json:"route"`
	}{file, current}
	proposed := struct {
		Action    string `json:"action"`
		Name      string `json:"name"`
		WebServer string `json:"web_server"`
		VHostPath string `json:"vhost_path"`
	}{"delete", input.Name, a.Config.WebServer, filepath.Join(a.Config.VHostRoot, input.Name)}
	preview, err := a.makeChangePreview("domain.route.delete:"+input.Name, before, proposed,
		[]string{"Remove the managed virtual-host configuration and reload the web server.", "DNS, site files, databases, and the domain registration are unchanged."},
		[]string{"A mode-0600 copy of the prior vhost is saved under STEPANEL_RECOVERY_ROOT/webserver-config before removal; restore it, validate the web server, and reload it to roll back.", "The route desired-state record is removed only after the web-server change succeeds."})
	if err != nil {
		http.Error(w, "could not create route deletion preview", http.StatusInternalServerError)
		return
	}
	if err := operationCtx.Err(); err != nil {
		http.Error(w, "route preview cancelled because the mutation lock was lost", http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, preview)
}
