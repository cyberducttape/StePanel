package rootbroker

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cyberducttape/StePanel/internal/sitelayout"
	_ "modernc.org/sqlite"
)

// newAccountBroker returns a broker whose control plane knows account alice
// with main site "alice", and whose stepanel-userctl is a stub that records
// its arguments.
func newAccountBroker(t *testing.T) (*Broker, *sql.DB, string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "control.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(sitelayout.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE site_identities (site TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE tenant_sites (site TEXT PRIMARY KEY, username TEXT NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tenant_sites(site, username, updated_at) VALUES ('alice', 'alice', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sitelayout.AllocateAccount(db, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := sitelayout.Place(db, sitelayout.Placement{Site: "alice", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindMain, Directory: sitelayout.MainDirectory}); err != nil {
		t.Fatal(err)
	}
	broker, err := newTestBroker(t, t.TempDir(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	broker.fencingDB = db
	record := filepath.Join(t.TempDir(), "userctl.args")
	stub := filepath.Join(t.TempDir(), "stepanel-userctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + record + "\nif [ \"$1\" = place ]; then printf 'alice\\t/home/alice/public_html\\n'; fi\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	broker.userctl = stub
	return broker, db, record
}

func TestValidatorAccountRequestsAcceptOnlyTheirShape(t *testing.T) {
	v := NewValidator("/var/www")
	valid := []*AccountRequest{
		{Action: "ensure", Account: "alice"},
		{Action: "quota", Account: "alice", DiskMB: 1024, Inodes: 100000},
		{Action: "place", Account: "alice", Site: "shop", Kind: "addon", Directory: "shop.example.org"},
		{Action: "place", Account: "alice", Site: "alice", Kind: "main", Directory: "public_html"},
		{Action: "unplace", Account: "alice", Site: "shop"},
		{Action: "delete", Account: "alice"},
	}
	for _, req := range valid {
		if err := v.ValidateRequest(&Request{RequestType: "account", Account: req}); err != nil {
			t.Errorf("valid %s request rejected: %v", req.Action, err)
		}
	}
	invalid := []*AccountRequest{
		{Action: "ensure", Account: "root"},
		{Action: "ensure", Account: "sp-blog-1a2b3c4d"},
		{Action: "ensure", Account: "alice", Directory: "public_html"},
		{Action: "quota", Account: "alice", DiskMB: 1, Inodes: 100000},
		{Action: "place", Account: "alice", Site: "shop", Kind: "addon", Directory: "public_html"},
		{Action: "place", Account: "alice", Site: "shop", Kind: "addon", Directory: "../root"},
		{Action: "place", Account: "alice", Site: "shop", Kind: "main", Directory: "shop.example.org"},
		{Action: "unplace", Account: "alice", Site: "../x"},
		{Action: "chown", Account: "alice"},
	}
	for _, req := range invalid {
		if err := v.ValidateRequest(&Request{RequestType: "account", Account: req}); err == nil {
			t.Errorf("invalid account request accepted: %+v", req)
		}
	}
}

func TestBrokerAccountRequestsMatchTheControlPlane(t *testing.T) {
	broker, db, record := newAccountBroker(t)
	ctx := context.Background()
	run := func(req *AccountRequest) *Response {
		t.Helper()
		resp, err := broker.handleAccountRequest(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	if resp := run(&AccountRequest{Action: "ensure", Account: "alice"}); !resp.OK {
		t.Fatalf("ensure = %s", resp.Error)
	}
	resp := run(&AccountRequest{Action: "place", Account: "alice", Site: "alice", Kind: "main", Directory: "public_html"})
	if !resp.OK {
		t.Fatalf("place = %s", resp.Error)
	}
	var details AccountResponse
	if err := json.Unmarshal(resp.Details, &details); err != nil || details.DocRoot != "/home/alice/public_html" {
		t.Fatalf("place details = %+v, %v", details, err)
	}
	for name, req := range map[string]*AccountRequest{
		"unallocated account":    {Action: "ensure", Account: "mallory"},
		"placement mismatch":     {Action: "place", Account: "alice", Site: "alice", Kind: "addon", Directory: "evil.example.org"},
		"unrecorded placement":   {Action: "place", Account: "alice", Site: "shop", Kind: "addon", Directory: "shop.example.org"},
		"account with sites":     {Action: "delete", Account: "alice"},
		"another account's site": {Action: "unplace", Account: "bob", Site: "alice"},
	} {
		if name == "another account's site" {
			if _, err := sitelayout.AllocateAccount(db, "bob"); err != nil {
				t.Fatal(err)
			}
		}
		if resp := run(req); resp.OK {
			t.Errorf("%s: request was accepted", name)
		}
	}
	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); got != "account-ensure alice\nplace alice alice main public_html" {
		t.Fatalf("userctl invocations = %q; refused requests must never reach the helper", got)
	}
}

func TestBrokerResolvesPlacedSitesToTheAccountUser(t *testing.T) {
	broker, db, _ := newAccountBroker(t)
	user, err := broker.resolveSiteUser(&SiteRequest{Site: "alice"}, true)
	if err != nil || user != "alice" {
		t.Fatalf("placed site user = %q, %v", user, err)
	}
	if _, err := broker.resolveSiteUser(&SiteRequest{Site: "alice", SiteUser: "sp-alice-1a2b3c4d"}, true); err == nil {
		t.Fatal("a placed site accepted a different Unix user")
	}
	var identities int
	if err := db.QueryRow(`SELECT COUNT(*) FROM site_identities`).Scan(&identities); err != nil || identities != 0 {
		t.Fatalf("placed site allocated %d per-site identities, %v", identities, err)
	}
	legacy, err := broker.resolveSiteUser(&SiteRequest{Site: "legacy"}, true)
	if err != nil || !strings.HasPrefix(legacy, "sp-") {
		t.Fatalf("legacy site user = %q, %v", legacy, err)
	}
}

func TestValidatorUsesPlacedDocumentAndStageRoots(t *testing.T) {
	v := NewValidator("/var/www")
	v.placements = func(site string) (sitelayout.Placement, error) {
		if site == "shop" {
			return sitelayout.Placement{Site: "shop", Account: "alice", UnixUser: "alice", Kind: sitelayout.KindAddon, Directory: "shop.example.org"}, nil
		}
		return sitelayout.Placement{}, sitelayout.ErrNotPlaced
	}
	worker := func(site, root string) *Request {
		return &Request{RequestType: "worker", Worker: &WorkerRequest{Action: "apply", Site: site, Name: "queue", Type: "laravel", Root: root, Processes: 1, MemoryMB: 128}}
	}
	if err := v.ValidateRequest(worker("shop", "/home/alice/shop.example.org")); err != nil {
		t.Fatalf("placed document root rejected: %v", err)
	}
	if err := v.ValidateRequest(worker("shop", "/var/www/sites/shop/public")); err == nil {
		t.Fatal("a placed site accepted the legacy document root")
	}
	if err := v.ValidateRequest(worker("legacy", "/var/www/sites/legacy/public")); err != nil {
		t.Fatalf("legacy document root rejected: %v", err)
	}
	stage := func(site, destination string) *Request {
		return &Request{RequestType: "git", Git: &GitRequest{Action: "stage-limit", Site: site, Destination: destination, MaxStageBytes: 1 << 20}}
	}
	if err := v.ValidateRequest(stage("shop", "/home/.stepanel-manager-staging/.stepanel-release-shop-42")); err != nil {
		t.Fatalf("placed release stage rejected: %v", err)
	}
	for site, destination := range map[string]string{
		"shop":   "/var/www/sites/.stepanel-manager-staging/.stepanel-release-shop-42",
		"legacy": "/home/.stepanel-manager-staging/.stepanel-release-legacy-42",
	} {
		if err := v.ValidateRequest(stage(site, destination)); err == nil {
			t.Errorf("site %s accepted a stage on the wrong filesystem: %s", site, destination)
		}
	}
	v.placements = func(string) (sitelayout.Placement, error) { return sitelayout.Placement{}, os.ErrPermission }
	if err := v.ValidateRequest(worker("shop", "/var/www/sites/shop/public")); err == nil {
		t.Fatal("an unreadable placement fell back to the legacy layout")
	}
}
