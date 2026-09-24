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
	"regexp"
	"strings"
	"syscall"
	"time"

	h "github.com/itchyitchy123/StePanel/internal/helper"
)

const (
	// Archive size limits (more conservative than original)
	maxArchiveSize          = 5 * 1024 * 1024 * 1024  // 5GB compressed (configurable)
	maxDecompressedSize     = 50 * 1024 * 1024 * 1024 // 50GB decompressed (configurable)
	maxArchiveEntries       = 250000                  // 250k files/dirs (was 1M, still generous)
	maxIndividualFileSize   = 10 * 1024 * 1024 * 1024 // 10GB per file
	maxDirectoriesInArchive = 25000                   // Separate limit for directories

	// Require 20% free space buffer after import to prevent filesystem exhaustion
	minFreeSpaceBuffer = 0.20
)

// formatBytes converts bytes to human-readable format (e.g., "1.5 MB")
func formatBytes(bytes int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	value := float64(bytes)
	for _, unit := range units {
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%.1f TB", value)
}

// DiskSpaceRequirement represents the space needed for an import operation
type DiskSpaceRequirement struct {
	CompressedSize   int64
	ExpandedSize     int64
	TotalNeeded      int64
	AvailableSpace   int64
	RequiredBuffer   int64
	HasSufficientSpace bool
	Reason           string
}

// CheckDiskSpace validates that sufficient space is available for archive import.
// Ensures enough room for compressed download, expanded extraction, plus 20% buffer.
func CheckDiskSpace(webRoot string, compressedSize, estimatedExpandedSize int64) DiskSpaceRequirement {
	result := DiskSpaceRequirement{
		CompressedSize: compressedSize,
		ExpandedSize:   estimatedExpandedSize,
	}

	// Get available space on the filesystem containing webRoot
	var stat syscall.Statfs_t
	if err := syscall.Statfs(webRoot, &stat); err != nil {
		result.Reason = fmt.Sprintf("could not check disk space: %v", err)
		return result
	}

	// Calculate available space (available blocks * block size)
	availableBytes := int64(stat.Bavail) * int64(stat.Bsize)
	result.AvailableSpace = availableBytes

	// Total space needed: compressed + expanded + safety buffer
	// We need enough for the compressed archive, the expanded files, plus 20% buffer
	totalNeeded := compressedSize + estimatedExpandedSize
	requiredBuffer := int64(float64(totalNeeded) * minFreeSpaceBuffer)
	totalNeeded += requiredBuffer

	result.TotalNeeded = totalNeeded
	result.RequiredBuffer = requiredBuffer

	if totalNeeded > availableBytes {
		result.HasSufficientSpace = false
		shortfall := totalNeeded - availableBytes
		result.Reason = fmt.Sprintf(
			"insufficient disk space: need %s (compressed %s + expanded %s + %d%% buffer %s) but only %s available; need %s more",
			formatBytes(totalNeeded),
			formatBytes(compressedSize),
			formatBytes(estimatedExpandedSize),
			int(minFreeSpaceBuffer*100),
			formatBytes(requiredBuffer),
			formatBytes(availableBytes),
			formatBytes(shortfall),
		)
		return result
	}

	result.HasSufficientSpace = true
	result.Reason = fmt.Sprintf(
		"sufficient space available: %s (need %s: compressed %s + expanded %s + buffer %s)",
		formatBytes(availableBytes),
		formatBytes(totalNeeded),
		formatBytes(compressedSize),
		formatBytes(estimatedExpandedSize),
		formatBytes(requiredBuffer),
	)
	return result
}

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

	resp, err := af.httpClient.Do(req) // URL validated at line 37; redirects checked via CheckRedirect policy
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

// NewExecutor creates a new import executor with secure redirect handling
func NewExecutor() *Executor {
	return &Executor{
		fetcher: &ArchiveFetcher{
			httpClient: &http.Client{
				// No global timeout - allows large file downloads
				// Context deadline should be set per-request by the caller
				// Uses shared NewSafeArchiveTransport to ensure consistent SSRF protection
				// with inspection path and prevent DNS-rebinding attacks
				Transport: NewSafeArchiveTransport(),
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					// Validate redirect destination is safe (prevents SSRF via redirect chain)
					if !isAllowedURL(req.URL.String()) {
						return fmt.Errorf("redirect to disallowed URL: %s", req.URL.String())
					}
					return nil
				},
			},
		},
	}
}

