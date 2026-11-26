package stepanel

import (
	"sync"
	"time"
)

// MetadataCache caches frequently-accessed filesystem metadata with TTL-based invalidation.
// This prevents expensive directory scans on every HTTP request when scaled to 1000+ sites.
type MetadataCache struct {
	mu sync.RWMutex

	// The assembled site overview is expensive because it combines filesystem,
	// route, app, and database inventory. One snapshot is kept and refreshed
	// by at most one build at a time (stale-while-revalidate); overviewMu
	// guards only these fields and is never held during a build, so readers
	// and invalidations do not wait for a slow scan. Authorization is always
	// applied by the handler, never cached.
	overviewMu sync.Mutex
	// siteOverviews is the last good snapshot; overviewsGen is the
	// invalidation generation it was built in.
	siteOverviews   []siteOverview
	overviewsExpire time.Time
	overviewsGen    uint64
	// overviewInvalidGen is bumped by every invalidation; a snapshot from an
	// older generation predates a change and is never served as current.
	overviewInvalidGen uint64
	// overviewRefresh is non-nil while a build runs and is closed when it ends.
	overviewRefresh chan struct{}
	// overviewErr records the latest failed build without discarding the
	// snapshot.
	overviewErr   error
	overviewErrAt time.Time

	// Site list cache
	sites        []string
	sitesExpires time.Time

	// Apps cache
	apps        []AppManifest
	appsExpires time.Time

	// Routes cache: per-site routes
	routesCache map[string][]siteRoute
	routesMu    sync.RWMutex
	routesExp   map[string]time.Time

	// Proxies cache: per-site proxies
	proxiesCache map[string][]proxyInfo
	proxiesMu    sync.RWMutex
	proxiesExp   map[string]time.Time

	// TTL for each cache entry
	ttl time.Duration

	// Track last refresh time for staleness reporting
	lastRefresh time.Time
}

// NewMetadataCache creates a new metadata cache with a given TTL.
func NewMetadataCache(ttl time.Duration) *MetadataCache {
	return &MetadataCache{
		ttl:          ttl,
		routesCache:  make(map[string][]siteRoute),
		routesExp:    make(map[string]time.Time),
		proxiesCache: make(map[string][]proxyInfo),
		proxiesExp:   make(map[string]time.Time),
	}
}

// InvalidateAll clears all caches immediately (called when operations modify sites).
func (mc *MetadataCache) InvalidateAll() {
	mc.invalidateSiteOverviews()
	mc.mu.Lock()
	mc.sites = nil
	mc.sitesExpires = time.Time{}
	mc.apps = nil
	mc.appsExpires = time.Time{}
	mc.mu.Unlock()

	mc.routesMu.Lock()
	mc.routesCache = make(map[string][]siteRoute)
	mc.routesExp = make(map[string]time.Time)
	mc.routesMu.Unlock()

	mc.proxiesMu.Lock()
	mc.proxiesCache = make(map[string][]proxyInfo)
	mc.proxiesExp = make(map[string]time.Time)
	mc.proxiesMu.Unlock()
}

// InvalidateApps clears the cached application manifests (called after app operations).
func (mc *MetadataCache) InvalidateApps() {
	mc.invalidateSiteOverviews()
	mc.mu.Lock()
	mc.apps = nil
	mc.appsExpires = time.Time{}
	mc.mu.Unlock()
}

// InvalidateSite clears cache for a specific site (called after site operations).
func (mc *MetadataCache) InvalidateSite(site string) {
	mc.invalidateSiteOverviews()
	mc.mu.Lock()
	mc.sites = nil // Site list changed; invalidate entirely
	mc.sitesExpires = time.Time{}
	mc.mu.Unlock()

	mc.routesMu.Lock()
	delete(mc.routesCache, site)
	delete(mc.routesExp, site)
	mc.routesMu.Unlock()

	mc.proxiesMu.Lock()
	delete(mc.proxiesCache, site)
	delete(mc.proxiesExp, site)
	mc.proxiesMu.Unlock()
}

// GetOrBuildSiteOverviews returns a copy of the full overview snapshot, owned
// by the caller. Freshness is capped at the database inventory's five-second
// bound even when the general metadata cache has a longer TTL.
//
// It never holds a lock while building. A snapshot that is merely past its
// TTL is returned at once while one background build refreshes it; a missing
// or invalidated snapshot (a change happened since it was built) is waited
// for, so a reader never sees state from before a change it may have made.
// A failed build keeps the previous snapshot and is recorded. Authorization
// must be applied after this method.
func (mc *MetadataCache) GetOrBuildSiteOverviews(build func() ([]siteOverview, error)) ([]siteOverview, error) {
	for {
		mc.overviewMu.Lock()
		current := mc.siteOverviews != nil && mc.overviewsGen == mc.overviewInvalidGen
		if current {
			if !time.Now().Before(mc.overviewsExpire) && mc.overviewRefresh == nil {
				mc.startOverviewRefreshLocked(build)
			}
			snapshot := cloneSiteOverviews(mc.siteOverviews)
			mc.overviewMu.Unlock()
			return snapshot, nil
		}
		// No snapshot valid for the current generation: wait for a build that
		// starts in this generation.
		wanted := mc.overviewInvalidGen
		if mc.overviewRefresh == nil {
			mc.startOverviewRefreshLocked(build)
		}
		done := mc.overviewRefresh
		mc.overviewMu.Unlock()
		<-done
		mc.overviewMu.Lock()
		valid := mc.siteOverviews != nil && mc.overviewsGen >= wanted && mc.overviewsGen == mc.overviewInvalidGen
		switch {
		case valid:
			snapshot := cloneSiteOverviews(mc.siteOverviews)
			mc.overviewMu.Unlock()
			return snapshot, nil
		case mc.overviewErr != nil && mc.overviewInvalidGen == wanted:
			// The build for this generation failed; report it rather than
			// serve a snapshot from before the change or retry in a loop.
			err := mc.overviewErr
			mc.overviewMu.Unlock()
			return nil, err
		}
		// Invalidated again during the build: wait for the next one.
		mc.overviewMu.Unlock()
	}
}

