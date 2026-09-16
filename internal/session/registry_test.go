package session

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTestSessionDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE sessions (
		id TEXT PRIMARY KEY,
		username TEXT NOT NULL,
		expiry INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestRegistryDBBackedIncrementalPersistence exercises the SQLite-backed
// registry through Add/Revoke/RevokeUser and reopens it from the same
// database, verifying the incremental persistAddLocked/persistDeleteLocked/
// persistDeleteByUsernameLocked paths (rather than the old delete-everything
// -reinsert-everything rewrite) leave the table in the correct state.
func TestRegistryDBBackedIncrementalPersistence(t *testing.T) {
	db := openTestSessionDB(t)
	registry, err := OpenDB(db)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour).Unix()
	if err := registry.Add("one", "alice", expiry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add("two", "bob", expiry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add("three", "carol", expiry); err != nil {
		t.Fatal(err)
	}
	// Add on an existing id should update it (ON CONFLICT upsert), not
	// duplicate it or fail with a primary-key violation.
	laterExpiry := expiry + 60
	if err := registry.Add("one", "alice", laterExpiry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Revoke("two"); err != nil {
		t.Fatal(err)
	}
	if err := registry.RevokeUser("carol"); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one surviving row in the sessions table, got %d", count)
	}
	var storedExpiry int64
	if err := db.QueryRow(`SELECT expiry FROM sessions WHERE id = 'one'`).Scan(&storedExpiry); err != nil {
		t.Fatal(err)
	}
	if storedExpiry != laterExpiry {
		t.Fatalf("expected the updated expiry to be persisted, got %d want %d", storedExpiry, laterExpiry)
	}

	reopened, err := OpenDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Valid("one", "alice", laterExpiry) {
		t.Fatal("surviving session was not reloaded correctly")
	}
	if reopened.Valid("two", "bob", expiry) || reopened.Valid("three", "carol", expiry) {
		t.Fatal("revoked sessions were reloaded as valid")
	}
}

func TestRegistryPersistsAndRevokesByUser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.json")
	registry, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour).Unix()
	if err := registry.Add("one", "alice", expiry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add("two", "bob", expiry); err != nil {
		t.Fatal(err)
	}
	if !registry.Valid("one", "alice", expiry) || registry.Valid("one", "bob", expiry) {
		t.Fatal("session validation did not enforce identity")
	}
	if err := registry.RevokeUser("alice"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Valid("one", "alice", expiry) || !reopened.Valid("two", "bob", expiry) {
		t.Fatal("user revocation was not persisted")
	}
}
