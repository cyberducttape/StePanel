package importer

import "time"

// ArchiveInspection represents the result of inspecting an archive's contents
type ArchiveInspection struct {
	// Basic archive info
	URL         string    `json:"url"`
	ArchiveType string    `json:"archive_type"` // "tar.gz", "zip"
	Size        int64     `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`

	// Config file location and content
	ConfigPath string `json:"config_path"`
	ConfigType string `json:"config_type"` // "wordpress", "custom", "unknown"

	// Site requirements discovered from config
	Requirements SiteRequirements `json:"requirements"`

	// Archive structure summary
	Structure ArchiveStructure `json:"structure"`

	// Issues/warnings
	Issues []ImportIssue `json:"issues"`
}

// SiteRequirements describes what a site needs to run
type SiteRequirements struct {
	// PHP version requirement
	PHPVersion string `json:"php_version"` // e.g., "8.1", "7.4-8.2"

	// Database
	DatabaseType    string `json:"database_type"` // "mysql", "postgresql", "sqlite"
	DatabaseVersion string `json:"database_version"`
	DatabaseName    string `json:"database_name"`
	DatabaseUser    string `json:"database_user"`

	// Extensions required
	Extensions []string `json:"extensions"` // e.g., ["mysqli", "curl", "gd"]

	// Web server
	WebServer string `json:"web_server"` // "apache", "nginx"

	// Storage estimate
	EstimatedStorageGB  int64 `json:"estimated_storage_gb"`
	EstimatedDatabaseGB int64 `json:"estimated_database_gb"`
}

// ArchiveStructure summarizes what's in the archive
type ArchiveStructure struct {
	TotalFiles         int64    `json:"total_files"`
	TotalDirs          int64    `json:"total_dirs"`
	LargestFiles       []string `json:"largest_files"`    // top 5 by size
	FileExtensions     []string `json:"file_extensions"`  // unique extensions found
	DirectoryLayout    string   `json:"directory_layout"` // text summary
	EstimatedStorageGB int64    `json:"estimated_storage_gb"`

	// Content hints
	HasWordPressCore bool `json:"has_wordpress_core"`
	HasWordPressMU   bool `json:"has_wordpress_mu"`
	HasDatabase      bool `json:"has_database"`
	HasBackupData    bool `json:"has_backup_data"`
}

// ImportIssue represents a problem or warning during inspection
type ImportIssue struct {
	Severity string `json:"severity"` // "error", "warning", "info"
	Code     string `json:"code"`     // machine-readable code
	Message  string `json:"message"`  // human-readable message
}

// ArchiveImportRequest is the API request to start an import
type ArchiveImportRequest struct {
	URL              string `json:"url"`                         // URL to archive (S3, HTTP, etc.)
	ConfigPath       string `json:"config_path"`                 // path to config file in archive
	SiteName         string `json:"site_name"`                   // name for new site
	DatabasePassword string `json:"database_password,omitempty"` // password for database restoration (Phase 2)
	DatabaseHost     string `json:"database_host,omitempty"`     // database host (default: localhost)
	AutoRestoreDB    bool   `json:"auto_restore_db,omitempty"`   // whether to auto-restore database if credentials provided
}

// ArchiveImportResponse is the result of starting an import job
type ArchiveImportResponse struct {
	JobID             string             `json:"job_id"`
	Status            string             `json:"status"` // "pending", "analyzing", "ready", "importing", "done", "failed"
	ArchiveInspection *ArchiveInspection `json:"inspection,omitempty"`
	Progress          ImportProgress     `json:"progress"`
	Error             string             `json:"error,omitempty"`
	EstimatedDuration string             `json:"estimated_duration_seconds,omitempty"`
}

// ImportProgress tracks the status of an import operation
type ImportProgress struct {
	Phase           string    `json:"phase"` // "validation", "extraction", "config-parse", "db-restore", "finalize"
	PercentComplete int       `json:"percent_complete"`
	FilesProcessed  int64     `json:"files_processed"`
	BytesProcessed  int64     `json:"bytes_processed"`
	CurrentFile     string    `json:"current_file,omitempty"`
	Message         string    `json:"message,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ImportResult is the final result of an import
type ImportResult struct {
	JobID         string        `json:"job_id"`
	Success       bool          `json:"success"`
	SiteName      string        `json:"site_name"`
	SiteStatus    string        `json:"site_status"` // "initializing", "needs_database_restore", "needs_database_setup", "ready", "failed"
	CreatedAt     time.Time     `json:"created_at"`
	FilesImported int64         `json:"files_imported"`
	DatabaseSize  int64         `json:"database_size_bytes"`
	StorageSize   int64         `json:"storage_size_bytes"`
	Issues        []ImportIssue `json:"issues"`
	NextSteps     []string      `json:"next_steps"` // recommended actions
	Error         string        `json:"error,omitempty"`
}
