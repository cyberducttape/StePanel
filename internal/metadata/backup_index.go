package metadata

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// BackupIndex manages SQLite-backed indexing of backup metadata
// Replaces filesystem archaeology with indexed database queries
type BackupIndex struct {
	db *sql.DB
}

// BackupEntry represents indexed backup metadata
type BackupEntry struct {
	Site           string
	Backup         string
	Path           string
	ArchiveSHA256  string
	Bytes          int64
	CreatedAt      time.Time
	VerifiedAt     time.Time
	Consistency    string
	ManifestSigned bool
	Databases      []string
	LastIndexed    time.Time
}

// NewBackupIndex creates or opens the backup index
func NewBackupIndex(db *sql.DB) (*BackupIndex, error) {
	if db == nil {
		return nil, errors.New("database connection required")
	}

	idx := &BackupIndex{db: db}
	if err := idx.initSchema(); err != nil {
		return nil, fmt.Errorf("failed to initialize backup index schema: %w", err)
	}

	return idx, nil
}

// initSchema creates the backup index tables if they don't exist
func (idx *BackupIndex) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS backup_index (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		site TEXT NOT NULL,
		backup_name TEXT NOT NULL,
		backup_path TEXT NOT NULL UNIQUE,
		archive_sha256 TEXT,
		bytes_total INTEGER,
		created_at TIMESTAMP,
		verified_at TIMESTAMP,
		consistency TEXT,
		manifest_signed BOOLEAN DEFAULT 0,
		last_indexed TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(site, backup_name),
		INDEX idx_site (site),
		INDEX idx_created (created_at DESC),
		INDEX idx_verified (verified_at DESC)
	);

	CREATE TABLE IF NOT EXISTS backup_databases (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		backup_id INTEGER NOT NULL,
		database_name TEXT NOT NULL,
		FOREIGN KEY (backup_id) REFERENCES backup_index(id) ON DELETE CASCADE,
		UNIQUE(backup_id, database_name)
	);

	CREATE TABLE IF NOT EXISTS backup_verification_cache (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		backup_id INTEGER NOT NULL,
		verified_checksum TEXT,
		verified_at TIMESTAMP,
		cache_ttl_seconds INTEGER DEFAULT 300,
		FOREIGN KEY (backup_id) REFERENCES backup_index(id) ON DELETE CASCADE
	);
	`

	_, err := idx.db.Exec(schema)
	return err
}

// AddBackup indexes a new backup
func (idx *BackupIndex) AddBackup(entry BackupEntry) error {
	stmt, err := idx.db.Prepare(`
		INSERT INTO backup_index (
			site, backup_name, backup_path, archive_sha256, bytes_total,
			created_at, verified_at, consistency, manifest_signed
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(site, backup_name) DO UPDATE SET
			verified_at = excluded.verified_at,
			consistency = excluded.consistency,
			last_indexed = CURRENT_TIMESTAMP
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	result, err := stmt.Exec(
		entry.Site, entry.Backup, entry.Path, entry.ArchiveSHA256,
		entry.Bytes, entry.CreatedAt, entry.VerifiedAt,
		entry.Consistency, entry.ManifestSigned,
	)
	if err != nil {
		return err
	}

	backupID, err := result.LastInsertId()
	if err != nil {
		return err
	}

	// Index databases
	if len(entry.Databases) > 0 {
		dbStmt, err := idx.db.Prepare("INSERT INTO backup_databases (backup_id, database_name) VALUES (?, ?)")
		if err != nil {
			return err
		}
		defer dbStmt.Close()

		for _, db := range entry.Databases {
			if _, err := dbStmt.Exec(backupID, db); err != nil {
				return err
			}
		}
	}

	return nil
}

