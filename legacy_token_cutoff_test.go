package stepanel

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	authpolicy "github.com/cyberducttape/StePanel/internal/auth"
)

type legacyCutoffFixture struct {
	auth   Auth
	db     *sql.DB
	now    *time.Time
	legacy string
	scoped string
}

func newLegacyCutoffFixture(t *testing.T, withPolicy bool) *legacyCutoffFixture {
	t.Helper()
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control-plane.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := authpolicy.LegacyTokenHardCutoff.AddDate(0, 0, -60)
	f := &legacyCutoffFixture{db: db, now: &now}
	f.auth = Auth{Enabled: true, Username: "admin", AuditLog: filepath.Join(t.TempDir(), "audit.log"), apiTokens: &apiTokenStore{db: db}}
	if withPolicy {
		ltd := authpolicy.NewLegacyTokenDeprecationWithClock(db, func() time.Time { return *f.now })
		if err := ltd.InitializeSchema(); err != nil {
			t.Fatal(err)
		}
		f.auth.legacyTokenDeprecation = ltd
	}
	f.legacy = "stp_legacy-unscoped-token-value-for-cutoff-tests"
	f.insertToken(t, "legacy", f.legacy, "")
	_, f.scoped, err = f.auth.apiTokens.createScoped("admin", "scoped", nil, []string{"admin:read"}, adminAPIScopes)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// insertToken stores a token the way releases before scoping did: no scopes.
func (f *legacyCutoffFixture) insertToken(t *testing.T, id, secret, scopes string) {
	t.Helper()
	digest := sha256.Sum256([]byte(secret))
	if _, err := f.db.Exec(`INSERT INTO api_tokens (id, username, name, token_hash, token_prefix, scopes, created_at) VALUES (?, 'admin', ?, ?, ?, ?, ?)`,
		id, id, hex.EncodeToString(digest[:]), secret[:12], scopes, time.Now().Add(-400*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
}

func (f *legacyCutoffFixture) authenticates(secret string) bool {
	request := httptest.NewRequest(http.MethodGet, "/api/sites", nil)
	request.Header.Set("Authorization", "Bearer "+secret)
	_, _, ok := f.auth.validAPITokenWithScopes(request)
	return ok
}

func (f *legacyCutoffFixture) revoked(t *testing.T, id string) bool {
	t.Helper()
	var revokedAt sql.NullInt64
	if err := f.db.QueryRow(`SELECT revoked_at FROM api_tokens WHERE id = ?`, id).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	return revokedAt.Valid
}

// A legacy token that is never used before the cutoff must still stop
// working at the cutoff, and the cutoff is made durable by revoking it.
func TestDormantLegacyTokenRefusedAndRevokedAtCutoff(t *testing.T) {
	f := newLegacyCutoffFixture(t, true)
	*f.now = authpolicy.LegacyTokenHardCutoff.Add(time.Minute)

	if f.authenticates(f.legacy) {
		t.Fatal("dormant legacy token authenticated after the hard cutoff")
	}
	if !f.revoked(t, "legacy") {
		t.Fatal("legacy token was refused but not revoked in the token store")
	}
	if !f.authenticates(f.scoped) {
		t.Fatal("scoped token stopped working at the legacy cutoff")
	}
}

func TestLegacyTokenWorksUntilCutoffThenStops(t *testing.T) {
	f := newLegacyCutoffFixture(t, true)
	*f.now = authpolicy.LegacyTokenHardCutoff.Add(-time.Hour)
	if !f.authenticates(f.legacy) {
		t.Fatal("legacy token refused before the cutoff")
	}
	// Using it shortly before the deadline must not buy another grace period.
	*f.now = authpolicy.LegacyTokenHardCutoff
	if f.authenticates(f.legacy) {
		t.Fatal("legacy token used before the cutoff kept working past it")
	}
}

// The startup sweep revokes unscoped tokens even if none is presented.
func TestStartupSweepRevokesLegacyTokensAfterCutoff(t *testing.T) {
	f := newLegacyCutoffFixture(t, true)
	if err := f.auth.enforceLegacyTokenCutoff(); err != nil {
		t.Fatal(err)
	}
	if f.revoked(t, "legacy") {
		t.Fatal("startup sweep revoked a legacy token before the cutoff")
	}
	*f.now = authpolicy.LegacyTokenHardCutoff.Add(time.Second)
	if err := f.auth.enforceLegacyTokenCutoff(); err != nil {
		t.Fatal(err)
	}
	if !f.revoked(t, "legacy") {
		t.Fatal("startup sweep left a legacy token active after the cutoff")
	}
}

func TestLegacyTokenRefusedWithoutDeprecationPolicy(t *testing.T) {
	f := newLegacyCutoffFixture(t, false)
	if f.authenticates(f.legacy) {
		t.Fatal("legacy token authenticated without a deprecation policy")
	}
	if !f.authenticates(f.scoped) {
		t.Fatal("scoped token refused without a deprecation policy")
	}
}
