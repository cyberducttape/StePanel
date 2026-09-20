package importer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	h "github.com/itchyitchy123/StePanel/internal/helper"
)

const (
	// Archive size limits (more conservative than original)
	maxArchiveSize        = 5 * 1024 * 1024 * 1024    // 5GB compressed (configurable)
	maxDecompressedSize   = 50 * 1024 * 1024 * 1024   // 50GB decompressed (configurable)
	maxArchiveEntries     = 250000                     // 250k files/dirs (was 1M, still generous)
	maxIndividualFileSize = 10 * 1024 * 1024 * 1024   // 10GB per file
	maxDirectoriesInArchive = 25000                    // Separate limit for directories
)

// ArchiveFetcher safely downloads archives with size limits and validation
type ArchiveFetcher struct {
	httpClient *http.Client
}

// FetchArchive downloads an archive with comprehensive security checks
func (af *ArchiveFetcher) FetchArchive(ctx context.Context, url string, maxBytes int64) (io.ReadCloser, error) {
	// Prevent SSRF
	if !isAllowedURL(url) {
		return nil, fmt.Errorf("archive URL not allowed: %s", url)
	}

	// Create context-aware request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid archive URL: %w", err)
	}

	resp, err := af.httpClient.Do(req) // lgtm[go/request-forgery]: URL is validated by isAllowedURL() at line 37
	if err != nil {
		return nil, fmt.Errorf("failed to download archive: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("archive download returned %d", resp.StatusCode)
	}

	// Validate Content-Length matches claimed size
	if resp.ContentLength > 0 && resp.ContentLength > maxBytes {
		resp.Body.Close()
		return nil, fmt.Errorf("claimed archive size (%d bytes) exceeds limit (%d bytes)", resp.ContentLength, maxBytes)
	}

	// Wrap body with size limit to prevent streaming attacks
	return &limitedReadCloser{
		reader: io.LimitReader(resp.Body, maxBytes),
		closer: resp.Body,
	}, nil
}

// limitedReadCloser combines a limited reader with a close method
type limitedReadCloser struct {
	reader io.Reader
	closer io.Closer
}

func (lrc *limitedReadCloser) Read(p []byte) (int, error) {
	return lrc.reader.Read(p)
}

func (lrc *limitedReadCloser) Close() error {
	return lrc.closer.Close()
}

// Executor performs actual archive import (extraction, DB restore, etc.)
type Executor struct {
	fetcher *ArchiveFetcher
}

// NewExecutor creates a new import executor
func NewExecutor() *Executor {
	return &Executor{
		fetcher: &ArchiveFetcher{
			httpClient: &http.Client{Timeout: 30 * time.Second},
		},
	}
}

// ImportJob tracks an in-progress import
type ImportJob struct {
	ID               string
	SiteName         string
	ArchiveURL       string
	ConfigPath       string
	WebRoot          string
	Status           string // "validating", "extracting", "restoring-db", "finalizing", "done", "failed"
	Progress         int    // 0-100
	FilesExtracted   int64
	DirectoriesCreated int64
	EntriesProcessed int64 // Total entries (files + dirs) to catch directory bombs
	BytesExtracted   int64
	CurrentFile      string
	Message          string
	StartedAt        time.Time
	UpdatedAt        time.Time
	Error            string
	DatabaseName     string
	DatabaseUser     string
	DatabasePassword string
}

