package main

import (
	"sync"
	"time"
)

// MetadataCache caches frequently-accessed filesystem metadata with TTL-based invalidation.
// This prevents expensive directory scans on every HTTP request when scaled to 1000+ sites.
type MetadataCache struct {
	mu sync.RWMutex

	// Site list cache
	sites        []string
	sitesExpires time.Time
	sitesError   error

	// Apps cache
	apps        []AppManifest
	appsExpires time.Time
	appsError   error

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

// InvalidateSite clears cache for a specific site (called after site operations).
func (mc *MetadataCache) InvalidateSite(site string) {
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