// startOverviewRefreshLocked starts one background build of the overview for
// the current invalidation generation. The caller holds overviewMu.
func (mc *MetadataCache) startOverviewRefreshLocked(build func() ([]siteOverview, error)) {
	done := make(chan struct{})
	mc.overviewRefresh = done
	gen := mc.overviewInvalidGen
	go func() {
		items, err := build()
		mc.overviewMu.Lock()
		defer mc.overviewMu.Unlock()
		defer close(done)
		mc.overviewRefresh = nil
		if err != nil {
			mc.overviewErr, mc.overviewErrAt = err, time.Now()
			return
		}
		mc.overviewErr = nil
		// A build that raced an invalidation still replaces the snapshot as a
		// fallback, but keeps its older generation so it is not served as
		// current.
		mc.siteOverviews = cloneSiteOverviews(items)
		mc.overviewsGen = gen
		ttl := mc.ttl
		if ttl > overviewInventoryMaxAge {
			ttl = overviewInventoryMaxAge
		}
		// Expiry counts from the end of the build, so a slow build does not
		// return an already-expired snapshot.
		mc.overviewsExpire = time.Now().Add(ttl)
		mc.mu.Lock()
		mc.lastRefresh = time.Now()
		mc.mu.Unlock()
	}()
}

func cloneSiteOverviews(items []siteOverview) []siteOverview {
	cloned := make([]siteOverview, len(items))
	for i := range items {
		cloned[i] = items[i]
		cloned[i].Routes = append([]siteRoute{}, items[i].Routes...)
		cloned[i].Applications = append([]AppManifest{}, items[i].Applications...)
		cloned[i].Proxies = append([]proxyInfo{}, items[i].Proxies...)
	}
	return cloned
}

// invalidateSiteOverviews marks the snapshot as predating a change. It does
// not wait for a running build; the next reader waits for a fresh one.
func (mc *MetadataCache) invalidateSiteOverviews() {
	mc.overviewMu.Lock()
	mc.overviewInvalidGen++
	mc.overviewMu.Unlock()
}

// GetSites returns cached site list if fresh, otherwise returns empty and signals refresh needed.
func (mc *MetadataCache) GetSites() ([]string, bool) {
	mc.mu.RLock()
	if mc.sites != nil && time.Now().Before(mc.sitesExpires) {
		defer mc.mu.RUnlock()
		return mc.sites, true
	}
	mc.mu.RUnlock()
	return nil, false
}

// SetSites caches the site list with TTL.
func (mc *MetadataCache) SetSites(sites []string) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.sites = sites
	mc.sitesExpires = time.Now().Add(mc.ttl)
	mc.lastRefresh = time.Now()
}

// GetApps returns cached apps list if fresh, otherwise returns empty and signals refresh needed.
func (mc *MetadataCache) GetApps() ([]AppManifest, bool) {
	mc.mu.RLock()
	if mc.apps != nil && time.Now().Before(mc.appsExpires) {
		defer mc.mu.RUnlock()
		return mc.apps, true
	}
	mc.mu.RUnlock()
	return nil, false
}

// SetApps caches the apps list with TTL.
func (mc *MetadataCache) SetApps(apps []AppManifest) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.apps = apps
	mc.appsExpires = time.Now().Add(mc.ttl)
	mc.lastRefresh = time.Now()
}

// GetRoutes returns cached routes for a site if fresh, otherwise returns empty and signals refresh needed.
func (mc *MetadataCache) GetRoutes(site string) ([]siteRoute, bool) {
	mc.routesMu.RLock()
	routes, exists := mc.routesCache[site]
	expires, hasExp := mc.routesExp[site]
	mc.routesMu.RUnlock()

	if exists && hasExp && time.Now().Before(expires) {
		return routes, true
	}
	return nil, false
}

// SetRoutes caches the routes for a site with TTL.
func (mc *MetadataCache) SetRoutes(site string, routes []siteRoute) {
	mc.routesMu.Lock()
	defer mc.routesMu.Unlock()
	mc.routesCache[site] = routes
	mc.routesExp[site] = time.Now().Add(mc.ttl)
	mc.lastRefresh = time.Now()
}

// GetProxies returns cached proxies for a site if fresh, otherwise returns empty and signals refresh needed.
func (mc *MetadataCache) GetProxies(site string) ([]proxyInfo, bool) {
	mc.proxiesMu.RLock()
	proxies, exists := mc.proxiesCache[site]
	expires, hasExp := mc.proxiesExp[site]
	mc.proxiesMu.RUnlock()

	if exists && hasExp && time.Now().Before(expires) {
		return proxies, true
	}
	return nil, false
}

// SetProxies caches the proxies for a site with TTL.
func (mc *MetadataCache) SetProxies(site string, proxies []proxyInfo) {
	mc.proxiesMu.Lock()
	defer mc.proxiesMu.Unlock()
	mc.proxiesCache[site] = proxies
	mc.proxiesExp[site] = time.Now().Add(mc.ttl)
	mc.lastRefresh = time.Now()
}

// StalenessSeconds returns how many seconds since the cache was last refreshed.
// Useful for operators to know if the metadata is stale.
func (mc *MetadataCache) StalenessSeconds() int64 {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	if mc.lastRefresh.IsZero() {
		return -1 // Never refreshed
	}
	return int64(time.Since(mc.lastRefresh).Seconds())
}
