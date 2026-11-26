package stepanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Site SFTP/shell access lives in site_access (one row per site, with a
// revision) and site_access_keys (its authorized SSH keys, in order) instead
// of the whole-object "site-access" state blob. SiteAccessStore keeps its map
// and lock; persistence is a keyedRowTable (keyed_rows.go).

const siteAccessTables = `CREATE TABLE IF NOT EXISTS site_access (
	site TEXT PRIMARY KEY,
	sftp_enabled INTEGER NOT NULL DEFAULT 0 CHECK (sftp_enabled IN (0, 1)),
	shell_enabled INTEGER NOT NULL DEFAULT 0 CHECK (shell_enabled IN (0, 1)),
	state TEXT NOT NULL DEFAULT 'applied',
	last_error TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS site_access_keys (
	site TEXT NOT NULL REFERENCES site_access(site) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	label TEXT NOT NULL,
	public_key TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	PRIMARY KEY (site, position)
)`

func siteAccessEqual(a, b SiteAccess) bool {
	if a.Site != b.Site || a.SFTPEnabled != b.SFTPEnabled || a.ShellEnabled != b.ShellEnabled || a.State != b.State || a.LastError != b.LastError || len(a.Keys) != len(b.Keys) {
		return false
	}
	for i := range a.Keys {
		if a.Keys[i] != b.Keys[i] {
			return false
		}
	}
	return true
}

func cloneSiteAccess(access SiteAccess) SiteAccess {
	access.Keys = append([]SSHKey{}, access.Keys...)
	return access
}

func insertSiteAccessKeys(ctx context.Context, tx *sql.Tx, site string, keys []SSHKey) error {
	for position, key := range keys {
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_access_keys (site, position, label, public_key, fingerprint) VALUES (?, ?, ?, ?, ?)`, site, position, key.Label, key.PublicKey, key.Fingerprint); err != nil {
			return err
		}
	}
	return nil
}

var siteAccessRows = keyedRowTable[SiteAccess]{
	name:  "site access",
	equal: siteAccessEqual,
	clone: cloneSiteAccess,
	load:  loadSiteAccess,
	insert: func(ctx context.Context, tx *sql.Tx, site string, access SiteAccess) (bool, error) {
		applied, err := oneRowAffected(tx.ExecContext(ctx, `INSERT INTO site_access (site, sftp_enabled, shell_enabled, state, last_error) VALUES (?, ?, ?, ?, ?) ON CONFLICT(site) DO NOTHING`, site, boolInt(access.SFTPEnabled), boolInt(access.ShellEnabled), access.State, access.LastError))
		if err != nil || !applied {
			return applied, err
		}
		return true, insertSiteAccessKeys(ctx, tx, site, access.Keys)
	},
	update: func(ctx context.Context, tx *sql.Tx, site string, access SiteAccess, revision int64) (bool, error) {
		applied, err := oneRowAffected(tx.ExecContext(ctx, `UPDATE site_access SET sftp_enabled = ?, shell_enabled = ?, state = ?, last_error = ?, revision = revision + 1 WHERE site = ? AND revision = ?`, boolInt(access.SFTPEnabled), boolInt(access.ShellEnabled), access.State, access.LastError, site, revision))
		if err != nil || !applied {
			return applied, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM site_access_keys WHERE site = ?`, site); err != nil {
			return false, err
		}
		return true, insertSiteAccessKeys(ctx, tx, site, access.Keys)
	},
	remove: func(ctx context.Context, tx *sql.Tx, site string, revision int64) (bool, error) {
		// site_access_keys rows go with it (ON DELETE CASCADE).
		return oneRowAffected(tx.ExecContext(ctx, `DELETE FROM site_access WHERE site = ? AND revision = ?`, site, revision))
	},
}

func loadSiteAccess(ctx context.Context, db *sql.DB) (map[string]SiteAccess, map[string]int64, error) {
	values := map[string]SiteAccess{}
	revisions := map[string]int64{}
	rows, err := db.QueryContext(ctx, `SELECT site, sftp_enabled, shell_enabled, state, last_error, revision FROM site_access`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var access SiteAccess
		var sftp, shell int
		var revision int64
		if err := rows.Scan(&access.Site, &sftp, &shell, &access.State, &access.LastError, &revision); err != nil {
			_ = rows.Close() // best-effort: the scan error is returned
			return nil, nil, err
		}
		access.SFTPEnabled, access.ShellEnabled = sftp == 1, shell == 1
		access.Keys = []SSHKey{}
		values[access.Site] = access
		revisions[access.Site] = revision
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	keys, err := db.QueryContext(ctx, `SELECT site, label, public_key, fingerprint FROM site_access_keys ORDER BY site, position`)
	if err != nil {
		return nil, nil, err
	}
	defer keys.Close()
	for keys.Next() {
		var site string
		var key SSHKey
		if err := keys.Scan(&site, &key.Label, &key.PublicKey, &key.Fingerprint); err != nil {
			return nil, nil, err
		}
		access, ok := values[site]
		if !ok {
			return nil, nil, fmt.Errorf("SSH key for site %s has no access row", site)
		}
		access.Keys = append(access.Keys, key)
		values[site] = access
	}
	return values, revisions, keys.Err()
}

// migrateSiteAccessBlob moves the "site-access" state blob, if any, into the
// site access tables and deletes it.
func migrateSiteAccessBlob(tx *sql.Tx) error {
	if _, err := tx.Exec(siteAccessTables); err != nil {
		return err
	}
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'site-access'`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read site access state: %w", err)
	}
	var accesses map[string]SiteAccess
	if err := json.Unmarshal(payload, &accesses); err != nil {
		return fmt.Errorf("decode site access state: %w", err)
	}
	ctx := context.Background()
	for site, access := range accesses {
		if access.Site == "" {
			access.Site = site
		}
		if access.Site != site {
			return fmt.Errorf("site access state holds site %q under key %q", access.Site, site)
		}
		if access.State == "" {
			access.State = "applied"
		}
		if _, err := tx.Exec(`INSERT INTO site_access (site, sftp_enabled, shell_enabled, state, last_error) VALUES (?, ?, ?, ?, ?)`, site, boolInt(access.SFTPEnabled), boolInt(access.ShellEnabled), access.State, access.LastError); err != nil {
			return fmt.Errorf("migrate site access %s: %w", site, err)
		}
		if err := insertSiteAccessKeys(ctx, tx, site, access.Keys); err != nil {
			return fmt.Errorf("migrate SSH keys of %s: %w", site, err)
		}
	}
	_, err = tx.Exec(`DELETE FROM state_blobs WHERE name = 'site-access'`)
	return err
}

// bindSiteAccess makes the site access tables the store's persistence.
func bindSiteAccess(s *SiteAccessStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = &keyedRowState[SiteAccess]{}
	}
	return siteAccessRows.bind(s.rows, db, s.values)
}
