package sitelayout

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotPlaced means the site has no placement and uses the legacy layout.
var ErrNotPlaced = errors.New("site has no account placement")

// Schema creates the layout tables. Control-plane migration 21 applies it.
const Schema = `
CREATE TABLE IF NOT EXISTS account_identities (
	account TEXT PRIMARY KEY,
	unix_user TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	retired_at INTEGER
);
CREATE TABLE IF NOT EXISTS site_placements (
	site TEXT PRIMARY KEY,
	account TEXT NOT NULL,
	unix_user TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('main', 'addon', 'subdomain')),
	directory TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	UNIQUE (unix_user, directory)
);
CREATE UNIQUE INDEX IF NOT EXISTS site_placements_one_main
	ON site_placements(account) WHERE kind = 'main';
CREATE INDEX IF NOT EXISTS site_placements_account ON site_placements(account);
`

// AllocateAccount records that account owns the Unix user of the same name and
// returns it. Allocation is idempotent for the same account. A Unix user name
// is never handed to a different account, even after the first is retired,
// so files and quota history can never be inherited by a new customer.
func AllocateAccount(db *sql.DB, account string) (string, error) {
	if db == nil {
		return "", errors.New("account layout requires the control-plane database")
	}
	if err := ValidUnixUser(account); err != nil {
		return "", err
	}
	tx, err := db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin account identity transaction: %w", err)
	}
	defer tx.Rollback()
	var unixUser string
	var retired sql.NullInt64
	err = tx.QueryRow(`SELECT unix_user, retired_at FROM account_identities WHERE account = ?`, account).Scan(&unixUser, &retired)
	switch {
	case err == nil && retired.Valid:
		return "", fmt.Errorf("account %q was retired; its Unix user is not reused", account)
	case err == nil:
		return unixUser, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("read account identity: %w", err)
	}
	var owner string
	if err := tx.QueryRow(`SELECT account FROM account_identities WHERE unix_user = ?`, account).Scan(&owner); err == nil {
		return "", fmt.Errorf("Unix user %q already belongs to account %q", account, owner)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("check Unix user ownership: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO account_identities(account, unix_user, created_at) VALUES (?, ?, ?)`, account, account, time.Now().UTC().Unix()); err != nil {
		return "", fmt.Errorf("record account identity: %w", err)
	}
	return account, tx.Commit()
}

// AccountUser returns the Unix user allocated to account.
func AccountUser(db *sql.DB, account string) (string, bool, error) {
	if db == nil {
		return "", false, errors.New("account layout requires the control-plane database")
	}
	var unixUser string
	var retired sql.NullInt64
	err := db.QueryRow(`SELECT unix_user, retired_at FROM account_identities WHERE account = ?`, account).Scan(&unixUser, &retired)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read account identity: %w", err)
	}
	if retired.Valid {
		return "", false, nil
	}
	return unixUser, true, nil
}

