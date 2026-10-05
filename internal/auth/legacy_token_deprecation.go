package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LegacyTokenDeprecation enforces a single, host-wide hard cutoff for unscoped
// legacy API tokens.
//
// The cutoff is the later of LegacyTokenHardCutoff and 30 days after this
// policy was first activated on the host (persisted once, never moved). It
// does not depend on when a token is used: a dormant token, or one stolen and
// replayed later, is refused at the same deadline as an active one. Earlier
// releases started a per-token 30-day clock at first use, which let an unused
// token stay valid indefinitely.
type LegacyTokenDeprecation struct {
	db       *sql.DB
	now      func() time.Time
	deadline time.Time
}

const (
	// Grace period for token migration after the policy is activated on a
	// host that upgrades after LegacyTokenHardCutoff.
	GracePeriodDays = 30

	// When to consider a token "stale" for deprecation
	// This allows us to track first-use for newly created legacy tokens
	StaleAfterDays = 365 // If created >1 year ago, likely forgotten token

	DeprecationMessage = "legacy unscoped API tokens are deprecated and will expire soon; migrate to scoped tokens"
	ExpirationMessage  = "legacy unscoped API tokens have expired; create a new scoped API token"
)

// LegacyTokenHardCutoff is the published deadline after which no unscoped
// legacy token is accepted on a host that activated the policy in time.
var LegacyTokenHardCutoff = time.Date(2026, time.November, 15, 0, 0, 0, 0, time.UTC)

// NewLegacyTokenDeprecation creates a deprecation manager
func NewLegacyTokenDeprecation(db *sql.DB) *LegacyTokenDeprecation {
	return NewLegacyTokenDeprecationWithClock(db, time.Now)
}

// NewLegacyTokenDeprecationWithClock creates a deprecation manager that reads
// the time from now. It exists for tests that cross the cutoff.
func NewLegacyTokenDeprecationWithClock(db *sql.DB, now func() time.Time) *LegacyTokenDeprecation {
	if now == nil {
		now = time.Now
	}
	return &LegacyTokenDeprecation{db: db, now: now}
}

// InitializeSchema ensures the deprecation tracking tables exist, records the
// policy activation time on first run, and loads the host-wide deadline.
func (ltd *LegacyTokenDeprecation) InitializeSchema() error {
	if ltd.db == nil {
		return errors.New("database unavailable")
	}
	schema := `
	CREATE TABLE IF NOT EXISTS api_token_deprecation (
		token_hash TEXT PRIMARY KEY,
		deprecation_triggered_at TIMESTAMP NOT NULL,
		expires_at TIMESTAMP NOT NULL,
		last_use_at TIMESTAMP,
		audit_logged BOOLEAN DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_deprecation_expires ON api_token_deprecation(expires_at);

	CREATE TABLE IF NOT EXISTS legacy_token_policy (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		activated_at INTEGER NOT NULL
	);
	`
	if _, err := ltd.db.Exec(schema); err != nil {
		return err
	}
	if _, err := ltd.db.Exec(`INSERT OR IGNORE INTO legacy_token_policy (id, activated_at) VALUES (1, ?)`, ltd.now().Unix()); err != nil {
		return fmt.Errorf("record legacy token policy activation: %w", err)
	}
	var activated int64
	if err := ltd.db.QueryRow(`SELECT activated_at FROM legacy_token_policy WHERE id = 1`).Scan(&activated); err != nil {
		return fmt.Errorf("read legacy token policy activation: %w", err)
	}
	ltd.deadline = LegacyTokenHardCutoff
	if graceEnd := time.Unix(activated, 0).UTC().AddDate(0, 0, GracePeriodDays); graceEnd.After(ltd.deadline) {
		ltd.deadline = graceEnd
	}
	return nil
}

// Deadline returns the host-wide hard cutoff. It is zero until
// InitializeSchema has succeeded.
func (ltd *LegacyTokenDeprecation) Deadline() time.Time {
	return ltd.deadline
}

// CutoffPassed reports whether the host-wide hard cutoff has passed. An
// uninitialized policy fails closed.
func (ltd *LegacyTokenDeprecation) CutoffPassed() bool {
	return ltd.deadline.IsZero() || !ltd.now().Before(ltd.deadline)
}

