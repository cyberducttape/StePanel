package siteidentity

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func testIdentityDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "identity.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE site_identities (site TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestResolveOrAllocateIsStableAndUnique(t *testing.T) {
	db := testIdentityDB(t)
	first, err := ResolveOrAllocate(db, "example")
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := ResolveOrAllocate(db, "example")
	if err != nil || repeat != first {
		t.Fatalf("repeat allocation = %q, %v; want %q", repeat, err, first)
	}
	second, err := ResolveOrAllocate(db, "another")
	if err != nil || second == first {
		t.Fatalf("second allocation = %q, %v; identities must be distinct", second, err)
	}
	if len(first) > 32 || !validPersistedUser(first) || !validPersistedUser(second) {
		t.Fatalf("invalid allocated accounts: %q, %q", first, second)
	}
}

func TestMigrateExistingRejectsLegacyCollision(t *testing.T) {
	db := testIdentityDB(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sites"), 0o750); err != nil {
		t.Fatal(err)
	}
	// These are two independent collisions for the legacy 32-bit derivation.
	sites := []string{
		"shared-site-prefix00018346", "shared-site-prefix0001d455",
		"other-site-prefixx0217f8", "other-site-prefixx02264b",
	}
	for _, site := range sites {
		if err := os.Mkdir(filepath.Join(root, "sites", site), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	err := MigrateExisting(db, root)
	if err == nil {
		t.Fatal("legacy identity collision was accepted")
	}
	for _, detail := range []string{
		"offline recovery is required",
		UnixUser(sites[0]), sites[0], sites[1],
		UnixUser(sites[2]), sites[2], sites[3],
	} {
		if !strings.Contains(err.Error(), detail) {
			t.Errorf("collision diagnostic %q does not include %q", err, detail)
		}
	}
	if strings.Index(err.Error(), UnixUser(sites[2])) > strings.Index(err.Error(), UnixUser(sites[0])) {
		t.Fatalf("collision diagnostic is not deterministic: %v", err)
	}
	var mapped int
	if err := db.QueryRow(`SELECT COUNT(*) FROM site_identities`).Scan(&mapped); err != nil {
		t.Fatal(err)
	}
	if mapped != 0 {
		t.Fatalf("collision-aborted migration left %d partial identity mappings", mapped)
	}
}

func TestMigrateExistingAcceptsFullyReassignedLegacyCollision(t *testing.T) {
	db := testIdentityDB(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sites"), 0o750); err != nil {
		t.Fatal(err)
	}
	sites := []string{"shared-site-prefix00018346", "shared-site-prefix0001d455"}
	for _, site := range sites {
		if err := os.Mkdir(filepath.Join(root, "sites", site), 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO site_identities(site, username, created_at) VALUES (?, ?, 1)`, site, allocatedUser(site, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := MigrateExisting(db, root); err != nil {
		t.Fatalf("completed unique reassignment did not permit startup migration: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(DISTINCT username) FROM site_identities WHERE site IN (?, ?)`, sites[0], sites[1]).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("reassigned identities have %d unique users, want 2", count)
	}
}

func TestMigrateExistingRejectsPartiallyReassignedLegacyCollision(t *testing.T) {
	db := testIdentityDB(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sites"), 0o750); err != nil {
		t.Fatal(err)
	}
	sites := []string{"shared-site-prefix00018346", "shared-site-prefix0001d455"}
	for _, site := range sites {
		if err := os.Mkdir(filepath.Join(root, "sites", site), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO site_identities(site, username, created_at) VALUES (?, ?, 1)`, sites[0], allocatedUser(sites[0], 0)); err != nil {
		t.Fatal(err)
	}
	if err := MigrateExisting(db, root); err == nil || !strings.Contains(err.Error(), "offline recovery is required") {
		t.Fatalf("partial collision reassignment error = %v, want fail-closed recovery diagnostic", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM site_identities`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed partial migration changed identity row count to %d", count)
	}
}

func TestLegacyCollisionErrorCollectsPersistedOwnerConflicts(t *testing.T) {
	db := testIdentityDB(t)
	owner := "deleted-site"
	newSite := "unrelated"
	if _, err := db.Exec(`INSERT INTO site_identities(site, username, created_at) VALUES (?, ?, 1)`, owner, UnixUser(newSite)); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sites"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sites", newSite), 0o750); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "sites"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := loadIdentityIndex(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	err = legacyCollisionError(liveSites(entries), index)
	want := fmt.Sprintf("%s shared by %q, %q", UnixUser(newSite), owner, newSite)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("persisted collision error = %v, want to include %q", err, want)
	}
}

func TestConcurrentMigrateExistingIsSerializedAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)"
	db1, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db1.Close()
	if _, err := db1.Exec(`CREATE TABLE site_identities (site TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	db2, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sites"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "sites", "example"), 0o750); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, db := range []*sql.DB{db1, db2} {
		wg.Add(1)
		go func(db *sql.DB) {
			defer wg.Done()
			<-start
			errs <- MigrateExisting(db, root)
		}(db)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent identity migration: %v", err)
		}
	}
	var count int
	if err := db1.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM site_identities WHERE site='example'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("migrated identity count = %d, want 1", count)
	}
}

func TestResolveOrAllocateRefusesSitesPlacedInAnAccount(t *testing.T) {
	db := testIdentityDB(t)
	if _, err := db.Exec(`CREATE TABLE site_placements (site TEXT PRIMARY KEY, account TEXT NOT NULL, unix_user TEXT NOT NULL, kind TEXT NOT NULL, directory TEXT NOT NULL, created_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_placements VALUES ('shop', 'alice', 'alice', 'addon', 'shop.example.org', 1)`); err != nil {
		t.Fatal(err)
	}
	if user, err := ResolveOrAllocate(db, "shop"); err == nil {
		t.Fatalf("placed site was given per-site account %q", user)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM site_identities`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("site identities = %d, %v", count, err)
	}
}
