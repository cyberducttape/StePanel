package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LegacyTokenDeprecation manages the deprecation timeline for unscoped legacy API tokens
// Timeline:
// - Day 0: Token first used after deprecation policy takes effect
// - Day 1-30: Token marked as deprecated, audit logged
// - Day 31: Token automatically expires
// - Day 32+: Token rejected with clear message
type LegacyTokenDeprecation struct {
	db *sql.DB
}

const (
	// Grace period for token migration (30 days)
	GracePeriodDays = 30

	// When to consider a token "stale" for deprecation
	// This allows us to track first-use for newly created legacy tokens
	StaleAfterDays = 365 // If created >1 year ago, likely forgotten token

	DeprecationMessage = "legacy unscoped API tokens are deprecated and will expire soon; migrate to scoped tokens"
	ExpirationMessage  = "legacy unscoped API tokens have expired; create a new scoped API token"
)

// NewLegacyTokenDeprecation creates a deprecation manager
func NewLegacyTokenDeprecation(db *sql.DB) *LegacyTokenDeprecation {
	return &LegacyTokenDeprecation{db: db}
}

// InitializeSchema ensures the deprecation tracking table exists
func (ltd *LegacyTokenDeprecation) InitializeSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS api_token_deprecation (
		token_hash TEXT PRIMARY KEY,
		deprecation_triggered_at TIMESTAMP NOT NULL,
		expires_at TIMESTAMP NOT NULL,
		last_use_at TIMESTAMP,
		audit_logged BOOLEAN DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_deprecation_expires ON api_token_deprecation(expires_at);
	`

	_, err := ltd.db.Exec(schema)
	return err
}

// MarkLegacyTokenDeprecated marks a legacy token as deprecated on first use
// Returns the expiration date
func (ltd *LegacyTokenDeprecation) MarkLegacyTokenDeprecated(tokenHash string) (*time.Time, error) {
	if ltd.db == nil {
		return nil, errors.New("database unavailable")
	}

	now := time.Now()
	expiresAt := now.AddDate(0, 0, GracePeriodDays) // 30 days from now

	stmt, err := ltd.db.Prepare(`
		INSERT INTO api_token_deprecation (token_hash, deprecation_triggered_at, expires_at, last_use_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(token_hash) DO UPDATE SET
			last_use_at = excluded.last_use_at
	`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	_, err = stmt.Exec(tokenHash, now, expiresAt, now)
	if err != nil {
		return nil, err
	}

	return &expiresAt, nil
}

// IsLegacyTokenExpired checks if a legacy token has expired
func (ltd *LegacyTokenDeprecation) IsLegacyTokenExpired(tokenHash string) (bool, error) {
	if ltd.db == nil {
		return false, errors.New("database unavailable")
	}

	var expiresAt time.Time
	err := ltd.db.QueryRow(
		`SELECT expires_at FROM api_token_deprecation WHERE token_hash = ?`,
		tokenHash,
	).Scan(&expiresAt)

	if err == sql.ErrNoRows {
		return false, nil // Token not yet deprecated
	}
	if err != nil {
		return false, err
	}

	return time.Now().After(expiresAt), nil
}

// GetLegacyTokenStatus returns the status of a legacy token
type LegacyTokenStatus struct {
	IsDeprecated bool
	IsExpired    bool
	ExpiresAt    *time.Time
	DaysRemaining int
	Message      string
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

	now := time.Now()
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

// GetExpiredTokens returns all legacy tokens that have expired
func (ltd *LegacyTokenDeprecation) GetExpiredTokens() ([]string, error) {
	if ltd.db == nil {
		return nil, errors.New("database unavailable")
	}

	rows, err := ltd.db.Query(
		`SELECT token_hash FROM api_token_deprecation WHERE expires_at < NOW()`,
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
