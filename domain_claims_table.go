package stepanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Domain claims live in domain_claims, one row per domain with a revision,
// instead of the whole-object "domain-claims" state blob, which every claim
// change rewrote in full (up to 4 MiB at 100,000 claims). The primary key
// also makes one claim per domain a database invariant across processes.
// DomainClaimStore keeps its map and lock; persistence is a keyedRowTable.

const domainClaimsTable = `CREATE TABLE IF NOT EXISTS domain_claims (
	domain TEXT PRIMARY KEY,
	site TEXT NOT NULL,
	token TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	verified_at TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	tenant TEXT NOT NULL DEFAULT '',
	expires_at TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1
)`

func formatClaimTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseClaimTime(domain, field, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("domain claim %s has an invalid %s: %w", domain, field, err)
	}
	return parsed, nil
}

func domainClaimArgs(claim DomainClaim) []any {
	return []any{claim.Site, claim.Token, claim.State, formatClaimTime(claim.VerifiedAt), claim.LastError, claim.Tenant, formatClaimTime(claim.ExpiresAt)}
}

func domainClaimEqual(a, b DomainClaim) bool {
	return a.Domain == b.Domain && a.Site == b.Site && a.Token == b.Token && a.State == b.State && a.LastError == b.LastError && a.Tenant == b.Tenant &&
		formatClaimTime(a.VerifiedAt) == formatClaimTime(b.VerifiedAt) && formatClaimTime(a.ExpiresAt) == formatClaimTime(b.ExpiresAt)
}

const domainClaimInsert = `INSERT INTO domain_claims (domain, site, token, state, verified_at, last_error, tenant, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

var domainClaimRows = keyedRowTable[DomainClaim]{
	name:  "domain claims",
	equal: domainClaimEqual,
	load:  loadDomainClaims,
	insert: func(ctx context.Context, tx *sql.Tx, domain string, claim DomainClaim) (bool, error) {
		return oneRowAffected(tx.ExecContext(ctx, domainClaimInsert+` ON CONFLICT(domain) DO NOTHING`, append([]any{domain}, domainClaimArgs(claim)...)...))
	},
	update: func(ctx context.Context, tx *sql.Tx, domain string, claim DomainClaim, revision int64) (bool, error) {
		return oneRowAffected(tx.ExecContext(ctx, `UPDATE domain_claims SET site = ?, token = ?, state = ?, verified_at = ?, last_error = ?, tenant = ?, expires_at = ?, revision = revision + 1 WHERE domain = ? AND revision = ?`, append(domainClaimArgs(claim), domain, revision)...))
	},
	remove: func(ctx context.Context, tx *sql.Tx, domain string, revision int64) (bool, error) {
		return oneRowAffected(tx.ExecContext(ctx, `DELETE FROM domain_claims WHERE domain = ? AND revision = ?`, domain, revision))
	},
}

func loadDomainClaims(ctx context.Context, db *sql.DB) (map[string]DomainClaim, map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT domain, site, token, state, verified_at, last_error, tenant, expires_at, revision FROM domain_claims`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	values := map[string]DomainClaim{}
	revisions := map[string]int64{}
	for rows.Next() {
		var claim DomainClaim
		var verified, expires string
		var revision int64
		if err := rows.Scan(&claim.Domain, &claim.Site, &claim.Token, &claim.State, &verified, &claim.LastError, &claim.Tenant, &expires, &revision); err != nil {
			return nil, nil, err
		}
		if claim.VerifiedAt, err = parseClaimTime(claim.Domain, "verified_at", verified); err != nil {
			return nil, nil, err
		}
		if claim.ExpiresAt, err = parseClaimTime(claim.Domain, "expires_at", expires); err != nil {
			return nil, nil, err
		}
		values[claim.Domain] = claim
		revisions[claim.Domain] = revision
	}
	return values, revisions, rows.Err()
}

// migrateDomainClaimsBlob moves the "domain-claims" state blob, if any, into
// domain_claims and deletes it.
func migrateDomainClaimsBlob(tx *sql.Tx) error {
	if _, err := tx.Exec(domainClaimsTable); err != nil {
		return err
	}
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'domain-claims'`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read domain claim state: %w", err)
	}
	var claims map[string]DomainClaim
	if err := json.Unmarshal(payload, &claims); err != nil {
		return fmt.Errorf("decode domain claim state: %w", err)
	}
	for domain, claim := range claims {
		if claim.Domain == "" {
			claim.Domain = domain
		}
		if claim.Domain != domain {
			return fmt.Errorf("domain claim state holds %q under key %q", claim.Domain, domain)
		}
		if _, err := tx.Exec(domainClaimInsert, append([]any{domain}, domainClaimArgs(claim)...)...); err != nil {
			return fmt.Errorf("migrate domain claim %s: %w", domain, err)
		}
	}
	_, err = tx.Exec(`DELETE FROM state_blobs WHERE name = 'domain-claims'`)
	return err
}

// bindDomainClaims makes domain_claims the store's persistence.
func bindDomainClaims(s *DomainClaimStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = &keyedRowState[DomainClaim]{}
	}
	return domainClaimRows.bind(s.rows, db, s.values)
}
