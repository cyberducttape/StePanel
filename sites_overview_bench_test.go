package stepanel

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// newOverviewScaleApp builds the 500-site legacy layout the capacity
// profile uses: one document root, one managed route, and one proxy file per
// site, so overview cost scales the way it does on a loaded host.
func newOverviewScaleApp(tb testing.TB, count int) *App {
	tb.Helper()
	root := tb.TempDir()
	webRoot := filepath.Join(root, "www")
	vhostRoot := filepath.Join(root, "vhosts")
	proxyRoot := filepath.Join(root, "proxies")
	for _, dir := range []string{vhostRoot, proxyRoot} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			tb.Fatal(err)
		}
	}
	for i := 0; i < count; i++ {
		site := fmt.Sprintf("cap%03d", i)
		if err := os.MkdirAll(filepath.Join(webRoot, "sites", site, "public"), 0o750); err != nil {
			tb.Fatal(err)
		}
		domain := site + ".example.test"
		route := filepath.Join(vhostRoot, "site-"+site+"-"+site+"_example_test.conf")
		if err := os.WriteFile(route, []byte("ServerName "+domain+"\n"), 0o640); err != nil {
			tb.Fatal(err)
		}
		proxy := filepath.Join(proxyRoot, site+"-api_"+site+"_example_test.conf")
		if err := os.WriteFile(proxy, []byte("ServerName api."+domain+"\n"), 0o640); err != nil {
			tb.Fatal(err)
		}
	}
	return &App{Config: Config{WebRoot: webRoot, VHostRoot: vhostRoot, ProxyRoot: proxyRoot, AppRoot: filepath.Join(root, "apps")}}
}

// BenchmarkSiteOverviewSnapshot500 measures one uncached overview rebuild,
// the cost a request pays every time the five-second snapshot expires.
func BenchmarkSiteOverviewSnapshot500(b *testing.B) {
	app := newOverviewScaleApp(b, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.MetadataCache = NewMetadataCache(0)
		if _, err := app.buildSiteOverviewSnapshot(); err != nil {
			b.Fatal(err)
		}
	}
}

// Overlapping site names share a filename prefix ("site-alice-" also starts
// "site-alice-shop-..."). The single-listing lookup must still attribute each
// route and proxy only to the site whose apply wrote it.
func TestSiteOverviewSingleListingKeepsOverlappingSitesSeparate(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	vhostRoot := filepath.Join(root, "vhosts")
	proxyRoot := filepath.Join(root, "proxies")
	for _, site := range []string{"alice", "alice-shop", "bob"} {
		if err := os.MkdirAll(filepath.Join(webRoot, "sites", site, "public"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(vhostRoot, "site-alice-alice_test.conf"), "ServerName alice.test\n")
	writeTestFile(t, filepath.Join(vhostRoot, "site-alice-shop-shop_test.conf"), "ServerName shop.test\n")
	writeTestFile(t, filepath.Join(vhostRoot, "site-bob-bob_test.caddy"), "bob.test {\n}\n")
	writeTestFile(t, filepath.Join(proxyRoot, "alice-api_alice_test.conf"), "ServerName api.alice.test\n")
	writeTestFile(t, filepath.Join(proxyRoot, "alice-shop-api_shop_test.conf"), "ServerName api.shop.test\n")
	app := &App{Config: Config{WebRoot: webRoot, VHostRoot: vhostRoot, ProxyRoot: proxyRoot, AppRoot: filepath.Join(root, "apps")}}
	overviews, err := app.buildSiteOverviewSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, overview := range overviews {
		var routes, proxies []string
		for _, route := range overview.Routes {
			routes = append(routes, route.Domain)
		}
		for _, proxy := range overview.Proxies {
			proxies = append(proxies, proxy.Domain)
		}
		got[overview.Site] = fmt.Sprint(routes, proxies)
	}
	want := map[string]string{
		"alice":      "[alice.test] [api.alice.test]",
		"alice-shop": "[shop.test] [api.shop.test]",
		"bob":        "[bob.test] []",
	}
	for site, expected := range want {
		if got[site] != expected {
			t.Errorf("%s routes/proxies = %s, want %s", site, got[site], expected)
		}
	}
	// The per-site wrappers used outside the overview agree with it.
	for site := range want {
		routes, err := siteRoutesForWithError(vhostRoot, site)
		if err != nil {
			t.Fatal(err)
		}
		proxies, err := siteProxiesForWithError(proxyRoot, site)
		if err != nil {
			t.Fatal(err)
		}
		if len(routes) != 1 || (site != "bob") != (len(proxies) == 1) {
			t.Errorf("%s per-site lookup = %v %v", site, routes, proxies)
		}
	}
}
