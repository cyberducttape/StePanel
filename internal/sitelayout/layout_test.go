package sitelayout

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openLayoutDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "layout.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE site_identities (site TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestValidUnixUserRejectsSystemAndLegacyNames(t *testing.T) {
	for _, name := range []string{"alice", "bob_2", "_svc", "a"} {
		if err := ValidUnixUser(name); err != nil {
			t.Errorf("ValidUnixUser(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "root", "www-data", "mysql", "stepanel", "stepanel-ops", "sp-blog-1a2b3c4d", "systemd-network", "1alice", "-alice", "Alice", "al.ice", strings.Repeat("a", 33)} {
		if err := ValidUnixUser(name); err == nil {
			t.Errorf("ValidUnixUser(%q) accepted a name that must be refused", name)
		}
	}
}

func TestPlacementPathsFollowTheCPanelLayout(t *testing.T) {
	main := Placement{Site: "alice", Account: "alice", UnixUser: "alice", Kind: KindMain, Directory: MainDirectory}
	addon := Placement{Site: "shop", Account: "alice", UnixUser: "alice", Kind: KindAddon, Directory: "shop.example.org"}
	for _, p := range []Placement{main, addon} {
		if err := p.Validate(); err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
	}
	if got := main.DocRoot("/home"); got != "/home/alice/public_html" {
		t.Fatalf("main docroot = %s", got)
	}
	if got := addon.DocRoot("/home"); got != "/home/alice/shop.example.org" {
		t.Fatalf("addon docroot = %s", got)
	}
	if got := StagingRoot("/home"); got != "/home/.stepanel-manager-staging" {
		t.Fatalf("staging root = %s", got)
	}
}

func TestPlacementRejectsDirectoriesOutsideItsRole(t *testing.T) {
	base := Placement{Site: "shop", Account: "alice", UnixUser: "alice"}
	for _, tc := range []struct {
		kind      Kind
		directory string
	}{
		{KindMain, "shop.example.org"},
		{KindAddon, MainDirectory},
		{KindAddon, "../etc"},
		{KindAddon, ".ssh"},
		{KindAddon, "etc"},
		{KindSubdomain, "Shop.Example.org"},
		{KindSubdomain, "192.168.1.1"},
		{KindSubdomain, "shop.example.org/../x"},
		{"parked", "shop.example.org"},
	} {
		p := base
		p.Kind, p.Directory = tc.kind, tc.directory
		if err := p.Validate(); err == nil {
			t.Errorf("placement %s %q was accepted", tc.kind, tc.directory)
		}
	}
}

func TestAllocateAccountNeverReusesAUnixUser(t *testing.T) {
	db := openLayoutDB(t)
	user, err := AllocateAccount(db, "alice")
	if err != nil || user != "alice" {
		t.Fatalf("allocate = %q, %v", user, err)
	}
	if again, err := AllocateAccount(db, "alice"); err != nil || again != "alice" {
		t.Fatalf("idempotent allocate = %q, %v", again, err)
	}
	if _, err := AllocateAccount(db, "root"); err == nil {
		t.Fatal("allocated a reserved system account")
	}
	if err := RetireAccount(db, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := AllocateAccount(db, "alice"); err == nil {
		t.Fatal("a retired account's Unix user was reused")
	}
	if _, ok, err := AccountUser(db, "alice"); err != nil || ok {
		t.Fatalf("retired account user lookup = %v, %v", ok, err)
	}
}

func TestPlaceEnforcesAccountOwnershipAndUniqueRoots(t *testing.T) {
	db := openLayoutDB(t)
	for _, account := range []string{"alice", "bob"} {
		if _, err := AllocateAccount(db, account); err != nil {
			t.Fatal(err)
		}
	}
	main := Placement{Site: "alice", Account: "alice", UnixUser: "alice", Kind: KindMain, Directory: MainDirectory}
	if err := Place(db, main); err != nil {
		t.Fatal(err)
	}
	cases := map[string]Placement{
		"second main site":    {Site: "alice2", Account: "alice", UnixUser: "alice", Kind: KindMain, Directory: MainDirectory},
		"same directory":      {Site: "shop", Account: "alice", UnixUser: "alice", Kind: KindMain, Directory: MainDirectory},
		"another user's home": {Site: "shop", Account: "alice", UnixUser: "bob", Kind: KindAddon, Directory: "shop.example.org"},
		"unallocated account": {Site: "shop", Account: "carol", UnixUser: "carol", Kind: KindMain, Directory: MainDirectory},
		"already placed site": {Site: "alice", Account: "bob", UnixUser: "bob", Kind: KindMain, Directory: MainDirectory},
	}
	for name, p := range cases {
		if err := Place(db, p); err == nil {
			t.Errorf("%s: placement accepted", name)
		}
	}
	if _, err := db.Exec(`INSERT INTO site_identities(site, username, created_at) VALUES ('legacy', 'sp-legacy-1a2b3c4d', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := Place(db, Placement{Site: "legacy", Account: "bob", UnixUser: "bob", Kind: KindMain, Directory: MainDirectory}); err == nil {
		t.Fatal("a legacy per-site site was also placed in an account")
	}
	addon := Placement{Site: "shop", Account: "alice", UnixUser: "alice", Kind: KindAddon, Directory: "shop.example.org"}
	if err := Place(db, addon); err != nil {
		t.Fatal(err)
	}
	placements, err := ForAccount(db, "alice")
	if err != nil || len(placements) != 2 || placements[0].Kind != KindMain || placements[1] != addon {
		t.Fatalf("account placements = %+v, %v", placements, err)
	}
	if err := RetireAccount(db, "alice"); err == nil {
		t.Fatal("retired an account that still has sites")
	}
	got, err := Get(db, "shop")
	if err != nil || got != addon {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if err := Remove(db, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(db, "shop"); !errors.Is(err, ErrNotPlaced) {
		t.Fatalf("removed placement lookup = %v", err)
	}
}

func TestGetRejectsTamperedPlacements(t *testing.T) {
	db := openLayoutDB(t)
	if _, err := db.Exec(`INSERT INTO site_placements(site, account, unix_user, kind, directory, created_at) VALUES ('evil', 'root', 'root', 'addon', '../../etc', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(db, "evil"); err == nil || errors.Is(err, ErrNotPlaced) {
		t.Fatalf("tampered placement returned %v", err)
	}
}