// ExecuteImport performs the full import workflow
func (e *Executor) ExecuteImport(ctx context.Context, req *ArchiveImportRequest, webRoot string, onProgress func(*ImportJob)) (*ImportResult, error) {
	job := &ImportJob{
		ID:         generateJobID(),
		SiteName:   req.SiteName,
		ArchiveURL: req.URL,
		ConfigPath: req.ConfigPath,
		WebRoot:    filepath.Join(webRoot, req.SiteName),
		Status:     "validating",
		StartedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	onProgress(job)

	// Step 1: Validate site can be created
	if err := e.validateSiteCreation(job); err != nil {
		job.Status = "failed"
		job.Error = err.Error()
		job.UpdatedAt = time.Now()
		onProgress(job)
		return nil, err
	}

	// Step 2: Create site directory
	if err := os.MkdirAll(job.WebRoot, 0755); err != nil {
		job.Status = "failed"
		job.Error = fmt.Sprintf("failed to create site directory: %v", err)
		job.UpdatedAt = time.Now()
		onProgress(job)
		return nil, errors.New(job.Error)
	}

	// Step 3: Download and extract archive
	job.Status = "extracting"
	job.UpdatedAt = time.Now()
	onProgress(job)

	if err := e.extractArchive(ctx, req.URL, job, onProgress); err != nil {
		job.Status = "failed"
		job.Error = fmt.Sprintf("extraction failed: %v", err)
		job.UpdatedAt = time.Now()
		onProgress(job)
		return nil, errors.New(job.Error)
	}

	// Step 4: Parse config and get database info
	job.Status = "extracting"
	job.Progress = 60
	job.Message = "Parsing configuration..."
	job.UpdatedAt = time.Now()
	onProgress(job)

	configFile := filepath.Join(job.WebRoot, req.ConfigPath)
	dbName, dbUser := e.extractDatabaseInfo(configFile)
	if dbName == "" {
		dbName = fmt.Sprintf("%s_db", req.SiteName)
	}
	if dbUser == "" {
		dbUser = fmt.Sprintf("%s_user", req.SiteName)
	}
	job.DatabaseName = dbName
	job.DatabaseUser = dbUser

	// Step 5: Look for and restore database
	job.Status = "restoring-db"
	job.Progress = 70
	job.Message = "Looking for database dump..."
	job.UpdatedAt = time.Now()
	onProgress(job)

	var dbRestorationIssue *ImportIssue
	sqlFile := e.findDatabaseDump(job.WebRoot)
	if sqlFile != "" {
		job.Message = fmt.Sprintf("Restoring database from %s...", filepath.Base(sqlFile))
		job.UpdatedAt = time.Now()
		onProgress(job)

		if err := e.restoreDatabase(ctx, sqlFile, dbName, dbUser); err != nil {
			// Database restoration failed - report as issue but don't fail import
			// (restoration might be deferred to manual step)
			dbRestorationIssue = &ImportIssue{
				Severity: "error",
				Code:     "database_restore_failed",
				Message:  fmt.Sprintf("Database restoration failed: %v. Restore manually.", err),
			}
			job.Message = dbRestorationIssue.Message
		}
	} else {
		// No database found - this is a warning, not fatal
		dbRestorationIssue = &ImportIssue{
			Severity: "warning",
			Code:     "no_database_found",
			Message:  "No database dump found in archive. Database will need to be restored manually.",
		}
		job.Message = "No database dump found; database must be restored separately"
	}

	// Step 6: Update config files with correct database credentials
	job.Status = "finalizing"
	job.Progress = 90
	job.Message = "Updating configuration..."
	job.UpdatedAt = time.Now()
	onProgress(job)

	var configIssue *ImportIssue
	if err := e.updateConfiguration(job, req.ConfigPath); err != nil {
		// Configuration update failed - this is an error
		configIssue = &ImportIssue{
			Severity: "error",
			Code:     "config_update_failed",
			Message:  fmt.Sprintf("Configuration update failed: %v. Update manually.", err),
		}
		job.Message = configIssue.Message
	}

	// Step 7: Mark complete
	job.Status = "done"
	job.Progress = 100
	job.Message = "Import complete"
	job.UpdatedAt = time.Now()
	onProgress(job)

	result := &ImportResult{
		JobID:         job.ID,
		Success:       configIssue == nil, // Only successful if config was updated
		SiteName:      job.SiteName,
		CreatedAt:     job.StartedAt,
		FilesImported: job.FilesExtracted,
		StorageSize:   job.BytesExtracted,
		Issues: []ImportIssue{
			{
				Severity: "info",
				Code:     "files_extracted",
				Message:  fmt.Sprintf("Successfully extracted %d files", job.FilesExtracted),
			},
		},
		NextSteps: []string{
			"Verify site loads at https://panel.example.com/site/" + req.SiteName,
			"Restore database if not yet restored",
			"Verify site configuration (database credentials, domain, SSL)",
			"Test WordPress admin login",
		},
	}

	// Add issues for database restoration
	if dbRestorationIssue != nil {
		result.Issues = append(result.Issues, *dbRestorationIssue)
	}

	// Add issues for configuration
	if configIssue != nil {
		result.Issues = append(result.Issues, *configIssue)
	} else {
		result.Issues = append(result.Issues, ImportIssue{
			Severity: "info",
			Code:     "config_updated",
			Message:  "Configuration file updated with database credentials",
		})
	}

	return result, nil
}

// validateSiteCreation checks if a site can be created
func (e *Executor) validateSiteCreation(job *ImportJob) error {
	// Check if directory already exists
	if _, err := os.Stat(job.WebRoot); err == nil {
		return fmt.Errorf("site %q already exists", job.SiteName)
	}

	// Check parent directory is writable
	parentDir := filepath.Dir(job.WebRoot)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("cannot create parent directory: %w", err)
	}

	return nil
}

