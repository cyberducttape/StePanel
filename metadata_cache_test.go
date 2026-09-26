package main

import (
	"testing"
	"time"
)

// TestMetadataCacheSites verifies sites cache works correctly.
func TestMetadataCacheSites(t *testing.T) {
	cache := NewMetadataCache(1 * time.Second)

	// Initially empty
	if _, ok := cache.GetSites(); ok {
		t.Fatal("cache should be empty initially")
	}

	// Set sites
	testSites := []string{"site1", "site2", "site3"}
	cache.SetSites(testSites)

	// Retrieve cached sites
	if sites, ok := cache.GetSites(); !ok || len(sites) != 3 {
		t.Fatalf("expected cached sites, got ok=%v, sites=%v", ok, sites)
	}

	// Wait for cache to expire
	time.Sleep(1100 * time.Millisecond)
	if _, ok := cache.GetSites(); ok {
		t.Fatal("cache should expire after TTL")
	}
}

// TestMetadataCacheApps verifies apps cache works correctly.
func TestMetadataCacheApps(t *testing.T) {
	cache := NewMetadataCache(1 * time.Second)

	// Initially empty
	if _, ok := cache.GetApps(); ok {
		t.Fatal("cache should be empty initially")
	}

	// Set apps
	testApps := []AppManifest{
		{Site: "site1", Domain: "app.example.com", Port: 3000},
		{Site: "site2", Domain: "api.example.com", Port: 8000},
	}
	cache.SetApps(testApps)

	// Retrieve cached apps
	if apps, ok := cache.GetApps(); !ok || len(apps) != 2 {
		t.Fatalf("expected cached apps, got ok=%v, apps=%v", ok, apps)
	}
}

// TestMetadataCacheRoutes verifies per-site routes cache.
func TestMetadataCacheRoutes(t *testing.T) {
	cache := NewMetadataCache(1 * time.Second)

	testRoutes := []siteRoute{
		{Site: "site1", Domain: "example.com"},
		{Site: "site1", Domain: "www.example.com"},
	}

	// Set routes for site1
	cache.SetRoutes("site1", testRoutes)

	// Retrieve routes
	if routes, ok := cache.GetRoutes("site1"); !ok || len(routes) != 2 {
		t.Fatalf("expected cached routes, got ok=%v", ok)
	}

	// Different site should not be cached
	if routes, ok := cache.GetRoutes("site2"); ok {
		t.Fatalf("site2 should not be cached, got routes=%v", routes)
	}
}

// TestMetadataCacheProxies verifies per-site proxies cache.
func TestMetadataCacheProxies(t *testing.T) {
	cache := NewMetadataCache(1 * time.Second)

	testProxies := []proxyInfo{
		{Name: "api", Config: "api.example.com"},
	}

	// Set proxies for site1
	cache.SetProxies("site1", testProxies)

	// Retrieve proxies
	if proxies, ok := cache.GetProxies("site1"); !ok || len(proxies) != 1 {
		t.Fatalf("expected cached proxies, got ok=%v", ok)
	}
}

// TestMetadataCacheInvalidation verifies cache invalidation.
func TestMetadataCacheInvalidation(t *testing.T) {
	cache := NewMetadataCache(100 * time.Second) // Long TTL

	testSites := []string{"site1", "site2"}
	cache.SetSites(testSites)

	// Verify cached
	if _, ok := cache.GetSites(); !ok {
		t.Fatal("sites should be cached")
	}

	// Invalidate all
	cache.InvalidateAll()

	// Should no longer be cached
	if sites, ok := cache.GetSites(); ok {
		t.Fatalf("sites should be invalidated, got sites=%v", sites)
	}
}

// TestMetadataCacheInvalidateSite verifies per-site invalidation.
func TestMetadataCacheInvalidateSite(t *testing.T) {
	cache := NewMetadataCache(100 * time.Second)

	testRoutes := []siteRoute{{Site: "site1", Domain: "example.com"}}
	cache.SetRoutes("site1", testRoutes)

	// Verify cached
	if _, ok := cache.GetRoutes("site1"); !ok {
		t.Fatal("routes should be cached")
	}

	// Invalidate site1
	cache.InvalidateSite("site1")

	// Site1 routes should no longer be cached
	if _, ok := cache.GetRoutes("site1"); ok {
		t.Fatal("site1 routes should be invalidated")
	}
}

// TestMetadataCacheStaleness verifies staleness reporting.
func TestMetadataCacheStaleness(t *testing.T) {
	cache := NewMetadataCache(1 * time.Second)

	// Initially never refreshed
	staleness := cache.StalenessSeconds()
	if staleness != -1 {
		t.Fatalf("staleness should be -1 before first refresh, got %d", staleness)
	}

	// Refresh
	cache.SetSites([]string{"site1"})
	staleness = cache.StalenessSeconds()
	if staleness < 0 {
		t.Fatalf("staleness should be >= 0 after refresh, got %d", staleness)
	}

	// Wait a bit
	time.Sleep(1100 * time.Millisecond)
	staleness = cache.StalenessSeconds()
	if staleness < 1 {
		t.Fatalf("staleness should be >= 1 after wait, got %d", staleness)
	}
}