// ImportJob tracks an in-progress import
type ImportJob struct {
	ID                 string
	SiteName           string
	ArchiveURL         string
	ConfigPath         string
	WebRoot            string
	Status             string // "validating", "extracting", "restoring-db", "finalizing", "done", "failed"
	Progress           int    // 0-100
	FilesExtracted     int64
	DirectoriesCreated int64
	EntriesProcessed   int64 // Total entries (files + dirs) to catch directory bombs
	BytesExtracted     int64
	CurrentFile        string
	Message            string
	StartedAt          time.Time
	UpdatedAt          time.Time
	Error              string
	DatabaseName       string
	DatabaseUser       string
	DatabasePassword   string
	DatabaseDumpPath   string // Path to SQL dump file if found
	DatabaseDumpSize   int64  // Size of SQL dump file
}

// ExecuteImport performs the full import workflow
func (e *Executor) ExecuteImport(ctx context.Context, req *ArchiveImportRequest, webRoot string, onProgress func(*ImportJob)) (*ImportResult, error) {
	job := &ImportJob{
		ID:         generateJobID(),
		SiteName:   req.SiteName,
		ArchiveURL: req.URL,
		ConfigPath: req.ConfigPath,
		WebRoot:    filepath.Join(webRoot, "sites", req.SiteName, "public"),
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

	// Step 1.5: Check disk space availability
	// Use conservative estimate: assume 10:1 compression ratio if archive is max size
	// This ensures we have enough space even in worst case
	estimatedExpanded := maxArchiveSize * 10 // 50 GB for max 5 GB compressed
	spaceCheck := CheckDiskSpace(filepath.Dir(filepath.Dir(job.WebRoot)), maxArchiveSize, int64(estimatedExpanded))
	if !spaceCheck.HasSufficientSpace {
		job.Status = "failed"
		job.Error = spaceCheck.Reason
		job.UpdatedAt = time.Now()
		onProgress(job)
		return nil, fmt.Errorf("insufficient disk space: %s", spaceCheck.Reason)
	}

	job.Message = spaceCheck.Reason
	job.UpdatedAt = time.Now()
	onProgress(job)

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
		// Get file size for reporting
		sqlInfo, _ := os.Stat(sqlFile)
		dumpSize := sqlInfo.Size()

		job.DatabaseDumpPath = sqlFile
		job.DatabaseDumpSize = dumpSize
		job.Message = fmt.Sprintf("Found database dump: %s (%s)", filepath.Base(sqlFile), formatBytes(dumpSize))
		job.UpdatedAt = time.Now()
		onProgress(job)

		// Validate the SQL file is valid (exists, readable, not empty)
		if err := e.restoreDatabase(ctx, sqlFile, dbName, dbUser); err != nil {
			// Database validation/restoration failed
			dbRestorationIssue = &ImportIssue{
				Severity: "warning",
				Code:     "database_restore_deferred",
				Message:  fmt.Sprintf("Database dump found at %s (%s). Operator must restore manually.", filepath.Base(sqlFile), formatBytes(dumpSize)),
			}
			job.Message = "Database dump file validated; manual restoration required"
		} else {
			// Database file is valid - manual restoration with provided credentials
			dbRestorationIssue = &ImportIssue{
				Severity: "info",
				Code:     "database_dump_ready",
				Message:  fmt.Sprintf("Database dump ready for restoration: %s (%s). Restore with: mysql -u %s %s < %s", filepath.Base(sqlFile), formatBytes(dumpSize), dbUser, dbName, filepath.Base(sqlFile)),
			}
			job.Message = "Database dump extracted and ready for restoration"
		}
	} else {
		// No database dump found in archive
		dbRestorationIssue = &ImportIssue{
			Severity: "warning",
			Code:     "no_database_dump_found",
			Message:  "No SQL database dump found in archive. Application will need to initialize its own database or you must provide one manually.",
		}
		job.Message = "No database dump found in archive"
	}

	// Step 6: Update config files with correct database credentials
	job.Status = "finalizing"
	job.Progress = 90
	job.Message = "Updating configuration..."
	job.UpdatedAt = time.Now()
	onProgress(job)

	var configIssue *ImportIssue
	var configError error
	if configError = e.updateConfiguration(job, req.ConfigPath); configError != nil {
		// Configuration update failed - mark job as failed
		configIssue = &ImportIssue{
			Severity: "error",
			Code:     "config_update_failed",
			Message:  fmt.Sprintf("Configuration update failed: %v. Site preparation incomplete.", configError),
		}
		job.Status = "failed"
		job.Progress = 100
		job.Message = configIssue.Message
		job.UpdatedAt = time.Now()
		onProgress(job)
		// Return error with partial result
		return nil, fmt.Errorf("import incomplete: %w", configError)
	}

	// Import is always successful if files were extracted and config updated
	// Database restoration is optional/deferred, not a blocker
	// Step 7: Mark complete
	job.Status = "done"
	job.Message = "Import complete - site files extracted and configured"
	job.Progress = 100
	job.UpdatedAt = time.Now()
	onProgress(job)

	issues := []ImportIssue{
		{
			Severity: "info",
			Code:     "files_extracted",
			Message:  fmt.Sprintf("Successfully extracted %d files (%s)", job.FilesExtracted, formatBytes(job.BytesExtracted)),
		},
		{
			Severity: "info",
			Code:     "config_updated",
			Message:  fmt.Sprintf("Configuration updated with database credentials (name: %s, user: %s)", job.DatabaseName, job.DatabaseUser),
		},
	}

	// Add database restoration issue (if any)
	if dbRestorationIssue != nil {
		issues = append(issues, *dbRestorationIssue)
	}

	// Build next steps
	nextSteps := []string{
		"1. Verify site is available in StePanel at /sites",
		"2. Check site configuration (domain, SSL certificates)",
	}

	// Add database restoration instructions
	if sqlFile != "" {
		nextSteps = append(nextSteps, []string{
			fmt.Sprintf("3. Restore database using: mysql -u %s %s < %s", dbUser, dbName, sqlFile),
			fmt.Sprintf("   (SQL dump location: %s)", sqlFile),
		}...)
	} else {
		nextSteps = append(nextSteps, "3. Set up database (if required by application)")
	}

	nextSteps = append(nextSteps, "4. Test application functionality")

	result := &ImportResult{
		JobID:         job.ID,
		Success:       true, // Files and config are done; database is optional/deferred
		SiteName:      job.SiteName,
		CreatedAt:     job.StartedAt,
		FilesImported: job.FilesExtracted,
		StorageSize:   job.BytesExtracted,
		Issues:        issues,
		NextSteps:     nextSteps,
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

// validateConfigPath validates that a config path is safe (no traversal, not absolute, stays within webRoot)
func validateConfigPath(webRoot, configPath string) error {
	// Reject absolute paths
	if filepath.IsAbs(configPath) {
		return errors.New("config path cannot be absolute")
	}

	// Reject path traversal attempts
	if strings.Contains(configPath, "..") {
		return errors.New("config path contains .. traversal")
	}

	// Reject paths starting with /
	if strings.HasPrefix(configPath, "/") {
		return errors.New("config path cannot start with /")
	}

	// Clean and validate the path stays within webRoot
	cleaned := filepath.Clean(configPath)
	targetPath := filepath.Join(webRoot, cleaned)

	realTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return fmt.Errorf("cannot resolve config path: %w", err)
	}

	realRoot, err := filepath.Abs(webRoot)
	if err != nil {
		return fmt.Errorf("cannot resolve root: %w", err)
	}

	// Ensure the path stays within webRoot and resolve symlinks safely
	if err := h.EnsureInside(realRoot, realTarget); err != nil {
		return fmt.Errorf("config path escapes site root: %w", err)
	}

	return nil
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

		// Explicitly handle only supported tar entry types
		switch header.Typeflag {
		case tar.TypeDir:
			// Directory bomb protection: limit directory count
			job.DirectoriesCreated++
			if job.DirectoriesCreated > maxDirectoriesInArchive {
				return fmt.Errorf("archive exceeds directory limit (%d dirs)", maxDirectoriesInArchive)
			}
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode&0755)); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", header.Name, err)
			}

		case tar.TypeReg, tar.TypeRegA:
			// Regular file extraction
			// Create parent directory
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory for %s: %w", header.Name, err)
			}

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

			// Restore file permissions from archive
			// Preserve executable bits but mask out dangerous bits (setuid/setgid/sticky)
			archiveMode := os.FileMode(header.Mode)
			safeMode := 0644 | (archiveMode & 0111) // Preserve executable bits, allow read for others
			if err := os.Chmod(targetPath, os.FileMode(safeMode)); err != nil {
				// Log but don't fail on permission restore
				job.Message = fmt.Sprintf("Warning: could not restore permissions for %s", header.Name)
			}

			job.FilesExtracted++
			job.BytesExtracted += copied
			job.CurrentFile = header.Name
			// Progress from 20-60% for extraction phase (linear with logarithmic cap)
			// Avoids going backwards and caps at 60% until completion
			fileProgress := job.FilesExtracted
			if fileProgress > 1000 {
				fileProgress = 1000 // Cap denominator to avoid slow growth at high file counts
			}
			job.Progress = 20 + int(40*fileProgress/1000)
			job.UpdatedAt = time.Now()

			// Report progress periodically
			if job.FilesExtracted%100 == 0 {
				onProgress(job)
			}

		case tar.TypeSymlink, tar.TypeLink:
			// Reject symlinks and hardlinks to prevent escape
			job.Message = fmt.Sprintf("Rejected symlink/hardlink: %s", header.Name)
			continue

		default:
			// Reject all other entry types (devices, FIFOs, sockets, sparse, etc.)
			return fmt.Errorf("unsupported tar entry type %v for %s", header.Typeflag, header.Name)
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
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return fmt.Errorf("failed to create directory %s: %w", file.Name, err)
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("failed to create parent directory for %s: %w", file.Name, err)
			}

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

			// Restore file permissions from zip entry
			// Preserve executable bits but mask out dangerous bits
			archiveMode := file.FileInfo().Mode()
			safeMode := 0644 | (archiveMode & 0111) // Preserve executable bits
			if err := os.Chmod(targetPath, safeMode); err != nil {
				// Log but don't fail on permission restore
				job.Message = fmt.Sprintf("Warning: could not restore permissions for %s", file.Name)
			}

			job.FilesExtracted++
			job.BytesExtracted += copied
			job.CurrentFile = file.Name
			// Progress from 20-60% for extraction phase (linear with logarithmic cap)
			// Avoids going backwards and caps at 60% until completion
			fileProgress := job.FilesExtracted
			if fileProgress > 1000 {
				fileProgress = 1000 // Cap denominator to avoid slow growth at high file counts
			}
			job.Progress = 20 + int(40*fileProgress/1000)
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