// extractArchive downloads and extracts the archive
func (e *Executor) extractArchive(ctx context.Context, url string, job *ImportJob, onProgress func(*ImportJob)) error {
	// Use secure fetcher with all validations
	body, err := e.fetcher.FetchArchive(ctx, url, maxArchiveSize)
	if err != nil {
		return err
	}
	defer body.Close()

	// Detect archive type
	archiveType := "tar.gz"
	if strings.HasSuffix(strings.ToLower(url), ".zip") {
		archiveType = "zip"
	}

	if archiveType == "tar.gz" {
		return e.extractTarGz(body, job, onProgress)
	}
	return e.extractZip(body, job, onProgress)
}

// safeTarExtractPath validates and sanitizes a tar entry path to prevent traversal
func safeTarExtractPath(webRoot, filename string) (string, error) {
	// Reject absolute paths and suspicious patterns
	if filepath.IsAbs(filename) {
		return "", errors.New("archive contains absolute path")
	}
	if strings.Contains(filename, "..") {
		return "", errors.New("archive contains .. path traversal")
	}
	if strings.HasPrefix(filename, "/") {
		return "", errors.New("archive path cannot start with /")
	}

	// Clean the path to remove any remaining issues
	cleaned := filepath.Clean(filename)
	targetPath := filepath.Join(webRoot, cleaned)

	// Verify the resolved path is still within webRoot
	realTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve path: %w", err)
	}

	realRoot, err := filepath.Abs(webRoot)
	if err != nil {
		return "", fmt.Errorf("cannot resolve root: %w", err)
	}

	// Use helper package's path validation for symlink safety
	if err := h.EnsureInside(realRoot, realTarget); err != nil {
		return "", fmt.Errorf("path validation failed: %w", err)
	}

	return targetPath, nil
}

// extractTarGz extracts a tar.gz archive with security checks
func (e *Executor) extractTarGz(reader io.Reader, job *ImportJob, onProgress func(*ImportJob)) error {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("not a valid gzip file: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	var totalDecompressed int64

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}

		// Archive bomb protection: check decompressed size
		totalDecompressed += header.Size
		if totalDecompressed > maxDecompressedSize {
			return fmt.Errorf("archive exceeds decompressed size limit (%d bytes)", maxDecompressedSize)
		}

		// Check total entries (files + directories) to prevent directory bombs
		job.EntriesProcessed++
		if job.EntriesProcessed > maxArchiveEntries {
			return fmt.Errorf("archive exceeds entry limit (%d total entries)", maxArchiveEntries)
		}

		// Check individual file size
		if header.Size > maxIndividualFileSize {
			return fmt.Errorf("file %s exceeds size limit (%d bytes)", header.Name, maxIndividualFileSize)
		}

		// Safely validate path
		targetPath, err := safeTarExtractPath(job.WebRoot, header.Name)
		if err != nil {
			// Log suspicious path but continue (skip this file)
			job.Message = fmt.Sprintf("Skipped suspicious path: %s (%v)", header.Name, err)
			continue
		}

		// Reject symlinks to prevent escape
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			job.Message = fmt.Sprintf("Rejected symlink/hardlink: %s", header.Name)
			continue
		}

		if header.Typeflag == tar.TypeDir {
			// Directory bomb protection: limit directory count
			job.DirectoriesCreated++
			if job.DirectoriesCreated > maxDirectoriesInArchive {
				return fmt.Errorf("archive exceeds directory limit (%d dirs)", maxDirectoriesInArchive)
			}
			os.MkdirAll(targetPath, os.FileMode(header.Mode&0755))
		} else {
			// Create parent directory
			os.MkdirAll(filepath.Dir(targetPath), 0755)

			// Extract file with limited size
			file, err := os.Create(targetPath)
			if err != nil {
				return fmt.Errorf("failed to create %s: %w", header.Name, err)
			}

			// Use LimitReader to prevent oversized files
			limitedReader := io.LimitReader(tr, maxIndividualFileSize)
			copied, err := io.Copy(file, limitedReader)
			file.Close()

			if err != nil && err != io.EOF {
				return fmt.Errorf("failed to write %s: %w", header.Name, err)
			}

			job.FilesExtracted++
			job.BytesExtracted += copied
			job.CurrentFile = header.Name
			job.Progress = 20 + int(job.FilesExtracted%40) // Show progress 20-60%
			job.UpdatedAt = time.Now()

			// Report progress periodically
			if job.FilesExtracted%100 == 0 {
				onProgress(job)
			}
		}
	}

	return nil
}