// ListBackups returns backups for a site (indexed, fast)
func (idx *BackupIndex) ListBackups(site string, limit int) ([]BackupEntry, error) {
	query := `
		SELECT site, backup_name, backup_path, archive_sha256, bytes_total,
		       created_at, verified_at, consistency, manifest_signed, last_indexed
		FROM backup_index
		WHERE site = ? AND verified_at IS NOT NULL
		ORDER BY verified_at DESC
		LIMIT ?
	`

	rows, err := idx.db.Query(query, site, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var backups []BackupEntry
	for rows.Next() {
		var entry BackupEntry
		err := rows.Scan(
			&entry.Site, &entry.Backup, &entry.Path, &entry.ArchiveSHA256,
			&entry.Bytes, &entry.CreatedAt, &entry.VerifiedAt,
			&entry.Consistency, &entry.ManifestSigned, &entry.LastIndexed,
		)
		if err != nil {
			return nil, err
		}

		// Load associated databases
		dbRows, err := idx.db.Query(
			"SELECT database_name FROM backup_databases WHERE backup_id = (SELECT id FROM backup_index WHERE site = ? AND backup_name = ?)",
			entry.Site, entry.Backup,
		)
		if err == nil {
			defer dbRows.Close()
			for dbRows.Next() {
				var dbName string
				if err := dbRows.Scan(&dbName); err == nil {
					entry.Databases = append(entry.Databases, dbName)
				}
			}
		}

		backups = append(backups, entry)
	}

	return backups, rows.Err()
}

// RemoveBackup deletes a backup index entry
func (idx *BackupIndex) RemoveBackup(site, backupName string) error {
	stmt, err := idx.db.Prepare("DELETE FROM backup_index WHERE site = ? AND backup_name = ?")
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(site, backupName)
	return err
}

// ReindexFilesystem walks the filesystem and updates the index
// Call periodically to sync filesystem state with index
func (idx *BackupIndex) ReindexFilesystem(backupRoot string) error {
	// Mark all existing entries as stale
	_, err := idx.db.Exec("UPDATE backup_index SET last_indexed = '1970-01-01' WHERE 1=1")
	if err != nil {
		return err
	}

	// Walk filesystem and update index
	return filepath.Walk(backupRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip inaccessible paths
		}

		// Only index manifest files
		if !info.IsDir() && filepath.Ext(path) == ".json" {
			rel, err := filepath.Rel(backupRoot, path)
			if err != nil {
				return nil
			}

			// Parse site and backup from path: site/backup-TIMESTAMP/manifest.json
			parts := filepath.SplitList(filepath.Dir(rel))
			if len(parts) >= 2 {
				site := parts[0]
				backup := parts[1]

				// Could load manifest here and extract metadata
				// For now, mark as indexed
				_, _ = idx.db.Exec(
					"UPDATE backup_index SET last_indexed = CURRENT_TIMESTAMP WHERE site = ? AND backup_name = ?",
					site, backup,
				)
			}
		}

		return nil
	})
}

// CleanupStaleEntries removes backups that no longer exist on filesystem
func (idx *BackupIndex) CleanupStaleEntries(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := idx.db.Exec(
		"DELETE FROM backup_index WHERE last_indexed < ?",
		cutoff,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// GetVerificationCache returns cached verification result if valid
func (idx *BackupIndex) GetVerificationCache(site, backupName string) (string, error) {
	query := `
		SELECT bvc.verified_checksum
		FROM backup_verification_cache bvc
		JOIN backup_index bi ON bvc.backup_id = bi.id
		WHERE bi.site = ? AND bi.backup_name = ?
		AND bvc.verified_at > datetime('now', '-' || bvc.cache_ttl_seconds || ' seconds')
		LIMIT 1
	`

	var checksum string
	err := idx.db.QueryRow(query, site, backupName).Scan(&checksum)
	if err == sql.ErrNoRows {
		return "", nil // Cache miss
	}
	return checksum, err
}

// SetVerificationCache stores verification result with TTL
func (idx *BackupIndex) SetVerificationCache(site, backupName, checksum string, ttlSeconds int) error {
	stmt, err := idx.db.Prepare(`
		INSERT INTO backup_verification_cache (backup_id, verified_checksum, verified_at, cache_ttl_seconds)
		SELECT id, ?, CURRENT_TIMESTAMP, ?
		FROM backup_index
		WHERE site = ? AND backup_name = ?
		ON CONFLICT DO UPDATE SET
			verified_checksum = excluded.verified_checksum,
			verified_at = CURRENT_TIMESTAMP
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	_, err = stmt.Exec(checksum, ttlSeconds, site, backupName)
	return err
}
