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

func TestLegacyTokenStatusAndCleanup(t *testing.T) {
	ltd := openDeprecationDB(t)
	if status, err := ltd.GetLegacyTokenStatus("unknown"); err != nil || status != nil {
		t.Fatalf("unknown status = %#v err=%v", status, err)
	}
	if _, err := ltd.MarkLegacyTokenDeprecated("active"); err != nil {
		t.Fatal(err)
	}
	status, err := ltd.GetLegacyTokenStatus("active")
	if err != nil || status == nil || !status.IsDeprecated || status.IsExpired || status.Message == "" {
		t.Fatalf("active status = %#v err=%v", status, err)
	}
	past := time.Now().Add(-48 * time.Hour)
	if _, err := ltd.db.Exec(`INSERT INTO api_token_deprecation (token_hash, deprecation_triggered_at, expires_at, last_use_at) VALUES (?, ?, ?, ?)`, "cleanup", past, past, past); err != nil {
		t.Fatal(err)
	}
	status, err = ltd.GetLegacyTokenStatus("cleanup")
	if err != nil || status == nil || !status.IsExpired || status.Message != ExpirationMessage {
		t.Fatalf("expired status = %#v err=%v", status, err)
	}
	removed, err := ltd.CleanupExpiredTokens(1)
	if err != nil || removed != 1 {
		t.Fatalf("cleanup removed=%d err=%v, want one", removed, err)
	}
}

func TestLegacyTokenDeprecationRequiresDatabase(t *testing.T) {
	ltd := NewLegacyTokenDeprecation(nil)
	if _, err := ltd.MarkLegacyTokenDeprecated("x"); err == nil {
		t.Fatal("mark accepted nil database")
	}
	if _, err := ltd.IsLegacyTokenExpired("x"); err == nil {
		t.Fatal("expiry check accepted nil database")
	}
	if _, err := ltd.GetLegacyTokenStatus("x"); err == nil {
		t.Fatal("status accepted nil database")
	}
	if _, err := ltd.CleanupExpiredTokens(1); err == nil {
		t.Fatal("cleanup accepted nil database")
	}
	if _, err := ltd.GetExpiredTokens(); err == nil {
		t.Fatal("expired-token listing accepted nil database")
	}
}

func openDeprecationDBAt(t *testing.T, path string, now func() time.Time) *LegacyTokenDeprecation {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ltd := NewLegacyTokenDeprecationWithClock(db, now)
	if err := ltd.InitializeSchema(); err != nil {
		t.Fatal(err)
	}
	return ltd
}

// A host that activates the policy before the published cutoff enforces the
// published cutoff; one that upgrades later gets the grace period from its
// activation. Re-initializing never moves the deadline.
func TestLegacyTokenDeadlineIsHostWideAndFixed(t *testing.T) {
	early := LegacyTokenHardCutoff.AddDate(0, 0, -45)
	path := filepath.Join(t.TempDir(), "early.sqlite")
	ltd := openDeprecationDBAt(t, path, func() time.Time { return early })
	if got := ltd.Deadline(); !got.Equal(LegacyTokenHardCutoff) {
		t.Fatalf("early activation deadline = %v, want %v", got, LegacyTokenHardCutoff)
	}
	reopened := openDeprecationDBAt(t, path, func() time.Time { return early.AddDate(1, 0, 0) })
	if got := reopened.Deadline(); !got.Equal(LegacyTokenHardCutoff) {
		t.Fatalf("re-initialization moved the deadline to %v", got)
	}

	late := LegacyTokenHardCutoff.AddDate(0, 3, 0)
	lateLtd := openDeprecationDBAt(t, filepath.Join(t.TempDir(), "late.sqlite"), func() time.Time { return late })
	if want := time.Unix(late.Unix(), 0).UTC().AddDate(0, 0, GracePeriodDays); !lateLtd.Deadline().Equal(want) {
		t.Fatalf("late activation deadline = %v, want %v", lateLtd.Deadline(), want)
	}
}

// The regression: expiry used to start at a token's first use, so a dormant
// token (or a stolen one replayed later) was never expired. Past the cutoff a
// token with no use record must be refused, and first use before the cutoff
// must not grant more time than the cutoff.
func TestDormantLegacyTokenExpiresAtHardCutoff(t *testing.T) {
	now := LegacyTokenHardCutoff.AddDate(0, 0, -60) // policy activated well before the cutoff
	ltd := openDeprecationDBAt(t, filepath.Join(t.TempDir(), "depr.sqlite"), func() time.Time { return now })
	now = LegacyTokenHardCutoff.Add(-24 * time.Hour)

	expiry, err := ltd.MarkLegacyTokenDeprecated("active")
	if err != nil {
		t.Fatal(err)
	}
	if !expiry.Equal(LegacyTokenHardCutoff) {
		t.Fatalf("first-use expiry = %v, want the hard cutoff %v", expiry, LegacyTokenHardCutoff)
	}
	if expired, err := ltd.IsLegacyTokenExpired("dormant"); err != nil || expired {
		t.Fatalf("dormant token before cutoff: expired=%v err=%v", expired, err)
	}

	now = LegacyTokenHardCutoff
	for _, hash := range []string{"active", "dormant"} {
		if expired, err := ltd.IsLegacyTokenExpired(hash); err != nil || !expired {
			t.Fatalf("%s token at cutoff: expired=%v err=%v, want expired", hash, expired, err)
		}
	}
}

