package rootbroker

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/operations"
	"github.com/cyberducttape/StePanel/internal/sitelayout"
)

func grantLease(t *testing.T, db *sql.DB, key string) operations.FencingToken {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO resource_locks VALUES (?, 'owner-a', ?, 1, ?)`, key, time.Now().Add(time.Minute).UnixNano(), time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	return operations.FencingToken{ResourceKey: key, OwnerID: "owner-a", Generation: 1}
}

// F-01 regression: a live lease on one site must not fence a mutation of
// another. The helper must never start.
func TestBrokerRejectsLeaseForADifferentSite(t *testing.T) {
	broker, db, completed := fencedWordPressBroker(t, "0")
	if _, err := db.Exec(`INSERT INTO site_identities VALUES ('other', 'sp-other-0123abcd', 0)`); err != nil {
		t.Fatal(err)
	}
	otherLease := grantLease(t, db, "other")
	response, err := broker.Execute(context.Background(), &Request{RequestType: "wordpress", Fencing: []operations.FencingToken{otherLease}, WordPress: &WordPressRequest{Action: "core-update", Site: "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || !strings.Contains(response.Error, "does not cover site demo") {
		t.Fatalf("response = %#v, want a scope refusal", response)
	}
	if _, err := os.Stat(completed); err == nil {
		t.Fatal("the helper ran under another site's lease")
	}

	// The same request under demo's own lease is admitted.
	response, err = broker.Execute(context.Background(), &Request{RequestType: "wordpress", Fencing: demoLease, WordPress: &WordPressRequest{Action: "core-update", Site: "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("own-site lease refused: %#v", response)
	}
}

func TestBrokerRejectsConflictingTokensForOneResource(t *testing.T) {
	broker, _, _ := fencedWordPressBroker(t, "0")
	conflicting := append(append([]operations.FencingToken(nil), demoLease...), operations.FencingToken{ResourceKey: "demo", OwnerID: "owner-b", Generation: 1})
	err := broker.checkFencingScope(&Request{RequestType: "wordpress", Fencing: conflicting, WordPress: &WordPressRequest{Action: "core-update", Site: "demo"}})
	if err == nil || !strings.Contains(err.Error(), "conflicting fencing tokens") {
		t.Fatalf("conflicting tokens = %v", err)
	}
}

func TestFencingScopeFollowsThePanelLockNames(t *testing.T) {
	broker, db, _ := fencedWordPressBroker(t, "0")
	for _, placement := range []sitelayout.Placement{
		{Site: "alice", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindMain, Directory: sitelayout.MainDirectory},
		{Site: "shop-example-org", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindAddon, Directory: "shop.example.org"},
		{Site: "bob", Account: "bob", UnixUser: "bob", Kind: sitelayout.KindMain, Directory: sitelayout.MainDirectory},
	} {
		if _, err := sitelayout.AllocateAccount(db, placement.Account); err != nil {
			t.Fatal(err)
		}
		if err := sitelayout.Place(db, placement); err != nil {
			t.Fatal(err)
		}
	}
	registry := t.TempDir()
	broker.databaseRegistryRoot = registry
	if err := os.WriteFile(filepath.Join(registry, "shop_db"), []byte("panel:shop-example-org:owner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registry, "pending_db"), []byte("pending-cpmove:bob:x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token := func(key string) []operations.FencingToken {
		return []operations.FencingToken{{ResourceKey: key, OwnerID: "owner-a", Generation: 1}}
	}
	cases := []struct {
		name  string
		lease string
		req   Request
		admit bool
	}{
		{"site lease", "shop-example-org", Request{RequestType: "site", Site: &SiteRequest{Action: "seal", Site: "shop-example-org"}}, true},
		{"account lease covers its sites", "account:alice", Request{RequestType: "site", Site: &SiteRequest{Action: "seal", Site: "shop-example-org"}}, true},
		{"main-site lease covers account sites", "alice", Request{RequestType: "files", Files: &FilesRequest{Action: "write", Site: "shop-example-org"}}, true},
		{"other account's lease", "account:bob", Request{RequestType: "site", Site: &SiteRequest{Action: "seal", Site: "shop-example-org"}}, false},
		{"other site's lease", "bob", Request{RequestType: "app", App: &AppRequest{Action: "restart", Site: "alice"}}, false},
		{"database lease", "database:other_db", Request{RequestType: "db", DB: &DBRequest{Action: "drop", Database: "other_db"}}, true},
		{"owning site of a registered database", "shop-example-org", Request{RequestType: "db", DB: &DBRequest{Action: "drop-managed", Database: "shop_db", Username: "u"}}, true},
		{"pending registry record", "bob", Request{RequestType: "db", DB: &DBRequest{Action: "drop-managed", Database: "pending_db", Username: "u"}}, true},
		{"unrelated site for a registered database", "bob", Request{RequestType: "db", DB: &DBRequest{Action: "drop-managed", Database: "shop_db", Username: "u"}}, false},
		{"session lease", "database:session:42", Request{RequestType: "db", DB: &DBRequest{Action: "terminate", SessionID: "42"}}, true},
		{"reconcile lease", "database:reconcile", Request{RequestType: "db", DB: &DBRequest{Action: "reconcile"}}, true},
		{"reconcile under a site lease", "alice", Request{RequestType: "db", DB: &DBRequest{Action: "reconcile"}}, false},
		{"vhost delete by its site", "shop-example-org", Request{RequestType: "vhost", Vhost: &VhostRequest{Action: "delete", Name: "site-shop-example-org-shop_example_org.caddy"}}, true},
		{"vhost delete by another site", "bob", Request{RequestType: "vhost", Vhost: &VhostRequest{Action: "delete", Name: "site-shop-example-org-shop_example_org.caddy"}}, false},
		{"proxy delete by its site", "alice", Request{RequestType: "proxy", Proxy: &ProxyRequest{Action: "delete", Name: "alice-app_example_org.conf"}}, true},
		{"proxy lease", "proxy:alice-app_example_org.conf", Request{RequestType: "proxy", Proxy: &ProxyRequest{Action: "delete", Name: "alice-app_example_org.conf"}}, true},
		{"proxy delete by another site", "bob", Request{RequestType: "proxy", Proxy: &ProxyRequest{Action: "delete", Name: "alice-app_example_org.conf"}}, false},
		{"account lease", "account:bob", Request{RequestType: "account", Account: &AccountRequest{Action: "quota", Account: "bob"}}, true},
		{"site placed in the account", "shop-example-org", Request{RequestType: "account", Account: &AccountRequest{Action: "unplace", Account: "alice", Site: "shop-example-org"}}, true},
		{"site placed in another account", "shop-example-org", Request{RequestType: "account", Account: &AccountRequest{Action: "unplace", Account: "bob", Site: "shop-example-org"}}, false},
		{"unplaced new site ensures its account", "newsite", Request{RequestType: "account", Account: &AccountRequest{Action: "ensure", Account: "carol", Site: "newsite"}}, true},
		{"unplaced site cannot delete an account", "newsite", Request{RequestType: "account", Account: &AccountRequest{Action: "delete", Account: "carol", Site: "newsite"}}, false},
		{"account resources", "account:alice", Request{RequestType: "resource", Resource: &ResourceRequest{Action: "apply-account", Account: "alice"}}, true},
		// Applying a placed site's resource profile, under that site's lease,
		// reaches its account's envelope and quota (installed-host smoke).
		{"account quota for a site placed in it", "shop-example-org", Request{RequestType: "account", Account: &AccountRequest{Action: "quota", Account: "alice", Site: "shop-example-org", DiskMB: 64, Inodes: 1000}}, true},
		{"account quota for a site placed elsewhere", "bob", Request{RequestType: "account", Account: &AccountRequest{Action: "quota", Account: "alice", Site: "bob", DiskMB: 64, Inodes: 1000}}, false},
		{"account quota under a site lease naming no site", "shop-example-org", Request{RequestType: "account", Account: &AccountRequest{Action: "quota", Account: "alice", DiskMB: 64, Inodes: 1000}}, false},
		{"account resources for a site placed in it", "shop-example-org", Request{RequestType: "resource", Resource: &ResourceRequest{Action: "apply-account", Account: "alice", Site: "shop-example-org"}}, true},
		{"account resources for a site placed elsewhere", "bob", Request{RequestType: "resource", Resource: &ResourceRequest{Action: "apply-account", Account: "alice", Site: "bob"}}, false},
		{"unplaced site cannot apply account resources", "newsite", Request{RequestType: "resource", Resource: &ResourceRequest{Action: "apply-account", Account: "carol", Site: "newsite"}}, false},
		{"site resources under its account's lease", "account:alice", Request{RequestType: "resource", Resource: &ResourceRequest{Action: "apply-site", Site: "shop-example-org", Account: "alice"}}, true},
		{"site resources under a claimed account's lease", "account:bob", Request{RequestType: "resource", Resource: &ResourceRequest{Action: "apply-site", Site: "shop-example-org", Account: "bob"}}, false},
		{"certificate lease", "certificate:shop.example.org", Request{RequestType: "certificate", Certificate: &CertificateRequest{Action: "issue", Domain: "Shop.Example.org"}}, true},
		{"certificate under a site lease", "alice", Request{RequestType: "certificate", Certificate: &CertificateRequest{Action: "issue", Domain: "shop.example.org"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			req.Fencing = token(tc.lease)
			err := broker.checkFencingScope(&req)
			if tc.admit && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tc.admit && err == nil {
				t.Fatal("admitted a lease that does not cover the target")
			}
		})
	}
}

func TestRegistryRecordSiteMirrorsDbctl(t *testing.T) {
	for record, want := range map[string]string{
		"panel:shop:owner\n":        "shop",
		"wordpress:blog\n":          "blog",
		"pending-cpmove:alice:x":    "alice",
		"stepanel_restore:whatever": "",
		"panel:../etc:x":            "",
		"":                          "",
		"panel:UPPER:x":             "",
		"panel:a-very-long-site-name-that-exceeds-32:x": "",
	} {
		if got := registryRecordSite(record); got != want {
			t.Errorf("registryRecordSite(%q) = %q, want %q", record, got, want)
		}
	}
}