// RetireAccount marks the account's Unix user as permanently retired. It
// fails while the account still has placed sites.
func RetireAccount(db *sql.DB, account string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin account retirement: %w", err)
	}
	defer tx.Rollback()
	var sites int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM site_placements WHERE account = ?`, account).Scan(&sites); err != nil {
		return fmt.Errorf("count account sites: %w", err)
	}
	if sites > 0 {
		return fmt.Errorf("account %q still has %d site(s)", account, sites)
	}
	if _, err := tx.Exec(`UPDATE account_identities SET retired_at = ? WHERE account = ? AND retired_at IS NULL`, time.Now().UTC().Unix(), account); err != nil {
		return fmt.Errorf("retire account identity: %w", err)
	}
	return tx.Commit()
}

// Place records a site inside its account home. The account must already
// have an allocated Unix user, which the placement must name.
func Place(db *sql.DB, p Placement) error {
	if db == nil {
		return errors.New("account layout requires the control-plane database")
	}
	if err := p.Validate(); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin site placement: %w", err)
	}
	defer tx.Rollback()
	var unixUser string
	var retired sql.NullInt64
	err = tx.QueryRow(`SELECT unix_user, retired_at FROM account_identities WHERE account = ?`, p.Account).Scan(&unixUser, &retired)
	if errors.Is(err, sql.ErrNoRows) || retired.Valid {
		return fmt.Errorf("account %q has no active Unix user", p.Account)
	}
	if err != nil {
		return fmt.Errorf("read account identity: %w", err)
	}
	if unixUser != p.UnixUser {
		return fmt.Errorf("site %q must use account %q's Unix user %q", p.Site, p.Account, unixUser)
	}
	var legacy int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM site_identities WHERE site = ?`, p.Site).Scan(&legacy); err != nil && !strings.Contains(err.Error(), "no such table") {
		return fmt.Errorf("check legacy site identity: %w", err)
	}
	if legacy > 0 {
		return fmt.Errorf("site %q already uses the legacy per-site layout", p.Site)
	}
	if _, err := tx.Exec(`INSERT INTO site_placements(site, account, unix_user, kind, directory, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		p.Site, p.Account, p.UnixUser, string(p.Kind), p.Directory, time.Now().UTC().Unix()); err != nil {
		return describePlacementConflict(tx, p, err)
	}
	return tx.Commit()
}

func describePlacementConflict(tx *sql.Tx, p Placement, cause error) error {
	var existing string
	if tx.QueryRow(`SELECT account FROM site_placements WHERE site = ?`, p.Site).Scan(&existing) == nil {
		return fmt.Errorf("site %q is already placed in account %q", p.Site, existing)
	}
	if tx.QueryRow(`SELECT site FROM site_placements WHERE unix_user = ? AND directory = ?`, p.UnixUser, p.Directory).Scan(&existing) == nil {
		return fmt.Errorf("directory %s is already used by site %q", p.Directory, existing)
	}
	if p.Kind == KindMain && tx.QueryRow(`SELECT site FROM site_placements WHERE account = ? AND kind = 'main'`, p.Account).Scan(&existing) == nil {
		return fmt.Errorf("account %q already has main site %q", p.Account, existing)
	}
	return fmt.Errorf("record site placement: %w", cause)
}

// Get returns the placement of site, or ErrNotPlaced for a legacy site.
func Get(db *sql.DB, site string) (Placement, error) {
	if db == nil {
		return Placement{}, errors.New("account layout requires the control-plane database")
	}
	var p Placement
	var kind string
	err := db.QueryRow(`SELECT site, account, unix_user, kind, directory FROM site_placements WHERE site = ?`, site).
		Scan(&p.Site, &p.Account, &p.UnixUser, &kind, &p.Directory)
	if errors.Is(err, sql.ErrNoRows) {
		return Placement{}, ErrNotPlaced
	}
	if err != nil {
		return Placement{}, fmt.Errorf("read site placement: %w", err)
	}
	p.Kind = Kind(kind)
	if err := p.Validate(); err != nil {
		return Placement{}, fmt.Errorf("stored placement of site %q is invalid: %w", site, err)
	}
	return p, nil
}

// ForAccount lists an account's placed sites, main site first.
func ForAccount(db *sql.DB, account string) ([]Placement, error) {
	rows, err := db.Query(`SELECT site, account, unix_user, kind, directory FROM site_placements WHERE account = ?
		ORDER BY CASE kind WHEN 'main' THEN 0 ELSE 1 END, directory`, account)
	if err != nil {
		return nil, fmt.Errorf("list account sites: %w", err)
	}
	defer rows.Close()
	var placements []Placement
	for rows.Next() {
		var p Placement
		var kind string
		if err := rows.Scan(&p.Site, &p.Account, &p.UnixUser, &kind, &p.Directory); err != nil {
			return nil, fmt.Errorf("read account site: %w", err)
		}
		p.Kind = Kind(kind)
		placements = append(placements, p)
	}
	return placements, rows.Err()
}

// Remove deletes a site's placement after the site itself has been removed.
// Removing an unplaced site is a no-op so teardown can be retried.
func Remove(db *sql.DB, site string) error {
	if _, err := db.Exec(`DELETE FROM site_placements WHERE site = ?`, site); err != nil {
		return fmt.Errorf("remove site placement: %w", err)
	}
	return nil
}
