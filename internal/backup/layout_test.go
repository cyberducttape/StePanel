package backup

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPathResolvesTheNamedSitesDirectory(t *testing.T) {
	root := t.TempDir()
	path, err := Path(root, "20260101-000000.000000000-my-shop")
	if err != nil || path != filepath.Join(root, "sites", "my-shop", "20260101-000000.000000000-my-shop") {
		t.Fatalf("Path = %q, %v", path, err)
	}
	for _, name := range []string{"", "renamed-new", "../20260101-000000.000000000-demo", "20260101-000000.000000000-demo/../x", ".pruning-20260101-000000.000000000-demo"} {
		if _, err := Path(root, name); err == nil {
			t.Errorf("Path accepted %q", name)
		}
	}
}

func TestNameSiteParsesOnlyPublishedNames(t *testing.T) {
	for name, want := range map[string]string{
		"20260101-000000.000000000-demo":                                    "demo",
		"20260101-000000.000000000-my-shop":                                 "my-shop",
		"renamed-new":                                                       "",
		"20260101-000000.000000000-":                                        "",
		"2026010x-000000.000000000-demo":                                    "",
		"20260101-000000.000000000-Bad/One":                                 "",
		"20260101-000000.000000000- demo":                                   "",
		"20260101-000000.000000000-" + "a234567890123456789012345678901234": "",
	} {
		if got := NameSite(name); got != want {
			t.Errorf("NameSite(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestValidNameRejectsPathTraversal(t *testing.T) {
	for _, name := range []string{"20260906-120000.000000000-account", "backup_v2-01"} {
		if !ValidName(name) {
			t.Errorf("valid backup name %q rejected", name)
		}
	}
	for _, name := range []string{"", "../account", "/account", "account/other", "account name", "..", "."} {
		if ValidName(name) {
			t.Errorf("unsafe backup name %q accepted", name)
		}
	}
}

func TestSiteDirectoryRejectsInvalidSites(t *testing.T) {
	root := t.TempDir()
	if got, err := SiteDirectory(root, "shop"); err != nil || got != filepath.Join(root, "sites", "shop") {
		t.Fatalf("SiteDirectory = %q, %v", got, err)
	}
	for _, site := range []string{"", "..", "Shop", "a/b", " shop"} {
		if _, err := SiteDirectory(root, site); err == nil {
			t.Errorf("SiteDirectory accepted %q", site)
		}
	}
}

func TestSiteNamesListsOnlySiteDirectories(t *testing.T) {
	root := t.TempDir()
	if sites, err := SiteNames(root); err != nil || len(sites) != 0 {
		t.Fatalf("SiteNames without a sites root = %v, %v", sites, err)
	}
	for _, name := range []string{"shop", "blog", "Not-A-Site"} {
		if err := os.MkdirAll(filepath.Join(SitesRoot(root), name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(SitesRoot(root), "notes"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	sites, err := SiteNames(root)
	slices.Sort(sites)
	if err != nil || !slices.Equal(sites, []string{"blog", "shop"}) {
		t.Fatalf("SiteNames = %v, %v", sites, err)
	}
}
