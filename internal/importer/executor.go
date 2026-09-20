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
	maxArchiveSize        = 5 * 1024 * 1024 * 1024  // 5GB
	maxDecompressedSize   = 50 * 1024 * 1024 * 1024 // 50GB (archive bomb protection)
	maxFilesInArchive     = 1000000
	maxIndividualFileSize = 10 * 1024 * 1024 * 1024 // 10GB per file
)

// Executor performs actual archive import (extraction, DB restore, etc.)
type Executor struct {
	httpClient *http.Client
}

// NewExecutor creates a new import executor
func NewExecutor() *Executor {
	return &Executor{
		httpClient: &http.Client{Timeout: 30 * time.Second},
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

	sqlFile := e.findDatabaseDump(job.WebRoot)
	if sqlFile != "" {
		job.Message = fmt.Sprintf("Restoring database from %s...", filepath.Base(sqlFile))
		job.UpdatedAt = time.Now()
		onProgress(job)

		if err := e.restoreDatabase(ctx, sqlFile, dbName, dbUser); err != nil {
			// Database restore failed, but don't fail entire import
			job.Message = fmt.Sprintf("Warning: database restore failed: %v", err)
		}
	} else {
		job.Message = "No database dump found; database will need to be restored separately"
	}

	// Step 6: Update config files with correct database credentials
	job.Status = "finalizing"
	job.Progress = 90
	job.Message = "Updating configuration..."
	job.UpdatedAt = time.Now()
	onProgress(job)

	if err := e.updateConfiguration(job, req.ConfigPath); err != nil {
		// Configuration update failed, but don't fail entire import
		job.Message = fmt.Sprintf("Warning: configuration update failed: %v", err)
	}

	// Step 7: Mark complete
	job.Status = "done"
	job.Progress = 100
	job.Message = "Import complete"
	job.UpdatedAt = time.Now()
	onProgress(job)

	result := &ImportResult{
		JobID:         job.ID,
		Success:       true,
		SiteName:      job.SiteName,
		CreatedAt:     job.StartedAt,
		FilesImported: job.FilesExtracted,
		StorageSize:   job.BytesExtracted,
		Issues: []ImportIssue{
			{
				Severity: "info",
				Code:     "import_complete",
				Message:  "Site import completed successfully",
			},
		},
		NextSteps: []string{
			"Verify site loads at https://panel.example.com/site/" + req.SiteName,
			"Check site content and database are intact",
			"Update site configuration if needed (domain, SSL certificate)",
			"Test WordPress admin login if applicable",
		},
	}

	if sqlFile == "" {
		result.Issues = append(result.Issues, ImportIssue{
			Severity: "warning",
			Code:     "no_database_restored",
			Message:  "No database dump was found in archive. Create database manually or upload dump separately.",
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
	resp, err := e.httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("failed to download archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("archive download returned %d", resp.StatusCode)
	}

	// Detect archive type
	archiveType := "tar.gz"
	if strings.HasSuffix(strings.ToLower(url), ".zip") {
		archiveType = "zip"
	}

	if archiveType == "tar.gz" {
		return e.extractTarGz(resp.Body, job, onProgress)
	}
	return e.extractZip(resp.Body, job, onProgress)
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

		// Check file count
		if job.FilesExtracted >= maxFilesInArchive {
			return fmt.Errorf("archive exceeds file limit (%d files)", maxFilesInArchive)
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

		// Check file count
		if job.FilesExtracted >= maxFilesInArchive {
			return fmt.Errorf("archive exceeds file limit (%d files)", maxFilesInArchive)
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

// findDatabaseDump looks for SQL dump files in the extracted archive
func (e *Executor) findDatabaseDump(webRoot string) string {
	patterns := []string{"*.sql", "*.sql.gz", "backup.sql", "database.sql", "db.sql"}

	for _, pattern := range patterns {
		matches, _ := filepath.Glob(filepath.Join(webRoot, "**", pattern))
		if len(matches) > 0 {
			return matches[0]
		}
	}

	return ""
}

// restoreDatabase restores a SQL dump (placeholder for Phase 2)
func (e *Executor) restoreDatabase(ctx context.Context, sqlFile, dbName, dbUser string) error {
	// TODO: Phase 2.5 - implement actual database restoration
	// For now, just validate the file exists and is readable
	if _, err := os.Stat(sqlFile); err != nil {
		return fmt.Errorf("database file not found: %w", err)
	}
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

// updateConfiguration updates config files with correct credentials (placeholder)
func (e *Executor) updateConfiguration(job *ImportJob, configPath string) error {
	// TODO: Phase 2.5 - update wp-config.php with correct database info
	// For now, this is a placeholder
	return nil
}

// extractWordPressDefine extracts a WordPress define value
func extractWordPressDefine(content, key string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.Contains(line, fmt.Sprintf("define('%s'", key)) || strings.Contains(line, fmt.Sprintf("define(\"%s\"", key)) {
			start := strings.Index(line, "'") + 1
			if start == 0 {
				start = strings.Index(line, "\"") + 1
			}
			if start > 0 {
				rest := line[start:]
				end := strings.Index(rest, "'")
				if end < 0 {
					end = strings.Index(rest, "\"")
				}
				if end > 0 {
					return rest[:end]
				}
			}
		}
	}
	return ""
}

// generateJobID creates a unique job ID
func generateJobID() string {
	return fmt.Sprintf("import-%d", time.Now().UnixNano())
}
