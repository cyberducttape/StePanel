package session

import (
	"database/sql"
	"encoding/json"
	"os"
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

// TestRevokeUserExceptKeepsOnlyTheGivenSession backs the "log out of all
// other devices" self-service action: it must revoke every other session
// for the user, leave the caller's own current session valid, and never
// touch another user's sessions.
func TestRevokeUserExceptKeepsOnlyTheGivenSession(t *testing.T) {
	db := openTestSessionDB(t)
	registry, err := OpenDB(db)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour).Unix()
	if err := registry.Add("alice-current", "alice", expiry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add("alice-other-device", "alice", expiry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add("bob-session", "bob", expiry); err != nil {
		t.Fatal(err)
	}
	if err := registry.RevokeUserExcept("alice", "alice-current"); err != nil {
		t.Fatal(err)
	}
	if !registry.Valid("alice-current", "alice", expiry) {
		t.Fatal("the caller's own current session was revoked")
	}
	if registry.Valid("alice-other-device", "alice", expiry) {
		t.Fatal("the other device's session survived")
	}
	if !registry.Valid("bob-session", "bob", expiry) {
		t.Fatal("an unrelated user's session was revoked")
	}

	reopened, err := OpenDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Valid("alice-current", "alice", expiry) || reopened.Valid("alice-other-device", "alice", expiry) || !reopened.Valid("bob-session", "bob", expiry) {
		t.Fatal("RevokeUserExcept was not durably persisted")
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

func TestOpenMigratesLegacySessionsAndPrunesExpiredEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	legacy := map[string]int64{
		"active":  time.Now().Add(time.Hour).Unix(),
		"expired": time.Now().Add(-time.Hour).Unix(),
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !registry.Valid("active", "", legacy["active"]) || registry.Valid("expired", "", legacy["expired"]) {
		t.Fatalf("migrated entries = %#v", registry.Entries)
	}
	var migrated map[string]Entry
	data, err = os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &migrated) != nil {
		t.Fatalf("migrated state unreadable: %v", err)
	}
	if migrated["active"].Username != "" {
		t.Fatalf("legacy username unexpectedly changed: %#v", migrated["active"])
	}
}

func TestOpenRejectsMalformedAndOversizedState(t *testing.T) {
	root := t.TempDir()
	malformed := filepath.Join(root, "bad.json")
	if err := os.WriteFile(malformed, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(malformed); err == nil {
		t.Fatal("malformed state was accepted")
	}
	oversized := filepath.Join(root, "large.json")
	if err := os.WriteFile(oversized, make([]byte, 1<<20+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(oversized); err == nil {
		t.Fatal("oversized state was accepted")
	}
}

func TestFilePersistenceFailureRollsBackMemory(t *testing.T) {
	path := t.TempDir()
	registry := New(path)
	err := registry.Add("session", "alice", time.Now().Add(time.Hour).Unix())
	if err == nil {
		t.Fatal("write to directory unexpectedly succeeded")
	}
	if _, ok := registry.Entries["session"]; ok {
		t.Fatal("failed add remained in memory")
	}
	if err := registry.Revoke("missing"); err == nil {
		t.Fatal("revoke unexpectedly succeeded with an unwritable state path")
	}
	if err := registry.RevokeUserExcept("alice", "missing"); err != nil {
		t.Fatalf("revoke user with no sessions = %v", err)
	}
	if registry.PersistenceError() != nil {
		t.Fatal("successful no-op cleared state with persistence error")
	}
}

func TestRegistryValidatesExpiryAndIdentity(t *testing.T) {
	registry := New("")
	expiry := time.Now().Add(time.Hour).Unix()
	if err := registry.Add("id", "alice", expiry); err != nil {
		t.Fatal(err)
	}
	if !registry.Valid("id", "alice", expiry) || registry.Valid("id", "bob", expiry) || registry.Valid("id", "alice", expiry+1) {
		t.Fatal("session identity or expiry validation failed")
	}
	if registry.Valid("id", "alice", time.Now().Add(-time.Hour).Unix()) {
		t.Fatal("expired session was accepted")
	}
	if registry.PersistenceError() != nil {
		t.Fatal("unexpected persistence error")
	}
}