// extractZip extracts a zip archive with security checks
func (e *Executor) extractZip(reader io.Reader, job *ImportJob, onProgress func(*ImportJob)) error {
	// For zip files, we need random access, so save to temp file first
	tempFile, err := os.CreateTemp("", "import-*.zip")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tempFile.Name())

	if _, err := io.Copy(tempFile, reader); err != nil {
		tempFile.Close()
		return fmt.Errorf("failed to download archive: %w", err)
	}
	tempFile.Close()

	zr, err := zip.OpenReader(tempFile.Name())
	if err != nil {
		return fmt.Errorf("not a valid zip file: %w", err)
	}
	defer zr.Close()

	var totalDecompressed int64

	for _, file := range zr.File {
		// Archive bomb protection
		totalDecompressed += file.FileInfo().Size()
		if totalDecompressed > maxDecompressedSize {
			return fmt.Errorf("archive exceeds decompressed size limit (%d bytes)", maxDecompressedSize)
		}

		// Check total entry count (including directories)
		job.EntriesProcessed++
		if job.EntriesProcessed > maxArchiveEntries {
			return fmt.Errorf("archive exceeds entry limit (%d total entries)", maxArchiveEntries)
		}

		// Check individual file size
		if file.FileInfo().Size() > maxIndividualFileSize {
			return fmt.Errorf("file %s exceeds size limit (%d bytes)", file.Name, maxIndividualFileSize)
		}

		// Safely validate path
		targetPath, err := safeTarExtractPath(job.WebRoot, file.Name)
		if err != nil {
			job.Message = fmt.Sprintf("Skipped suspicious path: %s (%v)", file.Name, err)
			continue
		}

		if file.FileInfo().IsDir() {
			os.MkdirAll(targetPath, 0755)
		} else {
			os.MkdirAll(filepath.Dir(targetPath), 0755)

			srcFile, err := file.Open()
			if err != nil {
				return fmt.Errorf("failed to open %s in zip: %w", file.Name, err)
			}

			destFile, err := os.Create(targetPath)
			if err != nil {
				srcFile.Close()
				return fmt.Errorf("failed to create %s: %w", file.Name, err)
			}

			// Use LimitReader for security
			limitedReader := io.LimitReader(srcFile, maxIndividualFileSize)
			copied, err := io.Copy(destFile, limitedReader)
			destFile.Close()
			srcFile.Close()

			if err != nil {
				return fmt.Errorf("failed to write %s: %w", file.Name, err)
			}

			job.FilesExtracted++
			job.BytesExtracted += copied
			job.CurrentFile = file.Name
			job.Progress = 20 + int(job.FilesExtracted%40)
			job.UpdatedAt = time.Now()

			if job.FilesExtracted%100 == 0 {
				onProgress(job)
			}
		}
	}

	return nil
}

// findDatabaseDump looks for SQL dump files in the extracted archive using proper recursion
func (e *Executor) findDatabaseDump(webRoot string) string {
	var found string
	err := filepath.WalkDir(webRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return err
		}
		if !d.IsDir() {
			name := d.Name()
			// Check for common database dump names
			if strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".sql.gz") {
				// Prioritize specific names
				if name == "backup.sql" || name == "database.sql" || name == "db.sql" {
					found = path
					return filepath.SkipDir
				}
				if found == "" {
					found = path
				}
			}
		}
		return nil
	})
	if err != nil {
		return ""
	}
	return found
}

