package stepanel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

const sampleUserdataMain = `---
addon_domains:
  shop.example.org: shop.alice.example.com
  blog.example.net: blog.alice.example.com
main_domain: alice.example.com
parked_domains:
  - alias.example.com
sub_domains:
  - blog.alice.example.com
  - shop.alice.example.com
  - dev.alice.example.com
`

func TestParseCPMoveUserdataMain(t *testing.T) {
	main, err := parseCPMoveUserdataMain([]byte(sampleUserdataMain))
	if err != nil {
		t.Fatal(err)
	}
	if main.MainDomain != "alice.example.com" || len(main.Addons) != 2 || main.Addons["shop.example.org"] != "shop.alice.example.com" ||
		len(main.Subdomains) != 3 || len(main.Parked) != 1 || main.Parked[0] != "alias.example.com" {
		t.Fatalf("parsed = %+v", main)
	}
	if _, err := parseCPMoveUserdataMain([]byte("main_domain alice.example.com\n")); err == nil {
		t.Fatal("a malformed top-level line was accepted")
	}
}

// writeCPMoveTree lays out an extracted cPanel backup under root.
func writeCPMoveTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlanCPMoveDomainsMapsClassicAndModernRoots(t *testing.T) {
	root := t.TempDir()
	writeCPMoveTree(t, root, map[string]string{
		"userdata/main":                           sampleUserdataMain,
		"userdata/shop.alice.example.com":         "documentroot: /home/alice/public_html/shop\n",
		"userdata/blog.alice.example.com":         "documentroot: /home/alice/blog.example.net\n",
		"userdata/dev.alice.example.com":          "documentroot: /home2/alice/public_html/dev\n",
		"homedir/public_html/index.html":          "main",
		"homedir/public_html/shop/index.html":     "shop",
		"homedir/blog.example.net/index.html":     "blog",
		"homedir/public_html/dev/index.html":      "dev",
		"homedir/public_html/assets/site.css":     "css",
		"userdata/alice.example.com":              "documentroot: /home/alice/public_html\n",
		"userdata/unused.alice.example.com":       "documentroot: /etc\n",
		"homedir/public_html/shop/admin/edit.php": "<?php",
	})
	plan, err := planCPMoveDomains(root, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if plan.MainDomain != "alice.example.com" || len(plan.Parked) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	want := map[string]struct {
		kind   sitelayout.Kind
		source string
	}{
		"blog.example.net":      {sitelayout.KindAddon, filepath.Join(root, "homedir", "blog.example.net")},
		"shop.example.org":      {sitelayout.KindAddon, filepath.Join(root, "homedir", "public_html", "shop")},
		"dev.alice.example.com": {sitelayout.KindSubdomain, filepath.Join(root, "homedir", "public_html", "dev")},
	}
	if len(plan.Sites) != len(want) {
		t.Fatalf("planned sites = %+v", plan.Sites)
	}
	for _, site := range plan.Sites {
		expected, found := want[site.Domain]
		if !found || site.Kind != expected.kind || site.Source != expected.source {
			t.Errorf("planned %+v, want %+v", site, expected)
		}
	}
	// Classic nested roots are restored as their own sites, not twice.
	if !plan.MainExcludes["shop"] || !plan.MainExcludes["dev"] || plan.MainExcludes["assets"] {
		t.Fatalf("main excludes = %v", plan.MainExcludes)
	}
}

func TestPlanCPMoveDomainsRejectsRootsOutsideTheAccount(t *testing.T) {
	for name, docroot := range map[string]string{
		"another user": "documentroot: /home/bob/public_html\n",
		"system path":  "documentroot: /etc/shop\n",
		"traversal":    "documentroot: /home/alice/../bob/site\n",
	} {
		root := t.TempDir()
		writeCPMoveTree(t, root, map[string]string{
			"userdata/main":                   "main_domain: alice.example.com\naddon_domains:\n  shop.example.org: shop.alice.example.com\n",
			"userdata/shop.alice.example.com": docroot,
		})
		if _, err := planCPMoveDomains(root, "alice"); err == nil {
			t.Errorf("%s: a document root outside the account was accepted", name)
		}
	}
}

func TestSiteNameForDomainIsUniqueAndBounded(t *testing.T) {
	taken := map[string]bool{"shop-example-org": true}
	name, err := siteNameForDomain("shop.example.org", func(n string) bool { return taken[n] })
	if err != nil || name != "shop-example-org-2" {
		t.Fatalf("name = %q, %v", name, err)
	}
	long, err := siteNameForDomain("a-very-long-subdomain-name.example-company.org", func(string) bool { return false })
	if err != nil || len(long) > 32 || safeUser(long) != long {
		t.Fatalf("long name = %q, %v", long, err)
	}
}

func TestRestoreCPMoveIntoAHostingAccount(t *testing.T) {
	root := t.TempDir()
	db, err := openControlPlaneDB(filepath.Join(root, "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registerSitePlacementDB(db)
	t.Cleanup(func() { registerSitePlacementDB(nil) })
	archive := makeTarGz(t, map[string]string{
		"cpmove-alice/userdata/main":                   sampleUserdataMain,
		"cpmove-alice/userdata/shop.alice.example.com": "documentroot: /home/alice/public_html/shop\n",
		"cpmove-alice/userdata/blog.alice.example.com": "documentroot: /home/alice/blog.example.net\n",
		"cpmove-alice/userdata/dev.alice.example.com":  "documentroot: /home/alice/public_html/dev\n",
		"cpmove-alice/homedir/public_html/index.html":  "main",
		"cpmove-alice/homedir/public_html/shop/a.html": "shop",
		"cpmove-alice/homedir/blog.example.net/b.html": "blog",
		"cpmove-alice/homedir/public_html/dev/d.html":  "dev",
	})
	file, header := openMultipartArchive(t, archive, "cpmove-alice.tar.gz")
	defer file.Close()
	webRoot, homeRoot := filepath.Join(root, "www"), filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(webRoot, "sites"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ImportRoot: filepath.Join(root, "imports"), WebRoot: webRoot, HomeRoot: homeRoot, MailRoot: filepath.Join(root, "mail"),
		RecoveryRoot: filepath.Join(webRoot, "sites", ".stepanel-recovery"), MaxEntries: 1000}
	result, err := RestoreCPMove(cfg, file, header, AuthorizedSite{site: "alice"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Account != "alice" || result.MainDomain != "alice.example.com" || len(result.Domains) != 3 || len(result.DomainErrors) != 0 {
		t.Fatalf("result = %+v", result)
	}
	home := filepath.Join(homeRoot, "alice")
	for path, body := range map[string]string{
		"public_html/index.html":       "main",
		"shop.example.org/a.html":      "shop",
		"blog.example.net/b.html":      "blog",
		"dev.alice.example.com/d.html": "dev",
	} {
		data, err := os.ReadFile(filepath.Join(home, path))
		if err != nil || string(data) != body {
			t.Errorf("%s = %q, %v", path, data, err)
		}
	}
	for _, nested := range []string{"public_html/shop", "public_html/dev"} {
		if _, err := os.Stat(filepath.Join(home, nested)); !os.IsNotExist(err) {
			t.Errorf("%s was also kept inside the main site: %v", nested, err)
		}
	}
	placements, err := sitelayout.ForAccount(db, "alice")
	if err != nil || len(placements) != 4 || placements[0].Site != "alice" || placements[0].Kind != sitelayout.KindMain {
		t.Fatalf("account placements = %+v, %v", placements, err)
	}
	if _, err := os.Stat(filepath.Join(webRoot, "sites", "alice")); !os.IsNotExist(err) {
		t.Fatalf("the restore also wrote the legacy site tree: %v", err)
	}
}