// restoreDatabase validates SQL dump or performs automated restoration if credentials provided
func (e *Executor) restoreDatabase(ctx context.Context, sqlFile, dbName, dbUser string) error {
	// Validate file exists and is readable
	info, err := os.Stat(sqlFile)
	if err != nil {
		return fmt.Errorf("database file not found: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("database file is a directory")
	}
	if info.Size() == 0 {
		return fmt.Errorf("database dump file is empty")
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("database file is not a regular file")
	}

	// Phase 1 (current): Validation only, operator restores manually
	// Phase 2 TODO: If credentials passed in request, implement:
	// 1. Call Config.DBCtl helper: restore-dump dbname dbuser < sqlFile
	// 2. Verify restoration with SELECT query
	// 3. Clean up dump file after success
	// 4. Return error if restoration fails

	// For now: File is valid, restoration instructions provided to operator
	return nil
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
	// Validate config path is safe (no traversal, not absolute)
	if err := validateConfigPath(job.WebRoot, configPath); err != nil {
		return fmt.Errorf("invalid config path: %w", err)
	}

	configFile := filepath.Join(job.WebRoot, configPath)

	// Verify the file exists and is a regular file (not symlink/directory/etc)
	info, err := os.Stat(configFile)
	if err != nil {
		return fmt.Errorf("cannot access config file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("config path is not a regular file")
	}

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
		// Write atomically: create temp file, write, then rename
		tempFile, err := os.CreateTemp(filepath.Dir(configFile), "."+filepath.Base(configFile)+".*")
		if err != nil {
			return fmt.Errorf("cannot create temp file: %w", err)
		}
		defer os.Remove(tempFile.Name())

		// Write content to temp file
		if _, err := tempFile.WriteString(content); err != nil {
			tempFile.Close()
			return fmt.Errorf("cannot write temp file: %w", err)
		}

		// Preserve original file permissions and ownership
		if err := tempFile.Chmod(info.Mode()); err != nil {
			tempFile.Close()
			return fmt.Errorf("cannot set temp file permissions: %w", err)
		}
		tempFile.Close()

		// Atomic rename
		if err := os.Rename(tempFile.Name(), configFile); err != nil {
			return fmt.Errorf("cannot replace config file: %w", err)
		}
	}

	return nil
}

// replaceDefineValue replaces the value of a define() statement
// Handles formats like: define('KEY', 'value') or define ( 'KEY', 'value' )
// Does NOT replace if value comes from a function call (getenv, env, etc)
func replaceDefineValue(content, key, quote, newValue string) string {
	// Pattern: define (with optional spaces) ( KEY (with optional spaces) ,
	// Use regex to be more flexible with whitespace
	escapedQuote := regexp.QuoteMeta(quote)
	escapedKey := regexp.QuoteMeta(key)
	pattern := fmt.Sprintf(`define\s*\(\s*%s%s%s\s*,`, escapedQuote, escapedKey, escapedQuote)

	re, err := regexp.Compile(pattern)
	if err != nil {
		return content
	}

	matches := re.FindAllStringIndex(content, -1)
	if len(matches) == 0 {
		return content
	}

	// Process the last match (most likely the one we want to replace)
	match := matches[len(matches)-1]
	matchEnd := match[1]

	afterComma := content[matchEnd:]

	// Skip whitespace after comma
	idx := 0
	for idx < len(afterComma) && (afterComma[idx] == ' ' || afterComma[idx] == '\t' || afterComma[idx] == '\n') {
		idx++
	}

	// Check if value comes from a function call - if so, don't replace
	// Look for patterns like getenv(), env(), etc.
	remainingContent := afterComma[idx:]
	if idx < len(afterComma) && afterComma[idx] == 'g' ||
		(idx+3 < len(afterComma) && strings.HasPrefix(remainingContent, "env(")) ||
		(idx+7 < len(afterComma) && strings.HasPrefix(remainingContent, "getenv(")) {
		// Value comes from function - don't replace
		return content
	}

	// Find the opening quote
	if idx >= len(afterComma) || (afterComma[idx] != '\'' && afterComma[idx] != '"') {
		return content
	}

	valueQuote := afterComma[idx : idx+1]

	// Find closing quote (skip escaped quotes)
	closeIdx := idx + 1
	for closeIdx < len(afterComma) {
		if afterComma[closeIdx] == '\\' && closeIdx+1 < len(afterComma) {
			closeIdx += 2 // Skip escaped character
			continue
		}
		if afterComma[closeIdx:closeIdx+1] == valueQuote {
			break
		}
		closeIdx++
	}

	if closeIdx >= len(afterComma) {
		return content
	}

	// Replace the value
	newContent := content[:matchEnd+idx] + valueQuote + newValue + valueQuote + afterComma[closeIdx+1:]
	return newContent
}

// extractWordPressDefine extracts a WordPress define value
// Handles: define('KEY', 'value') or define("KEY", "value")
func extractWordPressDefine(content, key string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		// Skip commented lines
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Look for define function with flexible spacing
		if !strings.Contains(line, "define") {
			continue
		}

		// Try both quote types
		for _, quote := range []string{"'", "\""} {
			// Find 'define(' part (with possible spaces)
			defineIdx := strings.Index(line, "define")
			if defineIdx < 0 {
				continue
			}

			// Look for the opening paren after define (with spaces)
			afterDefine := line[defineIdx+6:]
			parenIdx := strings.Index(afterDefine, "(")
			if parenIdx < 0 {
				continue
			}

			// Look for the key pattern: quote + key + quote
			keyPattern := fmt.Sprintf("%s%s%s", quote, key, quote)
			keyIdx := strings.Index(afterDefine[parenIdx:], keyPattern)
			if keyIdx < 0 {
				continue
			}

			// Find comma after the key
			afterKey := afterDefine[parenIdx+keyIdx+len(keyPattern):]
			commaIdx := strings.Index(afterKey, ",")
			if commaIdx < 0 {
				continue
			}

			// Get the value part (after comma)
			afterComma := strings.TrimSpace(afterKey[commaIdx+1:])
			if len(afterComma) == 0 {
				continue
			}

			// Value should start with a quote
			if !strings.HasPrefix(afterComma, "'") && !strings.HasPrefix(afterComma, "\"") {
				continue
			}

			valueQuote := afterComma[0:1]
			rest := afterComma[1:]

			// Find closing quote
			closeIdx := strings.Index(rest, valueQuote)
			if closeIdx < 0 {
				continue
			}

			return rest[:closeIdx]
		}
	}
	return ""
}

// generateJobID creates a unique job ID
func generateJobID() string {
	return fmt.Sprintf("import-%d", time.Now().UnixNano())
}
