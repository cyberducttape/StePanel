package siteidentity

import (
	"database/sql"
	"os"
	"path/filepath"
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
	// These names are a known collision for the legacy 32-bit derivation.
	for _, site := range []string{"shared-site-prefix00018346", "shared-site-prefix0001d455"} {
		if err := os.Mkdir(filepath.Join(root, "sites", site), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := MigrateExisting(db, root); err == nil {
		t.Fatal("legacy identity collision was accepted")
	}
}
