package metadata

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		UNIQUE(site, backup_name)
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

	CREATE INDEX IF NOT EXISTS idx_site ON backup_index(site);
	CREATE INDEX IF NOT EXISTS idx_created ON backup_index(created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_verified ON backup_index(verified_at DESC);

	CREATE TABLE IF NOT EXISTS offsite_backup_state (
		target_hash TEXT NOT NULL,
		site TEXT NOT NULL,
		backup_name TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		uploaded_at INTEGER,
		restore_verified_at INTEGER,
		PRIMARY KEY(target_hash, site, backup_name)
	);
	CREATE INDEX IF NOT EXISTS idx_offsite_pending ON offsite_backup_state(target_hash, uploaded_at, created_at);
	`

	_, err := idx.db.Exec(schema)
	return err
}

// OffsiteBackupSummary only describes backups first observed after tracking
// was introduced. Legacy backups remain explicitly untracked.
type OffsiteBackupSummary struct {
	LastSuccessfulBackup *time.Time `json:"last_successful_backup,omitempty"`
	LastVerifiedRestore  *time.Time `json:"last_verified_restore,omitempty"`
	OldestUnreplicated   *time.Time `json:"oldest_unreplicated_backup,omitempty"`
	TrackedBackups       int64      `json:"tracked_backups"`
}

func offsiteTargetHash(target string) string {
	sum := sha256.Sum256([]byte(target))
	return hex.EncodeToString(sum[:])
}

func (idx *BackupIndex) TrackOffsiteBackup(target, site, backup string, createdAt time.Time) error {
	if idx == nil || idx.db == nil {
		return errors.New("backup index is unavailable")
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := idx.db.Exec(`INSERT INTO offsite_backup_state(target_hash,site,backup_name,created_at) VALUES(?,?,?,?) ON CONFLICT(target_hash,site,backup_name) DO NOTHING`, offsiteTargetHash(target), site, backup, createdAt.UTC().UnixNano())
	return err
}

func (idx *BackupIndex) MarkOffsiteUploaded(target, site, backup string, at time.Time) error {
	if idx == nil || idx.db == nil {
		return errors.New("backup index is unavailable")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := idx.db.Exec(`UPDATE offsite_backup_state SET uploaded_at=? WHERE target_hash=? AND site=? AND backup_name=?`, at.UTC().UnixNano(), offsiteTargetHash(target), site, backup)
	return err
}

func (idx *BackupIndex) MarkOffsiteRestoreVerified(target, site, backup string, at time.Time) error {
	if idx == nil || idx.db == nil {
		return errors.New("backup index is unavailable")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := idx.db.Exec(`UPDATE offsite_backup_state SET restore_verified_at=? WHERE target_hash=? AND site=? AND backup_name=?`, at.UTC().UnixNano(), offsiteTargetHash(target), site, backup)
	return err
}

// OffsiteBackupState reports whether one backup is tracked for the target
// and when its upload completed. Backups taken before offsite tracking
// existed are reported as untracked rather than as missing.
func (idx *BackupIndex) OffsiteBackupState(target, site, backup string) (tracked bool, uploadedAt *time.Time, err error) {
	if idx == nil || idx.db == nil {
		return false, nil, errors.New("backup index is unavailable")
	}
	var uploaded sql.NullInt64
	err = idx.db.QueryRow(`SELECT uploaded_at FROM offsite_backup_state WHERE target_hash=? AND site=? AND backup_name=?`, offsiteTargetHash(target), site, backup).Scan(&uploaded)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if uploaded.Valid {
		t := time.Unix(0, uploaded.Int64).UTC()
		uploadedAt = &t
	}
	return true, uploadedAt, nil
}

func (idx *BackupIndex) OffsiteSummary(target string) (OffsiteBackupSummary, error) {
	var s OffsiteBackupSummary
	if idx == nil || idx.db == nil {
		return s, errors.New("backup index is unavailable")
	}
	hash := offsiteTargetHash(target)
	var uploaded, restored, pending sql.NullInt64
	err := idx.db.QueryRow(`SELECT MAX(uploaded_at),MAX(restore_verified_at),MIN(CASE WHEN uploaded_at IS NULL THEN created_at END),COUNT(*) FROM offsite_backup_state WHERE target_hash=?`, hash).Scan(&uploaded, &restored, &pending, &s.TrackedBackups)
	if err != nil {
		return s, err
	}
	if uploaded.Valid {
		t := time.Unix(0, uploaded.Int64).UTC()
		s.LastSuccessfulBackup = &t
	}
	if restored.Valid {
		t := time.Unix(0, restored.Int64).UTC()
		s.LastVerifiedRestore = &t
	}
	if pending.Valid {
		t := time.Unix(0, pending.Int64).UTC()
		s.OldestUnreplicated = &t
	}
	return s, nil
}

// AddBackup indexes a new backup
func (idx *BackupIndex) AddBackup(entry BackupEntry) error {
	tx, err := idx.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`
		INSERT INTO backup_index (
			site, backup_name, backup_path, archive_sha256, bytes_total,
			created_at, verified_at, consistency, manifest_signed
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(site, backup_name) DO UPDATE SET
			backup_path = excluded.backup_path,
			archive_sha256 = excluded.archive_sha256,
			bytes_total = excluded.bytes_total,
			created_at = excluded.created_at,
			verified_at = excluded.verified_at,
			consistency = excluded.consistency,
		manifest_signed = excluded.manifest_signed,
			last_indexed = CURRENT_TIMESTAMP
	`, entry.Site, entry.Backup, entry.Path, entry.ArchiveSHA256,
		entry.Bytes, entry.CreatedAt, entry.VerifiedAt,
		entry.Consistency, entry.ManifestSigned)
	if err != nil {
		return err
	}

	var backupID int64
	err = tx.QueryRow("SELECT id FROM backup_index WHERE site = ? AND backup_name = ?", entry.Site, entry.Backup).Scan(&backupID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM backup_databases WHERE backup_id = ?", backupID); err != nil {
		return err
	}

	// Index databases
	if len(entry.Databases) > 0 {
		dbStmt, err := tx.Prepare("INSERT INTO backup_databases (backup_id, database_name) VALUES (?, ?)")
		if err != nil {
			return err
		}

		for _, db := range entry.Databases {
			if _, err := dbStmt.Exec(backupID, db); err != nil {
				_ = dbStmt.Close()
				return err
			}
		}
		if err := dbStmt.Close(); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
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
		if err != nil {
			return nil, err
		}
		for dbRows.Next() {
			var dbName string
			if err := dbRows.Scan(&dbName); err != nil {
				_ = dbRows.Close()
				return nil, err
			}
			entry.Databases = append(entry.Databases, dbName)
		}
		if err := dbRows.Err(); err != nil {
			_ = dbRows.Close()
			return nil, err
		}
		if err := dbRows.Close(); err != nil {
			return nil, err
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
			return err
		}

		// Only index manifest files
		if !info.IsDir() && filepath.Ext(path) == ".json" {
			rel, err := filepath.Rel(backupRoot, path)
			if err != nil {
				return fmt.Errorf("resolve backup index path %q: %w", path, err)
			}

			// Parse site and backup from path: site/backup-TIMESTAMP/manifest.json
			parts := splitBackupPath(filepath.Dir(rel))
			if len(parts) >= 2 {
				site := parts[0]
				backup := parts[1]

				// Could load manifest here and extract metadata
				// For now, mark as indexed
				if _, err := idx.db.Exec(
					"UPDATE backup_index SET last_indexed = CURRENT_TIMESTAMP WHERE site = ? AND backup_name = ?",
					site, backup,
				); err != nil {
					return fmt.Errorf("update backup index for %s/%s: %w", site, backup, err)
				}
			}
		}

		return nil
	})
}

// splitBackupPath returns path components using the platform's separator.
// filepath.SplitList is for PATH-like lists, not directory paths; using it
// here silently left normal Unix site/backup paths unindexed.
func splitBackupPath(path string) []string {
	clean := filepath.Clean(path)
	if clean == "." || clean == string(filepath.Separator) {
		return nil
	}
	parts := strings.Split(clean, string(filepath.Separator))
	filtered := parts[:0]
	for _, part := range parts {
		if part != "" && part != "." {
			filtered = append(filtered, part)
		}
	}
	return filtered
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
