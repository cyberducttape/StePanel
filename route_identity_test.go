package stepanel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRoute(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The panel reads the served domain from each format the helpers write.
func TestManagedRouteServedDomainReadsEveryHelperFormat(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"apache.conf":       "<VirtualHost *:80>\n    ServerName Shop-Example.org\n    DocumentRoot /var/www/x\n</VirtualHost>\n",
		"caddy-site.caddy":  "# Managed by StePanel. Manual changes will be replaced.\nshop-example.org {\n\troot * /var/www/.stepanel-caddy-views/alice/public\n}\n",
		"caddy-proxy.caddy": "shop-example.org {\n\treverse_proxy 127.0.0.1:3000\n}\n",
		"ols-proxy.conf":    "# StePanel managed OpenLiteSpeed proxy for shop-example.org\nRewriteRule ^(.*)$ http://127.0.0.1:3000/$1 [P,L]\n",
		"ols-vhost.conf":    "# StePanel managed OpenLiteSpeed vhost for shop-example.org\nvirtualhost alice {\n}\n",
	} {
		writeRoute(t, dir, name, content)
		got, err := managedRouteServedDomain(filepath.Join(dir, name))
		if err != nil || got != "shop-example.org" {
			t.Errorf("%s served domain = %q, %v", name, got, err)
		}
	}
	for name, content := range map[string]string{
		"none.conf":    "# nothing here\n",
		"invalid.conf": "ServerName ../etc\n",
	} {
		writeRoute(t, dir, name, content)
		if got, err := managedRouteServedDomain(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s accepted as serving %q", name, got)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "apache.conf"), filepath.Join(dir, "link.conf")); err != nil {
		t.Fatal(err)
	}
	if _, err := managedRouteServedDomain(filepath.Join(dir, "link.conf")); err == nil {
		t.Error("a symlinked route was read")
	}
}

// NEW-02: "site-alice-" and "alice-" prefix-match alice-shop's routes and
// proxies. Terminating or listing alice must find only alice's own.
func TestSiteRouteDiscoveryDoesNotClaimANestedSitesRoutes(t *testing.T) {
	vhosts, proxies := t.TempDir(), t.TempDir()
	// alice serves shop-example.org; alice-shop serves example.org and
	// app.example.org. The first two share a name shape but not a domain.
	writeRoute(t, vhosts, "site-alice-shop-example_org.conf", "ServerName shop-example.org\n")
	writeRoute(t, vhosts, "site-alice-shop-app_example_org.conf", "ServerName app.example.org\n")
	writeRoute(t, proxies, "alice-shop-app_example_org.caddy", "app.example.org {\n\treverse_proxy 127.0.0.1:3000\n}\n")
	writeRoute(t, proxies, "alice-www_alice_org.caddy", "www.alice.org {\n\treverse_proxy 127.0.0.1:3001\n}\n")

	aliceRoutes, err := siteRoutesForWithError(vhosts, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceRoutes) != 1 || aliceRoutes[0].Domain != "shop-example.org" {
		t.Fatalf("alice's routes = %+v", aliceRoutes)
	}
	shopRoutes, err := siteRoutesForWithError(vhosts, "alice-shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(shopRoutes) != 1 || shopRoutes[0].Domain != "app.example.org" {
		t.Fatalf("alice-shop's routes = %+v", shopRoutes)
	}
	aliceProxies, err := siteProxiesForWithError(proxies, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceProxies) != 1 || aliceProxies[0].Domain != "www.alice.org" {
		t.Fatalf("alice's proxies = %+v", aliceProxies)
	}
	shopProxies, err := siteProxiesForWithError(proxies, "alice-shop")
	if err != nil || len(shopProxies) != 1 || shopProxies[0].Domain != "app.example.org" {
		t.Fatalf("alice-shop's proxies = %+v, %v", shopProxies, err)
	}
}

func TestTrackedRouteMustBeTheSitesRouteForItsDomain(t *testing.T) {
	for _, tc := range []struct {
		site, name, domain string
		ok                 bool
	}{
		{"alice", "site-alice-shop-example_org.conf", "shop-example.org", true},
		{"alice", "site-alice-shop-example_org.conf", "example.org", false},
		{"alice-shop", "site-alice-shop-example_org.caddy", "example.org", true},
		{"alice", "site-alice-shop-example_org.conf", "", false},
	} {
		if got := trackedRouteServes(tc.site, tc.name, tc.domain); got != tc.ok {
			t.Errorf("trackedRouteServes(%q, %q, %q) = %v", tc.site, tc.name, tc.domain, got)
		}
	}
}

// The helpers, not the caller, hold the authoritative check: each refuses to
// delete a file serving another domain and to overwrite one on apply.
func TestRouteHelpersRefuseFilesServingAnotherDomain(t *testing.T) {
	for _, helper := range []string{"stepanel-vhostctl", "stepanel-proxyctl", "stepanel-caddy-vhostctl", "stepanel-caddy-proxyctl", "stepanel-ols-vhostctl", "stepanel-ols-proxyctl"} {
		data, err := os.ReadFile(filepath.Join("deploy", "integrations", helper))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "delete NAME [DOMAIN]") || !strings.Contains(text, "serves another domain; refusing to delete it") && !strings.Contains(text, "serves another domain, or names none; refusing to delete it") {
			t.Errorf("%s does not verify the expected domain on delete", helper)
		}
		if !strings.Contains(text, "file already serves another domain") {
			t.Errorf("%s may overwrite a route file serving another domain", helper)
		}
	}
}
