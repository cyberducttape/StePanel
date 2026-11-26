package stepanel

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMetadataCacheSiteOverviewBuildIsSingleflightAndInvalidated(t *testing.T) {
	cache := NewMetadataCache(time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	var builds atomic.Int32
	build := func() ([]siteOverview, error) {
		builds.Add(1)
		close(started)
		<-release
		return []siteOverview{{Site: "site-a", Routes: []siteRoute{{Site: "site-a", Domain: "a.example"}}}}, nil
	}

	const callers = 8
	var wg sync.WaitGroup
	results := make(chan []siteOverview, callers)
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := cache.GetOrBuildSiteOverviews(build)
			results <- items
			errs <- err
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for items := range results {
		if len(items) != 1 || items[0].Site != "site-a" || len(items[0].Routes) != 1 {
			t.Fatalf("unexpected cached overview: %#v", items)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("concurrent cold requests built %d snapshots, want 1", got)
	}
	cached, err := cache.GetOrBuildSiteOverviews(func() ([]siteOverview, error) {
		return nil, nil
	})
	if err != nil || cached[0].Routes[0].Domain != "a.example" {
		t.Fatalf("cached snapshot changed unexpectedly: %#v, %v", cached, err)
	}
	cache.InvalidateSite("site-a")
	if _, err := cache.GetOrBuildSiteOverviews(func() ([]siteOverview, error) {
		builds.Add(1)
		return []siteOverview{{Site: "site-b"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := builds.Load(); got != 2 {
		t.Fatalf("invalidation did not rebuild snapshot; build count = %d", got)
	}
}

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

// A snapshot past its TTL is served at once while one background build
// refreshes it, and the expiry counts from the end of that build.
func TestMetadataCacheServesStaleSnapshotDuringRefresh(t *testing.T) {
	cache := NewMetadataCache(20 * time.Millisecond)
	if _, err := cache.GetOrBuildSiteOverviews(func() ([]siteOverview, error) {
		return []siteOverview{{Site: "old"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	release := make(chan struct{})
	var builds atomic.Int32
	slow := func() ([]siteOverview, error) {
		builds.Add(1)
		<-release
		return []siteOverview{{Site: "new"}}, nil
	}
	for range 5 {
		start := time.Now()
		items, err := cache.GetOrBuildSiteOverviews(slow)
		if err != nil || items[0].Site != "old" {
			t.Fatalf("stale read = %v, %v; want the old snapshot", items, err)
		}
		if time.Since(start) > 100*time.Millisecond {
			t.Fatal("a stale read waited for the refresh")
		}
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		items, err := cache.GetOrBuildSiteOverviews(slow)
		if err == nil && items[0].Site == "new" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background refresh never landed: %v, %v", items, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("stale reads started %d refreshes, want 1", got)
	}
}

// Invalidation never waits for a running build, and a reader after it never
// receives a snapshot from before the change.
func TestMetadataCacheInvalidationDoesNotBlockAndIsNotServedStale(t *testing.T) {
	cache := NewMetadataCache(time.Minute)
	started, release := make(chan struct{}), make(chan struct{})
	// A build reads the state current when it starts: only the build that
	// began before the change sees "before-change". Every reader's build
	// function behaves like this, as the real inventory scan does.
	var changed atomic.Bool
	var once sync.Once
	build := func() ([]siteOverview, error) {
		if changed.Load() {
			return []siteOverview{{Site: "after-change"}}, nil
		}
		once.Do(func() { close(started) })
		<-release
		return []siteOverview{{Site: "before-change"}}, nil
	}
	go func() {
		_, _ = cache.GetOrBuildSiteOverviews(build)
	}()
	<-started
	invalidated := make(chan struct{})
	go func() {
		changed.Store(true)
		cache.InvalidateSite("site-a")
		close(invalidated)
	}()
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("invalidation waited for a running build")
	}
	result := make(chan []siteOverview, 1)
	go func() {
		items, _ := cache.GetOrBuildSiteOverviews(build)
		result <- items
	}()
	close(release)
	select {
	case items := <-result:
		if len(items) != 1 || items[0].Site != "after-change" {
			t.Fatalf("read after invalidation = %v, want a build from after the change", items)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the read after invalidation never completed")
	}
}

// A failed refresh keeps the usable snapshot; a cold failure is returned, not
// retried in a loop; and callers own their copy.
func TestMetadataCacheRefreshFailuresAndSnapshotOwnership(t *testing.T) {
	cold := NewMetadataCache(time.Minute)
	var coldBuilds atomic.Int32
	if _, err := cold.GetOrBuildSiteOverviews(func() ([]siteOverview, error) {
		coldBuilds.Add(1)
		return nil, errors.New("inventory unavailable")
	}); err == nil || coldBuilds.Load() != 1 {
		t.Fatalf("cold failure = %v after %d builds, want the error after one", err, coldBuilds.Load())
	}

	cache := NewMetadataCache(10 * time.Millisecond)
	items, err := cache.GetOrBuildSiteOverviews(func() ([]siteOverview, error) {
		return []siteOverview{{Site: "kept", Routes: []siteRoute{{Domain: "kept.example"}}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	items[0].Site, items[0].Routes[0].Domain = "mutated", "mutated.example"
	time.Sleep(20 * time.Millisecond)
	failing := func() ([]siteOverview, error) { return nil, errors.New("scan failed") }
	again, err := cache.GetOrBuildSiteOverviews(failing)
	if err != nil || again[0].Site != "kept" || again[0].Routes[0].Domain != "kept.example" {
		t.Fatalf("snapshot after a caller mutation and a failing refresh = %v, %v", again, err)
	}
	time.Sleep(20 * time.Millisecond)
	if still, err := cache.GetOrBuildSiteOverviews(failing); err != nil || still[0].Site != "kept" {
		t.Fatalf("a failed refresh discarded the usable snapshot: %v, %v", still, err)
	}
}
