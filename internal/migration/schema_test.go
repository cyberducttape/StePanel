package migration

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openMigrationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:migration-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(SchemaMigrationsTable); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrationMetadataAndApply(t *testing.T) {
	db := openMigrationDB(t)
	migration := NewMigration(7, "add widget", func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY)`)
		return err
	})
	if migration.Version() != 7 || migration.Description() != "add widget" {
		t.Fatalf("migration metadata = %d/%q", migration.Version(), migration.Description())
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	skipped, err := applyMigrationToTx(tx, migration)
	if err != nil || skipped {
		t.Fatalf("apply migration = skipped %v err %v", skipped, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRow(`SELECT version FROM control_plane_migrations`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("recorded version = %d err=%v", version, err)
	}
}

func TestMigrationCheckSkipsExistingSchema(t *testing.T) {
	db := openMigrationDB(t)
	if _, err := db.Exec(`CREATE TABLE widgets (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	migration := NewMigrationWithCheck(8, "add widget", func(tx *sql.Tx) (bool, error) {
		return columnExists(tx, "widgets", "id")
	}, func(tx *sql.Tx) error { return errors.New("must not apply") })
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	skipped, err := applyMigrationToTx(tx, migration)
	if err != nil || !skipped {
		t.Fatalf("checked migration = skipped %v err %v", skipped, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationFailuresAreReturned(t *testing.T) {
	db := openMigrationDB(t)
	migration := NewMigration(9, "fail", func(*sql.Tx) error { return errors.New("apply failed") })
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrationToTx(tx, migration); err == nil || !strings.Contains(err.Error(), "apply failed") {
		t.Fatalf("apply failure = %v", err)
	}
	_ = tx.Rollback()

	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if exists, err := columnExists(tx, "missing", "id"); err != nil || exists {
		t.Fatalf("missing table column check = %v err=%v", exists, err)
	}
	_ = tx.Rollback()
}

func TestMigrationDetectionAndRecordErrors(t *testing.T) {
	db := openMigrationDB(t)
	detection := NewMigrationWithCheck(10, "detect failure", func(*sql.Tx) (bool, error) {
		return false, errors.New("detector failed")
	}, func(*sql.Tx) error { return nil })
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrationToTx(tx, detection); err == nil || !strings.Contains(err.Error(), "detect pre-existing migration") {
		t.Fatalf("detection error = %v", err)
	}
	_ = tx.Rollback()

	if _, err := db.Exec(`INSERT INTO control_plane_migrations (version) VALUES (11)`); err != nil {
		t.Fatal(err)
	}
	recordFailure := NewMigration(11, "duplicate", func(*sql.Tx) error { return nil })
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyMigrationToTx(tx, recordFailure); err == nil || !strings.Contains(err.Error(), "record migration") {
		t.Fatalf("record error = %v", err)
	}
	_ = tx.Rollback()
}
