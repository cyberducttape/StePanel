package migration

import (
	"database/sql"
	"fmt"
)

// Migration is one ordered, individually tracked schema change.
// Each migration runs inside its own transaction: either the whole thing
// commits and its version is recorded, or nothing about the schema changes
// and the version is not recorded, so a crash or failure between migrations
// never leaves an ambiguous half-applied schema for the next startup to
// guess about.
type Migration struct {
	version        int
	description    string
	AlreadyApplied func(tx *sql.Tx) (bool, error)
	Apply          func(tx *sql.Tx) error
}

// Version returns the migration version number
func (m *Migration) Version() int {
	return m.version
}

// Description returns the migration description
func (m *Migration) Description() string {
	return m.description
}

// SchemaMigrationsTable is the SQL schema for tracking applied migrations
const SchemaMigrationsTable = `
CREATE TABLE IF NOT EXISTS control_plane_migrations (
	version INTEGER PRIMARY KEY,
	applied_at INTEGER
)`

// columnExists checks if a column exists in a table
func columnExists(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, fmt.Errorf("inspect columns of %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// NewMigration creates a new migration with the given version and SQL
func NewMigration(version int, description string, apply func(tx *sql.Tx) error) *Migration {
	return &Migration{
		version:     version,
		description: description,
		Apply:       apply,
	}
}

// NewMigrationWithCheck creates a migration that can detect if it's already applied
func NewMigrationWithCheck(version int, description string, alreadyApplied func(tx *sql.Tx) (bool, error), apply func(tx *sql.Tx) error) *Migration {
	return &Migration{
		version:        version,
		description:    description,
		AlreadyApplied: alreadyApplied,
		Apply:          apply,
	}
}

// applyMigrationToTx applies a single migration transaction
func applyMigrationToTx(tx *sql.Tx, migration *Migration) (bool, error) {
	// Check if already applied (if detector exists)
	if migration.AlreadyApplied != nil {
		skip, err := migration.AlreadyApplied(tx)
		if err != nil {
			return false, fmt.Errorf("detect pre-existing migration %d: %w", migration.version, err)
		}
		if skip {
			// Record the version without re-running the schema
			if _, err := tx.Exec(`INSERT INTO control_plane_migrations (version) VALUES (?)`, migration.version); err != nil {
				return false, fmt.Errorf("record already-applied migration %d: %w", migration.version, err)
			}
			return true, nil
		}
	}

	// Apply the migration
	if err := migration.Apply(tx); err != nil {
		return false, err
	}

	// Record the version
	if _, err := tx.Exec(`INSERT INTO control_plane_migrations (version) VALUES (?)`, migration.version); err != nil {
		return false, fmt.Errorf("record migration %d: %w", migration.version, err)
	}

	return false, nil
}