// restoreDatabase restores a SQL dump using mysql/mariadb client
func (e *Executor) restoreDatabase(ctx context.Context, sqlFile, dbName, dbUser string) error {
	// Validate file exists and is readable
	info, err := os.Stat(sqlFile)
	if err != nil {
		return fmt.Errorf("database file not found: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("database file is a directory")
	}

	// Note: Full database restoration requires MySQL credentials and connection.
	// This is a stub that validates the SQL file exists.
	// Phase 2.5 should implement actual MySQL restoration via:
	// 1. Require admin credentials in request
	// 2. Create database if not exists
	// 3. Execute: mysql -u user -p db < sqlFile
	// 4. Verify restoration with simple query

	// For now, log that restoration would happen
	// The operator must manually restore the database for now
	return fmt.Errorf("database restoration not yet implemented - manual restore required for %q", dbName)
}

// extractDatabaseInfo extracts database name/user from config file
func (e *Executor) extractDatabaseInfo(configFile string) (dbName, dbUser string) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return "", ""
	}

	content := string(data)
	dbName = extractWordPressDefine(content, "DB_NAME")
	dbUser = extractWordPressDefine(content, "DB_USER")

	return dbName, dbUser
}

// updateConfiguration updates config files with correct credentials
func (e *Executor) updateConfiguration(job *ImportJob, configPath string) error {
	configFile := filepath.Join(job.WebRoot, configPath)

	// Read the config file
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("cannot read config file: %w", err)
	}

	content := string(data)
	modified := false

	// Replace database name, user, and create password placeholder
	updates := map[string]string{
		"DB_NAME": job.DatabaseName,
		"DB_USER": job.DatabaseUser,
		// DB_PASSWORD should be set separately by admin with actual password
		// Do NOT set a placeholder password
	}

	for key, value := range updates {
		if value != "" {
			// Replace define('KEY', 'old_value') with define('KEY', 'new_value')
			// This is a simple text replacement; a proper parser would be better
			for _, quote := range []string{"'", "\""} {
				pattern := fmt.Sprintf("define(%s%s%s", quote, key, quote)
				if strings.Contains(content, pattern) {
					// Find and replace the value for this key
					content = replaceDefineValue(content, key, quote, value)
					modified = true
				}
			}
		}
	}

	if modified {
		return os.WriteFile(configFile, []byte(content), 0644)
	}

	return nil
}

// replaceDefineValue replaces the value of a define() statement
func replaceDefineValue(content, key, quote, newValue string) string {
	pattern := fmt.Sprintf("define(%s%s%s,", quote, key, quote)
	parts := strings.Split(content, pattern)
	if len(parts) != 2 {
		return content
	}

	afterComma := parts[1]

	// Find the next quote (start of value)
	idx := strings.IndexAny(afterComma, "'\"\n")
	if idx < 0 || afterComma[idx] == '\n' {
		return content
	}

	valueQuote := afterComma[idx : idx+1]

	// Find closing quote
	closeIdx := strings.Index(afterComma[idx+1:], valueQuote)
	if closeIdx < 0 {
		return content
	}

	// Reconstruct with new value
	return parts[0] + pattern + afterComma[:idx] + valueQuote + newValue + valueQuote + afterComma[idx+1+closeIdx:]
}

// extractWordPressDefine extracts a WordPress define value
// Handles: define('KEY', 'value') or define("KEY", "value")
func extractWordPressDefine(content, key string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		// Match define('KEY'... or define("KEY"...
		for _, quote := range []string{"'", "\""} {
			pattern := fmt.Sprintf("define(%s%s%s", quote, key, quote)
			if !strings.Contains(line, pattern) {
				continue
			}

			// Find where the pattern starts
			idx := strings.Index(line, pattern)
			if idx < 0 {
				continue
			}

			// Move past "define('KEY'" to find the comma
			afterKey := idx + len(pattern)
			rest := line[afterKey:]

			// Find the comma separator
			commaIdx := strings.Index(rest, ",")
			if commaIdx < 0 {
				continue
			}

			// Move past the comma and whitespace to find the value
			afterComma := rest[commaIdx+1:]
			afterComma = strings.TrimSpace(afterComma)

			// Extract the value (first character should be a quote)
			if len(afterComma) == 0 {
				continue
			}

			valueQuote := afterComma[0:1]
			if valueQuote != "'" && valueQuote != "\"" {
				continue
			}

			// Find the closing quote
			valueStart := 1
			valueEnd := strings.Index(afterComma[valueStart:], valueQuote)
			if valueEnd < 0 {
				continue
			}

			return afterComma[valueStart : valueStart+valueEnd]
		}
	}
	return ""
}

// generateJobID creates a unique job ID
func generateJobID() string {
	return fmt.Sprintf("import-%d", time.Now().UnixNano())
}
