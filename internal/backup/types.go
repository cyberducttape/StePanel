package backup

import "time"

const MaxBackupBytes int64 = 20 << 30

type BackupEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type BackupManifest struct {
	Version              int           `json:"version"`
	Site                 string        `json:"site"`
	CreatedAt            time.Time     `json:"created_at"`
	VerifiedAt           time.Time     `json:"verified_at"`
	Archive              string        `json:"archive"`
	ArchiveSHA256        string        `json:"archive_sha256"`
	Bytes                int64         `json:"bytes"`
	Databases            []string      `json:"databases"`
	Entries              []BackupEntry `json:"entries"`
	Consistency          string        `json:"consistency"`
	ArchiveVerified      bool          `json:"archive_verified"`
	DatabaseDumpVerified bool          `json:"database_dump_verified"`
	ApplicationQuiesced  bool          `json:"application_quiesced"`
	FilesystemSnapshot   bool          `json:"filesystem_snapshot"`
	SignatureAlgorithm   string        `json:"signature_algorithm,omitempty"`
}

type BackupResult struct {
	Site           string    `json:"site"`
	Path           string    `json:"path"`
	ArchiveSHA256  string    `json:"archive_sha256"`
	Bytes          int64     `json:"bytes"`
	Databases      []string  `json:"databases"`
	CreatedAt      time.Time `json:"created_at"`  // When backup was created (immutable)
	VerifiedAt     time.Time `json:"verified_at"` // When backup was last verified
	Consistency    string    `json:"consistency"`
	ManifestSigned bool      `json:"manifest_signed"`
}

type RestoreToStagingRequest struct {
	Backup         string `json:"backup"`
	SourceSite     string `json:"source_site,omitempty"`
	Site           string `json:"site"`
	Domain         string `json:"domain"`
	Database       string `json:"database,omitempty"`
	TargetDatabase string `json:"target_database,omitempty"`
	TargetUser     string `json:"target_user,omitempty"`
	TargetPassword string `json:"target_password,omitempty"`
}

type BackupRestoreResult struct {
	Site              string    `json:"site"`
	Backup            string    `json:"backup"`
	Mode              string    `json:"mode"`
	Database          string    `json:"database,omitempty"`
	FilesRestored     bool      `json:"files_restored"`
	DatabaseRestored  bool      `json:"database_restored"`
	DatabasePreserved bool      `json:"database_preserved"`
	SafetyBackup      string    `json:"safety_backup,omitempty"`
	Consistency       string    `json:"consistency"`
	SchemaRollback    string    `json:"schema_rollback"`
	CompletedAt       time.Time `json:"completed_at"`
}

type DurableBackupRestoreRequest struct {
	Mode     string `json:"mode"`
	Site     string `json:"site"`
	Backup   string `json:"backup"`
	Database string `json:"database,omitempty"`
	Actor    string `json:"actor"`
}

type RestoreDatabaseInput struct {
	Database       string
	TargetDatabase string
	TargetUser     string
	TargetPassword string
}

type BackupSchedule struct {
	Site             string     `json:"site"`
	IntervalMinutes  int        `json:"interval_minutes"`
	KeepLast         int        `json:"keep_last"`
	IncludeDatabases bool       `json:"include_databases"`
	Enabled          bool       `json:"enabled"`
	NextRun          time.Time  `json:"next_run"`
	LastRun          *time.Time `json:"last_run,omitempty"`
	LastSuccess      *time.Time `json:"last_success,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
	LastDurationMS   int64      `json:"last_duration_ms,omitempty"`
	ConsecutiveFails int        `json:"consecutive_failures"`
}
