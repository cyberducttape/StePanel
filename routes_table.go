package stepanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Desired proxy routes live in desired_routes, one row per route name with a
// revision, instead of the whole-object "routes" state blob, which grew with
// every served domain. RouteStore keeps its map and lock; persistence is a
// keyedRowTable.

const desiredRoutesTable = `CREATE TABLE IF NOT EXISTS desired_routes (
	name TEXT PRIMARY KEY,
	site TEXT NOT NULL,
	domain TEXT NOT NULL,
	state TEXT NOT NULL,
	last_error TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1
)`

func routeArgs(route RouteDesired) []any {
	return []any{route.Site, route.Domain, route.State, route.LastError, formatClaimTime(route.UpdatedAt)}
}

func routeEqual(a, b RouteDesired) bool {
	return a.Name == b.Name && a.Site == b.Site && a.Domain == b.Domain && a.State == b.State && a.LastError == b.LastError && formatClaimTime(a.UpdatedAt) == formatClaimTime(b.UpdatedAt)
}

const desiredRouteInsert = `INSERT INTO desired_routes (name, site, domain, state, last_error, updated_at) VALUES (?, ?, ?, ?, ?, ?)`

var routeRows = keyedRowTable[RouteDesired]{
	name:  "desired routes",
	equal: routeEqual,
	load:  loadDesiredRoutes,
	insert: func(ctx context.Context, tx *sql.Tx, name string, route RouteDesired) (bool, error) {
		return oneRowAffected(tx.ExecContext(ctx, desiredRouteInsert+` ON CONFLICT(name) DO NOTHING`, append([]any{name}, routeArgs(route)...)...))
	},
	update: func(ctx context.Context, tx *sql.Tx, name string, route RouteDesired, revision int64) (bool, error) {
		return oneRowAffected(tx.ExecContext(ctx, `UPDATE desired_routes SET site = ?, domain = ?, state = ?, last_error = ?, updated_at = ?, revision = revision + 1 WHERE name = ? AND revision = ?`, append(routeArgs(route), name, revision)...))
	},
	remove: func(ctx context.Context, tx *sql.Tx, name string, revision int64) (bool, error) {
		return oneRowAffected(tx.ExecContext(ctx, `DELETE FROM desired_routes WHERE name = ? AND revision = ?`, name, revision))
	},
}

func loadDesiredRoutes(ctx context.Context, db *sql.DB) (map[string]RouteDesired, map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, site, domain, state, last_error, updated_at, revision FROM desired_routes`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	values := map[string]RouteDesired{}
	revisions := map[string]int64{}
	for rows.Next() {
		var route RouteDesired
		var updated string
		var revision int64
		if err := rows.Scan(&route.Name, &route.Site, &route.Domain, &route.State, &route.LastError, &updated, &revision); err != nil {
			return nil, nil, err
		}
		if updated != "" {
			if route.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
				return nil, nil, fmt.Errorf("route %s has an invalid updated_at: %w", route.Name, err)
			}
		}
		values[route.Name] = route
		revisions[route.Name] = revision
	}
	return values, revisions, rows.Err()
}

// migrateRoutesBlob moves the "routes" state blob, if any, into
// desired_routes and deletes it.
func migrateRoutesBlob(tx *sql.Tx) error {
	if _, err := tx.Exec(desiredRoutesTable); err != nil {
		return err
	}
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'routes'`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read route state: %w", err)
	}
	var routes map[string]RouteDesired
	if err := json.Unmarshal(payload, &routes); err != nil {
		return fmt.Errorf("decode route state: %w", err)
	}
	for name, route := range routes {
		if route.Name == "" {
			route.Name = name
		}
		if route.Name != name {
			return fmt.Errorf("route state holds %q under key %q", route.Name, name)
		}
		if _, err := tx.Exec(desiredRouteInsert, append([]any{name}, routeArgs(route)...)...); err != nil {
			return fmt.Errorf("migrate route %s: %w", name, err)
		}
	}
	_, err = tx.Exec(`DELETE FROM state_blobs WHERE name = 'routes'`)
	return err
}

// bindRoutes makes desired_routes the store's persistence.
func bindRoutes(s *RouteStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = &keyedRowState[RouteDesired]{}
	}
	return routeRows.bind(s.rows, db, s.values)
}
