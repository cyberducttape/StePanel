package session

import (
	"path/filepath"
	"testing"
	"time"
)

// Account-wide revocations advance the account's generation; a single
// revoke (logout) does not. Both registry backends behave the same.
func TestRevocationGenerationsAndSessionHashes(t *testing.T) {
	file, err := Open(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(openTestSessionDB(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, registry := range map[string]*Registry{"file": file, "database": db} {
		t.Run(name, func(t *testing.T) {
			expiry := time.Now().Add(time.Hour).Unix()
			for _, id := range []string{"laptop", "phone", "tablet"} {
				if err := registry.Add(id, "alice", expiry); err != nil {
					t.Fatal(err)
				}
			}
			if err := registry.Add("other", "bob", expiry); err != nil {
				t.Fatal(err)
			}
			generation := func() int64 {
				t.Helper()
				g, err := registry.Generation("alice")
				if err != nil {
					t.Fatal(err)
				}
				return g
			}
			alive := func(id string) bool {
				t.Helper()
				ok, err := registry.ActiveSessionHash("alice", HashSessionID(id))
				if err != nil {
					t.Fatal(err)
				}
				return ok
			}
			if generation() != 0 || !alive("laptop") || alive("other") || alive("missing") {
				t.Fatal("unexpected initial state")
			}
			if ok, err := registry.ActiveSessionHash("alice", ""); err != nil || ok {
				t.Fatalf("empty hash = %v, %v", ok, err)
			}
			// Logout of one session: no generation change.
			if err := registry.Revoke("tablet"); err != nil {
				t.Fatal(err)
			}
			if generation() != 0 || alive("tablet") {
				t.Fatal("a single logout advanced the generation or left the session alive")
			}
			// "Log out other devices": generation advances, kept session lives.
			if err := registry.RevokeUserExcept("alice", "laptop"); err != nil {
				t.Fatal(err)
			}
			if generation() != 1 || !alive("laptop") || alive("phone") {
				t.Fatalf("after revoking other devices: generation %d laptop %v phone %v", generation(), alive("laptop"), alive("phone"))
			}
			// Full revocation: generation advances, nothing survives.
			if err := registry.RevokeUser("alice"); err != nil {
				t.Fatal(err)
			}
			if generation() != 2 || alive("laptop") {
				t.Fatal("full revocation did not advance the generation or end every session")
			}
			if g, err := registry.Generation("bob"); err != nil || g != 0 {
				t.Fatalf("another account's generation = %d, %v", g, err)
			}
		})
	}
	if HashSessionID("") != "" || HashSessionID("a") == HashSessionID("b") || HashSessionID("a") == "a" {
		t.Fatal("session hashes are not distinct, non-empty digests")
	}
}
