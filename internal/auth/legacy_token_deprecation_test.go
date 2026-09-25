package auth

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openDeprecationDB(t *testing.T) *LegacyTokenDeprecation {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "depr.sqlite")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ltd := NewLegacyTokenDeprecation(db)
	if err := ltd.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	return ltd
}

// TestGetExpiredTokensUsesPortableTimeComparison regresses the SQLite
// portability bug the prior code had: the query used NOW(), which SQLite
// does not implement, so GetExpiredTokens raised a syntax error on every
// call. Now the query passes time.Now() as a bind parameter, which is
// portable and — bonus — makes the server clock, not an implicit DB
// clock, the source of truth.
func TestGetExpiredTokensUsesPortableTimeComparison(t *testing.T) {
	ltd := openDeprecationDB(t)
	// Insert one expired row directly so we know it should be picked up.
	past := time.Now().Add(-24 * time.Hour)
	_, err := ltd.db.Exec(
		`INSERT INTO api_token_deprecation (token_hash, deprecation_triggered_at, expires_at, last_use_at) VALUES (?, ?, ?, ?)`,
		"expired-hash", past.Add(-30*24*time.Hour), past, past,
	)
	if err != nil {
		t.Fatal(err)
	}
	// And one not-yet-expired row that must be filtered out.
	future := time.Now().Add(24 * time.Hour)
	_, err = ltd.db.Exec(
		`INSERT INTO api_token_deprecation (token_hash, deprecation_triggered_at, expires_at, last_use_at) VALUES (?, ?, ?, ?)`,
		"still-valid-hash", time.Now(), future, time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := ltd.GetExpiredTokens()
	if err != nil {
		t.Fatalf("GetExpiredTokens (prior code failed on NOW()): %v", err)
	}
	if len(expired) != 1 || expired[0] != "expired-hash" {
		t.Errorf("GetExpiredTokens returned %v, want [\"expired-hash\"]", expired)
	}
}

// TestMarkLegacyTokenDeprecatedIsIdempotent — first-use should set the
// clock; subsequent uses should update last_use_at but must not reset
// expires_at. This is critical: if MarkLegacyTokenDeprecated reset the
// expiration on every request, the grace period would never actually
// elapse and enforcement would be defeated.
func TestMarkLegacyTokenDeprecatedIsIdempotent(t *testing.T) {
	ltd := openDeprecationDB(t)
	firstExpiry, err := ltd.MarkLegacyTokenDeprecated("hash-a")
	if err != nil {
		t.Fatalf("first mark: %v", err)
	}
	// Sleep briefly so the second call's "now" differs.
	time.Sleep(10 * time.Millisecond)
	secondExpiry, err := ltd.MarkLegacyTokenDeprecated("hash-a")
	if err != nil {
		t.Fatalf("second mark: %v", err)
	}
	// Both calls return "now + 30 days" — the second call's return value
	// is the fresh calculation, but the persisted expires_at (checked via
	// the query below) must be from the first call.
	if firstExpiry == nil || secondExpiry == nil {
		t.Fatal("expiry pointer was nil")
	}
	var persisted time.Time
	if err := ltd.db.QueryRow(`SELECT expires_at FROM api_token_deprecation WHERE token_hash = ?`, "hash-a").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Equal(*firstExpiry) {
		t.Errorf("second use reset the expiration clock: persisted=%v firstExpiry=%v", persisted, *firstExpiry)
	}
}

// TestIsLegacyTokenExpired covers the three cases: unknown token (no
// deprecation record), tracked-but-not-yet-expired, and past the grace
// period.
func TestIsLegacyTokenExpired(t *testing.T) {
	ltd := openDeprecationDB(t)
	// Case 1: unknown token → not expired (no deprecation record).
	if expired, err := ltd.IsLegacyTokenExpired("never-seen"); err != nil || expired {
		t.Errorf("unknown token: expired=%v err=%v", expired, err)
	}
	// Case 2: within grace period.
	if _, err := ltd.MarkLegacyTokenDeprecated("fresh"); err != nil {
		t.Fatal(err)
	}
	if expired, err := ltd.IsLegacyTokenExpired("fresh"); err != nil || expired {
		t.Errorf("fresh token: expired=%v err=%v", expired, err)
	}
	// Case 3: past grace period — insert an artificially expired row.
	past := time.Now().Add(-time.Hour)
	_, err := ltd.db.Exec(
		`INSERT INTO api_token_deprecation (token_hash, deprecation_triggered_at, expires_at, last_use_at) VALUES (?, ?, ?, ?)`,
		"stale", past.Add(-30*24*time.Hour), past, past,
	)
	if err != nil {
		t.Fatal(err)
	}
	if expired, err := ltd.IsLegacyTokenExpired("stale"); err != nil || !expired {
		t.Errorf("stale token: expired=%v err=%v (want true, nil)", expired, err)
	}
}
