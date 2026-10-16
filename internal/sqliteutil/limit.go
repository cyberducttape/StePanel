// Package sqliteutil contains shared SQLite safety limits used by both the
// unprivileged control plane and its privileged root broker.
package sqliteutil

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

const DefaultDatabaseMaxBytes int64 = 10 << 30

// SetDatabasePageLimit limits logical SQLite database growth on this
// connection. SQLite applies max_page_count per connection, so every process
// that can write the database must call this function for its own connection.
func SetDatabasePageLimit(db *sql.DB, maxBytes int64) error {
	if db == nil || maxBytes < 16<<20 {
		return errors.New("SQLite database limit requires a database and at least 16 MiB")
	}
	var pageSize int64
	if err := db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		return fmt.Errorf("read SQLite page size: %w", err)
	}
	if pageSize < 1 {
		return errors.New("SQLite reported an invalid page size")
	}
	maxPages := maxBytes / pageSize
	var effectiveMax int64
	if err := db.QueryRow(`PRAGMA max_page_count = ` + strconv.FormatInt(maxPages, 10)).Scan(&effectiveMax); err != nil {
		return fmt.Errorf("apply SQLite page limit: %w", err)
	}
	var pageCount int64
	if err := db.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		return fmt.Errorf("read SQLite page count: %w", err)
	}
	if effectiveMax > maxPages || pageCount > maxPages {
		return fmt.Errorf("SQLite database uses %d bytes, exceeding configured page limit %d", pageCount*pageSize, maxBytes)
	}
	return nil
}