func TestUninitializedLegacyTokenPolicyFailsClosed(t *testing.T) {
	ltd := NewLegacyTokenDeprecation(nil)
	if !ltd.CutoffPassed() {
		t.Fatal("uninitialized policy reported the cutoff as not passed")
	}
}

// Every entry point fails closed without a database, and nothing may mark a
// token before the host-wide deadline is known.
func TestLegacyTokenPolicyFailsClosedWithoutState(t *testing.T) {
	ltd := NewLegacyTokenDeprecationWithClock(nil, nil)
	if ltd.now == nil {
		t.Fatal("nil clock was not replaced")
	}
	if err := ltd.InitializeSchema(); err == nil {
		t.Error("InitializeSchema without a database succeeded")
	}
	if _, err := ltd.MarkLegacyTokenDeprecated("h"); err == nil {
		t.Error("MarkLegacyTokenDeprecated without a database succeeded")
	}
	if _, err := ltd.IsLegacyTokenExpired("h"); err == nil {
		t.Error("IsLegacyTokenExpired without a database succeeded")
	}
	if _, err := ltd.GetLegacyTokenStatus("h"); err == nil {
		t.Error("GetLegacyTokenStatus without a database succeeded")
	}
	if _, err := ltd.CleanupExpiredTokens(1); err == nil {
		t.Error("CleanupExpiredTokens without a database succeeded")
	}
	if _, err := ltd.GetExpiredTokens(); err == nil {
		t.Error("GetExpiredTokens without a database succeeded")
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "uninit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	uninitialized := NewLegacyTokenDeprecation(db)
	if _, err := uninitialized.MarkLegacyTokenDeprecated("h"); err == nil {
		t.Error("a token was marked before the policy deadline was loaded")
	}
	if expired, _ := uninitialized.IsLegacyTokenExpired("h"); !expired {
		t.Error("an uninitialized policy treated a legacy token as valid")
	}
}

func TestLegacyTokenPolicyReportsStoreFailures(t *testing.T) {
	now := LegacyTokenHardCutoff.AddDate(0, 0, -60)
	ltd := openDeprecationDBAt(t, filepath.Join(t.TempDir(), "depr.sqlite"), func() time.Time { return now })
	if _, err := ltd.db.Exec(`DROP TABLE api_token_deprecation`); err != nil {
		t.Fatal(err)
	}
	if _, err := ltd.MarkLegacyTokenDeprecated("h"); err == nil {
		t.Error("MarkLegacyTokenDeprecated ignored a missing table")
	}
	if _, err := ltd.IsLegacyTokenExpired("h"); err == nil {
		t.Error("IsLegacyTokenExpired ignored a missing table")
	}
	if _, err := ltd.CleanupExpiredTokens(1); err == nil {
		t.Error("CleanupExpiredTokens ignored a missing table")
	}
	if _, err := ltd.GetExpiredTokens(); err == nil {
		t.Error("GetExpiredTokens ignored a missing table")
	}
	if _, err := ltd.db.Exec(`DROP TABLE legacy_token_policy; CREATE TABLE legacy_token_policy (id INTEGER PRIMARY KEY, activated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := ltd.db.Exec(`INSERT INTO legacy_token_policy (id, activated_at) VALUES (1, 'not a time')`); err != nil {
		t.Fatal(err)
	}
	if err := ltd.InitializeSchema(); err == nil {
		t.Error("InitializeSchema accepted an unreadable activation time")
	}
}

func TestLegacyTokenStatusReportsTheDeadline(t *testing.T) {
	now := LegacyTokenHardCutoff.AddDate(0, 0, -60)
	ltd := openDeprecationDBAt(t, filepath.Join(t.TempDir(), "depr.sqlite"), func() time.Time { return now })
	if status, err := ltd.GetLegacyTokenStatus("unused"); err != nil || status != nil {
		t.Fatalf("status of an unused token = %+v, %v", status, err)
	}
	if _, err := ltd.MarkLegacyTokenDeprecated("used"); err != nil {
		t.Fatal(err)
	}
	status, err := ltd.GetLegacyTokenStatus("used")
	if err != nil || status == nil || status.IsExpired || status.ExpiresAt == nil || !status.ExpiresAt.Equal(LegacyTokenHardCutoff) {
		t.Fatalf("status before the cutoff = %+v, %v", status, err)
	}
	now = LegacyTokenHardCutoff.Add(time.Hour)
	if status, err = ltd.GetLegacyTokenStatus("used"); err != nil || !status.IsExpired || status.Message != ExpirationMessage {
		t.Fatalf("status after the cutoff = %+v, %v", status, err)
	}
	if removed, err := ltd.CleanupExpiredTokens(0); err != nil || removed != 1 {
		t.Fatalf("CleanupExpiredTokens = %d, %v", removed, err)
	}
}