// MarkLegacyTokenDeprecated records the first and latest use of a legacy
// token. It never extends the token's life: the recorded expiry is the
// host-wide deadline.
func (ltd *LegacyTokenDeprecation) MarkLegacyTokenDeprecated(tokenHash string) (*time.Time, error) {
	if ltd.db == nil {
		return nil, errors.New("database unavailable")
	}
	if ltd.deadline.IsZero() {
		return nil, errors.New("legacy token policy is not initialized")
	}
	now := ltd.now()
	expiresAt := ltd.deadline
	_, err := ltd.db.Exec(`
		INSERT INTO api_token_deprecation (token_hash, deprecation_triggered_at, expires_at, last_use_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET
			last_use_at = excluded.last_use_at
	`, tokenHash, now, expiresAt, now)
	if err != nil {
		return nil, err
	}
	return &expiresAt, nil
}

// IsLegacyTokenExpired reports whether a legacy token must be refused: the
// host-wide cutoff has passed, or a per-token expiry recorded by an earlier
// release has already elapsed. Errors fail closed in the caller.
func (ltd *LegacyTokenDeprecation) IsLegacyTokenExpired(tokenHash string) (bool, error) {
	if ltd.db == nil {
		return false, errors.New("database unavailable")
	}
	if ltd.CutoffPassed() {
		return true, nil
	}
	var expiresAt time.Time
	err := ltd.db.QueryRow(
		`SELECT expires_at FROM api_token_deprecation WHERE token_hash = ?`,
		tokenHash,
	).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !ltd.now().Before(expiresAt), nil
}

// GetLegacyTokenStatus returns the status of a legacy token
type LegacyTokenStatus struct {
	IsDeprecated  bool
	IsExpired     bool
	ExpiresAt     *time.Time
	DaysRemaining int
	Message       string
}

func (ltd *LegacyTokenDeprecation) GetLegacyTokenStatus(tokenHash string) (*LegacyTokenStatus, error) {
	if ltd.db == nil {
		return nil, errors.New("database unavailable")
	}

	var expiresAt sql.NullTime
	var deprecationTriggeredAt sql.NullTime

	err := ltd.db.QueryRow(`
		SELECT deprecation_triggered_at, expires_at
		FROM api_token_deprecation
		WHERE token_hash = ?
	`, tokenHash).Scan(&deprecationTriggeredAt, &expiresAt)

	if err == sql.ErrNoRows {
		return nil, nil // Token not deprecated
	}
	if err != nil {
		return nil, err
	}

	now := ltd.now()
	status := &LegacyTokenStatus{
		IsDeprecated: deprecationTriggeredAt.Valid,
	}

	if expiresAt.Valid {
		status.ExpiresAt = &expiresAt.Time
		status.IsExpired = now.After(expiresAt.Time)
		daysRemaining := int(expiresAt.Time.Sub(now).Hours() / 24)
		status.DaysRemaining = daysRemaining

		if status.IsExpired {
			status.Message = ExpirationMessage
		} else {
			status.Message = fmt.Sprintf("%s (expires in %d days)", DeprecationMessage, daysRemaining)
		}
	}

	return status, nil
}

// CleanupExpiredTokens removes deprecation records for tokens that have been
// revoked or deleted. Call periodically (e.g., daily).
func (ltd *LegacyTokenDeprecation) CleanupExpiredTokens(olderThanDays int) (int64, error) {
	if ltd.db == nil {
		return 0, errors.New("database unavailable")
	}

	cutoff := time.Now().AddDate(0, 0, -olderThanDays)
	result, err := ltd.db.Exec(
		`DELETE FROM api_token_deprecation WHERE expires_at < ?`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// GetExpiredTokens returns all legacy tokens that have expired. The
// comparison uses a parameter (not SQLite's non-existent NOW() function
// the prior code called) so the query is portable and — crucially — the
// server clock and the DB clock cannot disagree.
func (ltd *LegacyTokenDeprecation) GetExpiredTokens() ([]string, error) {
	if ltd.db == nil {
		return nil, errors.New("database unavailable")
	}

	rows, err := ltd.db.Query(
		`SELECT token_hash FROM api_token_deprecation WHERE expires_at < ?`,
		time.Now(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var expired []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		expired = append(expired, hash)
	}

	return expired, rows.Err()
}
