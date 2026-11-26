package stepanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Site environment variables live in environment_sites (one row per site,
// with a revision) and environment_variables (one row per variable, secrets
// sealed bound to their site and name), instead of the whole-object
// "environment" state blob. EnvironmentStore keeps its map and lock; its
// keyedRowTable is built per store because writing seals and loading opens
// with the store's own keys. Re-sealing (key rotation, the context-binding
// migration) leaves plaintexts unchanged, so it marks every site for rewrite.

const environmentTables = `CREATE TABLE IF NOT EXISTS environment_sites (
	site TEXT PRIMARY KEY,
	revision INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS environment_variables (
	site TEXT NOT NULL REFERENCES environment_sites(site) ON DELETE CASCADE,
	name TEXT NOT NULL,
	secret INTEGER NOT NULL CHECK (secret IN (0, 1)),
	value TEXT NOT NULL,
	PRIMARY KEY (site, name)
)`

func environmentValuesEqual(a, b map[string]environmentValue) bool {
	if len(a) != len(b) {
		return false
	}
	for name, value := range a {
		if other, ok := b[name]; !ok || other != value {
			return false
		}
	}
	return true
}

// insertEnvironmentVariables writes vars, sealing secrets with seal.
func insertEnvironmentVariables(ctx context.Context, tx *sql.Tx, site string, vars map[string]environmentValue, seal func(site, name, value string) (string, error)) error {
	for name, value := range vars {
		stored := value.Value
		if value.Secret {
			sealed, err := seal(site, name, value.Value)
			if err != nil {
				return fmt.Errorf("seal %s/%s: %w", site, name, err)
			}
			stored = sealed
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO environment_variables (site, name, secret, value) VALUES (?, ?, ?, ?)`, site, name, boolInt(value.Secret), stored); err != nil {
			return err
		}
	}
	return nil
}

// environmentRows returns the store's table binding. Caller holds s.mu
// while it is used.
func (s *EnvironmentStore) environmentRows() keyedRowTable[map[string]environmentValue] {
	return keyedRowTable[map[string]environmentValue]{
		name:  "environment variables",
		equal: environmentValuesEqual,
		clone: cloneEnvironmentValues,
		load:  s.loadEnvironmentRows,
		insert: func(ctx context.Context, tx *sql.Tx, site string, vars map[string]environmentValue) (bool, error) {
			applied, err := oneRowAffected(tx.ExecContext(ctx, `INSERT INTO environment_sites (site) VALUES (?) ON CONFLICT(site) DO NOTHING`, site))
			if err != nil || !applied {
				return applied, err
			}
			return true, insertEnvironmentVariables(ctx, tx, site, vars, s.encrypt)
		},
		update: func(ctx context.Context, tx *sql.Tx, site string, vars map[string]environmentValue, revision int64) (bool, error) {
			applied, err := oneRowAffected(tx.ExecContext(ctx, `UPDATE environment_sites SET revision = revision + 1 WHERE site = ? AND revision = ?`, site, revision))
			if err != nil || !applied {
				return applied, err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM environment_variables WHERE site = ?`, site); err != nil {
				return false, err
			}
			return true, insertEnvironmentVariables(ctx, tx, site, vars, s.encrypt)
		},
		remove: func(ctx context.Context, tx *sql.Tx, site string, revision int64) (bool, error) {
			// Variables go with the site row (ON DELETE CASCADE).
			return oneRowAffected(tx.ExecContext(ctx, `DELETE FROM environment_sites WHERE site = ? AND revision = ?`, site, revision))
		},
	}
}

// loadEnvironmentRows reads every site and opens its secrets, which records
// any secret opened with a previous key or in the legacy format.
func (s *EnvironmentStore) loadEnvironmentRows(ctx context.Context, db *sql.DB) (map[string]map[string]environmentValue, map[string]int64, error) {
	values := map[string]map[string]environmentValue{}
	revisions := map[string]int64{}
	sites, err := db.QueryContext(ctx, `SELECT site, revision FROM environment_sites`)
	if err != nil {
		return nil, nil, err
	}
	for sites.Next() {
		var site string
		var revision int64
		if err := sites.Scan(&site, &revision); err != nil {
			_ = sites.Close() // best-effort: the scan error is returned
			return nil, nil, err
		}
		values[site] = map[string]environmentValue{}
		revisions[site] = revision
	}
	if err := sites.Close(); err != nil {
		return nil, nil, err
	}
	if err := sites.Err(); err != nil {
		return nil, nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT site, name, secret, value FROM environment_variables`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var site, name, stored string
		var secret int
		if err := rows.Scan(&site, &name, &secret, &stored); err != nil {
			return nil, nil, err
		}
		vars, ok := values[site]
		if !ok {
			return nil, nil, fmt.Errorf("environment variable %s has no site row %s", name, site)
		}
		value := environmentValue{Value: stored, Secret: secret == 1}
		if value.Secret {
			if len(s.key) == 0 {
				return nil, nil, errors.New("environment encryption key is required to read secret values")
			}
			plain, err := s.decrypt(site, name, stored)
			if err != nil {
				return nil, nil, fmt.Errorf("decrypt %s/%s: %w", site, name, err)
			}
			value.Value = plain
		}
		vars[name] = value
	}
	return values, revisions, rows.Err()
}

// migrateEnvironmentBlob moves the "environment" state blob, if any, into
// the environment tables, copying secrets in their sealed form, and deletes
// the blob.
func migrateEnvironmentBlob(tx *sql.Tx) error {
	if _, err := tx.Exec(environmentTables); err != nil {
		return err
	}
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'environment'`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read environment state: %w", err)
	}
	var sealed map[string]map[string]environmentValue
	if err := json.Unmarshal(payload, &sealed); err != nil {
		return fmt.Errorf("decode environment state: %w", err)
	}
	for site, vars := range sealed {
		if _, err := tx.Exec(`INSERT INTO environment_sites (site) VALUES (?)`, site); err != nil {
			return fmt.Errorf("migrate environment of %s: %w", site, err)
		}
		for name, value := range vars {
			if _, err := tx.Exec(`INSERT INTO environment_variables (site, name, secret, value) VALUES (?, ?, ?, ?)`, site, name, boolInt(value.Secret), value.Value); err != nil {
				return fmt.Errorf("migrate %s/%s: %w", site, name, err)
			}
		}
	}
	_, err = tx.Exec(`DELETE FROM state_blobs WHERE name = 'environment'`)
	return err
}

// bindEnvironment makes the environment tables the store's persistence.
func bindEnvironment(s *EnvironmentStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string]map[string]environmentValue{}
	}
	if s.rows == nil {
		s.rows = &keyedRowState[map[string]environmentValue]{}
	}
	return s.environmentRows().bind(s.rows, db, s.values)
}
