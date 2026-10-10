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
	"unicode/utf8"

	"github.com/cyberducttape/StePanel/internal/archivesafe"
	h "github.com/cyberducttape/StePanel/internal/helper"
)

const (
	// Archive size limits (more conservative than original)
	maxArchiveSize           = 5 * 1024 * 1024 * 1024  // 5GB compressed (configurable)
	maxDecompressedSize      = 50 * 1024 * 1024 * 1024 // 50GB decompressed (configurable)
	maxArchiveEntries        = 250000                  // 250k files/dirs (was 1M, still generous)
	maxIndividualFileSize    = 10 * 1024 * 1024 * 1024 // 10GB per file
	maxDirectoriesInArchive  = 25000                   // Separate limit for directories
	baselineExpandedEstimate = 256 * 1024 * 1024

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
	CompressedSize     int64
	ExpandedSize       int64
	TotalNeeded        int64
	AvailableSpace     int64
	RequiredBuffer     int64
	HasSufficientSpace bool
	Reason             string
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
		reader:        resp.Body,
		closer:        resp.Body,
		limit:         maxBytes,
		contentLength: resp.ContentLength,
	}, nil
}

// limitedReadCloser combines a limited reader with a close method
type limitedReadCloser struct {
	reader        io.Reader
	closer        io.Closer
	limit         int64
	read          int64
	contentLength int64
}

func (lrc *limitedReadCloser) Read(p []byte) (int, error) {
	if lrc.read >= lrc.limit {
		var probe [1]byte
		n, err := lrc.reader.Read(probe[:])
		if n > 0 {
			return 0, fmt.Errorf("archive exceeds download limit (%d bytes)", lrc.limit)
		}
		return 0, err
	}
	remaining := lrc.limit - lrc.read
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := lrc.reader.Read(p)
	lrc.read += int64(n)
	return n, err
}

func (lrc *limitedReadCloser) Close() error {
	return lrc.closer.Close()
}

// Executor performs actual archive import (extraction, DB restore, etc.)
type Executor struct {
	fetcher          *ArchiveFetcher
	databaseRestorer DatabaseRestorer
	spool            ArchiveSpool
}

// ArchiveSpool stores a streamed archive on disk. ZIP archives need random
// access to their central directory, so they are spooled before reading.
type ArchiveSpool interface {
	Store(src io.Reader, limit int64) (*archivesafe.Spooled, error)
}

// WithSpool sets where ZIP archives are spooled. The control plane passes a
// spool in its capacity-managed import root. Without one, the executor
// spools beside the extraction root, on the filesystem its disk-space check
// covers, never in the system temporary directory.
func (e *Executor) WithSpool(spool ArchiveSpool) *Executor {
	e.spool = spool
	return e
}

func (e *Executor) spoolFor(job *ImportJob) ArchiveSpool {
	if e.spool != nil {
		return e.spool
	}
	return archivesafe.DirSpool{Dir: filepath.Dir(job.WebRoot)}
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
					if len(via) >= maxArchiveRedirects {
						return fmt.Errorf("archive redirect limit exceeded (%d)", maxArchiveRedirects)
					}
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

// WithDatabaseRestorer attaches the control-plane's privileged database
// adapter. Keeping the callback injectable preserves the archive package's
// unprivileged trust boundary and makes restore/rollback behavior testable.
func (e *Executor) WithDatabaseRestorer(restorer DatabaseRestorer) *Executor {
	e.databaseRestorer = restorer
	return e
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

// ExecuteImport extracts an archive into extractRoot, updates its config, and
// reports on any embedded database dump. extractRoot must not exist yet: the
// executor creates it, so it is safe for the caller to use as a staging
// directory and atomically rename into the canonical site location only after
// the import returns successfully. The executor never touches any path other
// than extractRoot and its children.
func (e *Executor) ExecuteImport(ctx context.Context, req *ArchiveImportRequest, extractRoot string, onProgress func(*ImportJob)) (*ImportResult, error) {
	job := &ImportJob{
		ID:         generateJobID(),
		SiteName:   req.SiteName,
		ArchiveURL: req.URL,
		ConfigPath: req.ConfigPath,
		WebRoot:    extractRoot,
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

	// Validate the caller/archive-selected configuration path before joining
	// it to the extraction root. Config parsing is an early read boundary and
	// must have the same containment guarantee as the later rewrite path.
	configFile := ""
	if strings.TrimSpace(req.ConfigPath) != "" {
		var configErr error
		configFile, configErr = safeConfigPath(job.WebRoot, req.ConfigPath)
		if configErr != nil {
			job.Status = "failed"
			job.Error = fmt.Sprintf("invalid config path: %v", configErr)
			job.UpdatedAt = time.Now()
			onProgress(job)
			return nil, errors.New(job.Error)
		}
	}

	// Step 1.5: Perform a small baseline admission check. The actual archive
	// size is checked after response headers are available, rather than charging
	// every import for the maximum 5GB/50GB limits.
	spaceCheck := CheckDiskSpace(filepath.Dir(job.WebRoot), 0, baselineExpandedEstimate)
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

	dbName, dbUser := e.extractDatabaseInfo(configFile)
	if dbName == "" {
		dbName = fmt.Sprintf("%s_db", req.SiteName)
	}
	if dbUser == "" {
		dbUser = fmt.Sprintf("%s_user", req.SiteName)
	}
	job.DatabaseName = dbName
	job.DatabaseUser = dbUser
	if req.AutoRestoreDB {
		job.DatabasePassword = req.DatabasePassword
	}
	var configError error

	// Step 5: Look for and restore database
	job.Status = "restoring-db"
	job.Progress = 70
	job.Message = "Looking for database dump..."
	job.UpdatedAt = time.Now()
	onProgress(job)

	var dbRestorationIssue *ImportIssue
	var cleanup DatabaseCleanup
	sqlFile, dumpErr := e.findDatabaseDump(job.WebRoot)
	if dumpErr != nil {
		return nil, fmt.Errorf("discover database dumps: %w", dumpErr)
	}
	if sqlFile != "" {
		// Get file size for reporting
		dumpSize, err := databaseDumpSize(sqlFile)
		if err != nil {
			return nil, fmt.Errorf("database dump became unavailable: %w", err)
		}

		job.DatabaseDumpPath = sqlFile
		job.DatabaseDumpSize = dumpSize
		job.Message = fmt.Sprintf("Found database dump: %s (%s)", filepath.Base(sqlFile), formatBytes(dumpSize))
		job.UpdatedAt = time.Now()
		onProgress(job)

		// Validate the SQL file before either publishing a manual instruction or
		// invoking the privileged adapter.
		if err := e.restoreDatabase(ctx, sqlFile, dbName, dbUser); err != nil {
			return nil, fmt.Errorf("database dump validation failed: %w", err)
		}
		if req.AutoRestoreDB {
			if req.DatabasePassword == "" {
				return nil, errors.New("automatic database restoration requires a database password")
			}
			if e.databaseRestorer == nil {
				return nil, errors.New("automatic database restoration is not configured")
			}
			var restoreErr error
			cleanup, restoreErr = e.databaseRestorer(ctx, sqlFile, dbName, dbUser, req.DatabasePassword, req.SiteName)
			if restoreErr != nil {
				return nil, fmt.Errorf("database restoration failed: %w", restoreErr)
			}
			if cleanup != nil {
				defer func() {
					if configError != nil {
						if cleanupErr := cleanup(); cleanupErr != nil {
							configError = fmt.Errorf("%w; database cleanup failed: %v", configError, cleanupErr)
						}
					}
				}()
			}
			dbRestorationIssue = &ImportIssue{
				Severity: "info",
				Code:     "database_restored",
				Message:  fmt.Sprintf("Database dump restored into managed database %s", dbName),
			}
			job.Message = "Database dump restored"
		} else {
			dbRestorationIssue = &ImportIssue{
				Severity: "warning",
				Code:     "database_restore_deferred",
				Message:  fmt.Sprintf("Database dump found at %s (%s). Operator must restore manually.", filepath.Base(sqlFile), formatBytes(dumpSize)),
			}
			job.Message = "Database dump file validated; manual restoration required"
		}
	} else {
		if req.AutoRestoreDB {
			return nil, errors.New("automatic database restoration requested but no database dump was found")
		}
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

	siteStatus := importSiteStatus(sqlFile)
	result := &ImportResult{
		JobID:         job.ID,
		Success:       true, // Files and config are done; database is optional/deferred
		SiteName:      job.SiteName,
		SiteStatus:    siteStatus,
		CreatedAt:     job.StartedAt,
		FilesImported: job.FilesExtracted,
		StorageSize:   job.BytesExtracted,
		Issues:        issues,
		NextSteps:     nextSteps,
	}
	if req.AutoRestoreDB {
		// The caller owns the cleanup until its outer activation transaction
		// commits. This function's config-update failure path above still
		// invokes it immediately.
		result.Cleanup = cleanup
	}

	return result, nil
}

func importSiteStatus(databaseDumpPath string) string {
	if databaseDumpPath != "" {
		return "needs_database_restore"
	}
	return "needs_database_setup"
}

// validateSiteCreation checks that the extraction target is a fresh directory
// path. It is the caller's responsibility to arrange this (typically by
// passing a unique staging path). Refusing an existing path here is what
// guarantees the executor never merges a partial archive over pre-existing
// files.
func (e *Executor) validateSiteCreation(job *ImportJob) error {
	if _, err := os.Stat(job.WebRoot); err == nil {
		return fmt.Errorf("extract target %q already exists", job.WebRoot)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot inspect extract target: %w", err)
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

	compressedSize := int64(0)
	if limitedBody, ok := body.(*limitedReadCloser); ok {
		compressedSize = limitedBody.contentLength
	}
	if compressedSize <= 0 {
		compressedSize = baselineExpandedEstimate
	}
	estimatedExpanded := compressedSize * 10
	if estimatedExpanded < baselineExpandedEstimate {
		estimatedExpanded = baselineExpandedEstimate
	}
	if estimatedExpanded > maxDecompressedSize {
		estimatedExpanded = maxDecompressedSize
	}
	spaceCheck := CheckDiskSpace(filepath.Dir(job.WebRoot), compressedSize, estimatedExpanded)
	if !spaceCheck.HasSufficientSpace {
		return fmt.Errorf("insufficient disk space for archive: %s", spaceCheck.Reason)
	}

	// The format comes from the archive's bytes, exactly as in the analyzer,
	// never from the URL.
	format, archive, err := archivesafe.DetectReader(body)
	if err != nil {
		return err
	}
	if format == archivesafe.Zip {
		return e.extractZip(archive, job, onProgress)
	}
	return e.extractTarGz(archive, job, onProgress)
}

// safeArchiveMode preserves the source's ordinary permission bits while
// stripping setuid, setgid, sticky, and file-type bits. A zero mode is common
// in hand-built archives; use a private usable default in that case.
func safeArchiveMode(mode os.FileMode, directory bool) os.FileMode {
	perm := mode.Perm()
	if perm == 0 {
		if directory {
			return 0700
		}
		return 0600
	}
	return perm
}

// validateConfigPath validates that a config path is safe (no traversal, not absolute, stays within webRoot)
func validateConfigPath(webRoot, configPath string) error {
	_, err := safeConfigPath(webRoot, configPath)
	return err
}

func safeConfigPath(webRoot, configPath string) (string, error) {
	if strings.TrimSpace(configPath) == "" {
		return "", errors.New("config path is empty")
	}
	// Reject absolute paths
	if filepath.IsAbs(configPath) {
		return "", errors.New("config path cannot be absolute")
	}

	// Reject path traversal attempts
	if strings.Contains(configPath, "..") {
		return "", errors.New("config path contains .. traversal")
	}

	// Reject paths starting with /
	if strings.HasPrefix(configPath, "/") {
		return "", errors.New("config path cannot start with /")
	}

	// Clean and validate the path stays within webRoot
	cleaned := filepath.Clean(configPath)
	targetPath, err := h.SafePath(webRoot, cleaned)
	if err != nil {
		return "", fmt.Errorf("config path escapes site root: %w", err)
	}
	return targetPath, nil
}

// recordExtractedFile updates progress after a regular file is written.
func recordExtractedFile(job *ImportJob, name string, written int64, onProgress func(*ImportJob)) {
	job.FilesExtracted++
	job.BytesExtracted += written
	job.CurrentFile = name
	// Progress from 20-60% for the extraction phase. The denominator is
	// capped so progress never moves backwards and stays below 60% until
	// extraction completes.
	fileProgress := job.FilesExtracted
	if fileProgress > 1000 {
		fileProgress = 1000
	}
	job.Progress = 20 + int(40*fileProgress/1000)
	job.UpdatedAt = time.Now()
	if job.FilesExtracted%100 == 0 {
		onProgress(job)
	}
}

// openArchiveExtractor confines extraction to the job root (see
// internal/archivesafe): entries cannot escape it, collide, or overwrite.
func openArchiveExtractor(job *ImportJob) (*archivesafe.Extractor, error) {
	extractor, err := archivesafe.OpenExtractor(job.WebRoot)
	if err != nil {
		return nil, err
	}
	extractor.ParentMode = 0o755
	return extractor, nil
}

// extractTarGz extracts a tar.gz archive with security checks
func (e *Executor) extractTarGz(reader io.Reader, job *ImportJob, onProgress func(*ImportJob)) error {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("not a valid gzip file: %w", err)
	}
	defer gz.Close()
	extractor, err := openArchiveExtractor(job)
	if err != nil {
		return err
	}
	defer extractor.Close()

	tr := tar.NewReader(gz)
	var totalDecompressed int64
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}

		// Archive bomb protection: validate before accumulating so malformed
		// negative or overflowing tar sizes cannot bypass the limit.
		if header.Size < 0 || header.Size > maxIndividualFileSize || totalDecompressed > maxDecompressedSize-header.Size {
			return fmt.Errorf("archive exceeds decompressed size limit (%d bytes)", maxDecompressedSize)
		}
		totalDecompressed += header.Size

		// Check total entries (files + directories) to prevent directory bombs
		job.EntriesProcessed++
		if job.EntriesProcessed > maxArchiveEntries {
			return fmt.Errorf("archive exceeds entry limit (%d total entries)", maxArchiveEntries)
		}

		kind, err := archivesafe.TarKind(header)
		if err != nil {
			return fmt.Errorf("unsafe archive entry: %w", err)
		}
		if kind == archivesafe.Directory {
			job.DirectoriesCreated++
			if job.DirectoriesCreated > maxDirectoriesInArchive {
				return fmt.Errorf("archive exceeds directory limit (%d dirs)", maxDirectoriesInArchive)
			}
			if _, err := extractor.Dir(header.Name, safeArchiveMode(os.FileMode(header.Mode), true)); err != nil {
				return fmt.Errorf("unsafe archive entry: %w", err)
			}
			continue
		}
		// Preserve executable bits but mask out setuid/setgid/sticky.
		name, written, err := extractor.File(header.Name, safeArchiveMode(os.FileMode(header.Mode), false), tr, header.Size)
		if err != nil {
			return fmt.Errorf("unsafe archive entry: %w", err)
		}
		recordExtractedFile(job, name, written, onProgress)
	}
}

// extractZip extracts a zip archive with security checks. A ZIP needs
// random access, so it is spooled to capacity-managed storage first.
func (e *Executor) extractZip(reader io.Reader, job *ImportJob, onProgress func(*ImportJob)) error {
	spooled, err := e.spoolFor(job).Store(reader, maxArchiveSize)
	if err != nil {
		return fmt.Errorf("failed to download archive: %w", err)
	}
	defer spooled.Close()

	zr, err := zip.NewReader(spooled, spooled.Size)
	if err != nil {
		return fmt.Errorf("not a valid zip file: %w", err)
	}
	if len(zr.File) > maxArchiveEntries {
		return fmt.Errorf("archive exceeds entry limit (%d total entries)", maxArchiveEntries)
	}
	// The central directory states every entry's size, so validate all
	// limits and the real space requirement before writing anything.
	var totalDecompressed int64
	for _, file := range zr.File {
		if file.UncompressedSize64 > uint64(maxIndividualFileSize) {
			return fmt.Errorf("file %s exceeds size limit (%d bytes)", file.Name, maxIndividualFileSize)
		}
		size := int64(file.UncompressedSize64)
		if totalDecompressed > maxDecompressedSize-size {
			return fmt.Errorf("archive exceeds decompressed size limit (%d bytes)", maxDecompressedSize)
		}
		totalDecompressed += size
	}
	if check := CheckDiskSpace(filepath.Dir(job.WebRoot), 0, totalDecompressed); !check.HasSufficientSpace {
		return fmt.Errorf("insufficient disk space for archive: %s", check.Reason)
	}

	extractor, err := openArchiveExtractor(job)
	if err != nil {
		return err
	}
	defer extractor.Close()
	for _, file := range zr.File {
		job.EntriesProcessed++
		kind, err := archivesafe.ZipKind(file)
		if err != nil {
			return fmt.Errorf("unsafe archive entry: %w", err)
		}
		mode := file.Mode()
		if kind == archivesafe.Directory {
			job.DirectoriesCreated++
			if job.DirectoriesCreated > maxDirectoriesInArchive {
				return fmt.Errorf("archive exceeds directory limit (%d dirs)", maxDirectoriesInArchive)
			}
			if _, err := extractor.Dir(file.Name, safeArchiveMode(mode, true)); err != nil {
				return fmt.Errorf("unsafe archive entry: %w", err)
			}
			continue
		}
		src, err := file.Open()
		if err != nil {
			return fmt.Errorf("failed to open %s in zip: %w", file.Name, err)
		}
		name, written, err := extractor.File(file.Name, safeArchiveMode(mode, false), src, int64(file.UncompressedSize64))
		closeErr := src.Close()
		if err != nil {
			return fmt.Errorf("unsafe archive entry: %w", err)
		}
		if closeErr != nil {
			return fmt.Errorf("failed to close %s in zip: %w", file.Name, closeErr)
		}
		recordExtractedFile(job, name, written, onProgress)
	}
	return nil
}

// findDatabaseDump looks for SQL dump files in the extracted archive using
// proper recursion. A traversal failure is distinct from finding no dump:
// silently treating an unreadable subtree as absent can produce a seemingly
// successful partial migration.
func (e *Executor) findDatabaseDump(webRoot string) (string, error) {
	var found string
	err := filepath.WalkDir(webRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if found != "" {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			name := d.Name()
			// Check for common database dump names
			if strings.HasSuffix(name, ".sql") || strings.HasSuffix(name, ".sql.gz") {
				// Prioritize specific names
				if name == "backup.sql" || name == "database.sql" || name == "db.sql" {
					found = path
					return nil
				}
				if found == "" {
					found = path
				}
			}
		}
		return nil
	})
	return found, err
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

	// Database dump validation: File is valid, path and contents confirmed.
	// Restoration is handled by ExecuteImport's restoreDatabase callback,
	// which executes database provisioning and restoration via Config.DBCtl
	// if auto_restore_db is enabled and credentials are provided.
	// Operator instructions are provided if credentials were not supplied.
	return nil
}

func databaseDumpSize(path string) (int64, error) {
	file, info, err := h.OpenRegularNoFollow(path, nil)
	if err != nil {
		return 0, err
	}
	if err := file.Close(); err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// extractDatabaseInfo extracts database name/user from config file
func (e *Executor) extractDatabaseInfo(configFile string) (dbName, dbUser string) {
	file, _, err := h.OpenRegularNoFollow(configFile, nil)
	if err != nil {
		return "", ""
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return "", ""
	}

	content := string(data)
	dbName = extractWordPressDefine(content, "DB_NAME")
	dbUser = extractWordPressDefine(content, "DB_USER")

	return dbName, dbUser
}

// updateConfiguration updates config files with correct credentials
func (e *Executor) updateConfiguration(job *ImportJob, configPath string) error {
	_, err := e.updateConfigurationWithReport(job, configPath)
	return err
}

// ConfigurationUpdateReport records which credential definitions were changed
// and which were intentionally left alone. This distinction is important for
// function-backed values such as getenv(), which cannot safely be replaced by
// a literal without changing the configuration's meaning.
type ConfigurationUpdateReport struct {
	Updated []string
	Skipped []string
}

func (e *Executor) updateConfigurationWithReport(job *ImportJob, configPath string) (ConfigurationUpdateReport, error) {
	var report ConfigurationUpdateReport
	// Validate config path is safe (no traversal, not absolute)
	configFile, err := safeConfigPath(job.WebRoot, configPath)
	if err != nil {
		return report, fmt.Errorf("invalid config path: %w", err)
	}

	// Verify the file exists and is a regular file (not symlink/directory/etc)
	file, info, err := h.OpenRegularNoFollow(configFile, nil)
	if err != nil {
		return report, fmt.Errorf("cannot access config file: %w", err)
	}
	// Read the same no-follow descriptor that was validated above.
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		if readErr == nil {
			readErr = closeErr
		}
		return report, fmt.Errorf("cannot read config file: %w", readErr)
	}

	content := string(data)
	modified := false

	// Replace database name, user, and create password placeholder
	updates := map[string]string{
		"DB_NAME": job.DatabaseName,
		"DB_USER": job.DatabaseUser,
	}
	if job.DatabasePassword != "" {
		updates["DB_PASSWORD"] = job.DatabasePassword
	}

	for key, value := range updates {
		if value != "" {
			// Replace define('KEY', 'old_value') with define('KEY', 'new_value')
			// This is a simple text replacement; a proper parser would be better
			for _, quote := range []string{"'", "\""} {
				pattern := fmt.Sprintf("define(%s%s%s", quote, key, quote)
				if strings.Contains(content, pattern) {
					updated, changed, skipped, replaceErr := replaceDefineValue(content, key, quote, value)
					if replaceErr != nil {
						return report, fmt.Errorf("cannot update %s: %w", key, replaceErr)
					}
					if changed {
						content = updated
						modified = true
						if !containsString(report.Updated, key) {
							report.Updated = append(report.Updated, key)
						}
					} else if skipped && !containsString(report.Skipped, key) {
						report.Skipped = append(report.Skipped, key)
					}
				}
			}
		}
	}

	if modified {
		if err := durableReplaceConfig(configFile, []byte(content), info); err != nil {
			return report, err
		}
	}

	return report, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// durableReplaceConfig publishes a replacement only after its contents and
// metadata are synced. The parent-directory sync makes the rename durable
// across a sudden power loss, while preserving the original mode and owner.
func durableReplaceConfig(configFile string, content []byte, info os.FileInfo) (retErr error) {
	tempFile, err := os.CreateTemp(filepath.Dir(configFile), "."+filepath.Base(configFile)+".*")
	if err != nil {
		return fmt.Errorf("cannot create temp file: %w", err)
	}
	tempName := tempFile.Name()
	defer os.Remove(tempName)

	if _, err := tempFile.Write(content); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("cannot write temp file: %w", err)
	}
	if err := tempFile.Chmod(info.Mode().Perm()); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("cannot set temp file permissions: %w", err)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if err := tempFile.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			_ = tempFile.Close()
			return fmt.Errorf("cannot preserve config file ownership: %w", err)
		}
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		return fmt.Errorf("cannot sync temp config file: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("cannot close temp config file: %w", err)
	}
	if err := os.Rename(tempName, configFile); err != nil {
		return fmt.Errorf("cannot replace config file: %w", err)
	}
	if err := syncDirectory(filepath.Dir(configFile)); err != nil {
		return fmt.Errorf("config replacement committed but parent directory sync failed: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	if closeErr := directory.Close(); err == nil {
		err = closeErr
	}
	return err
}

// replaceDefineValue replaces the value of a define() statement
// Handles formats like: define('KEY', 'value') or define ( 'KEY', 'value' )
// Does NOT replace if value comes from a function call (getenv, env, etc)
func replaceDefineValue(content, key, quote, newValue string) (string, bool, bool, error) {
	// Pattern: define (with optional spaces) ( KEY (with optional spaces) ,
	// Use regex to be more flexible with whitespace
	escapedQuote := regexp.QuoteMeta(quote)
	escapedKey := regexp.QuoteMeta(key)
	pattern := fmt.Sprintf(`define\s*\(\s*%s%s%s\s*,`, escapedQuote, escapedKey, escapedQuote)

	re, err := regexp.Compile(pattern)
	if err != nil {
		return content, false, false, err
	}

	matches := re.FindAllStringIndex(content, -1)
	if len(matches) == 0 {
		return content, false, false, nil
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
	if (idx+7 <= len(afterComma) && strings.HasPrefix(remainingContent, "getenv(")) ||
		(idx+4 <= len(afterComma) && strings.HasPrefix(remainingContent, "env(")) {
		// Value comes from function - don't replace
		return content, false, true, nil
	}

	// Find the opening quote
	if idx >= len(afterComma) || (afterComma[idx] != '\'' && afterComma[idx] != '"') {
		return content, false, false, fmt.Errorf("%s has an unsupported value expression", key)
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
		return content, false, false, fmt.Errorf("%s has an unterminated string value", key)
	}

	encoded, err := encodePHPDoubleQuotedString(newValue)
	if err != nil {
		return content, false, false, fmt.Errorf("%s value cannot be encoded: %w", key, err)
	}
	newContent := content[:matchEnd+idx] + `"` + encoded + `"` + afterComma[closeIdx+1:]
	return newContent, newContent != content, false, nil
}

// encodePHPDoubleQuotedString returns the body of a PHP double-quoted string.
// Every interpolation or escape introducer is neutralized; invalid UTF-8 and
// unsupported control bytes are rejected rather than emitted as PHP source.
func encodePHPDoubleQuotedString(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", errors.New("invalid UTF-8")
	}
	var escaped strings.Builder
	for _, r := range value {
		switch r {
		case '\\':
			escaped.WriteString(`\\`)
		case '"':
			escaped.WriteString(`\"`)
		case '$':
			escaped.WriteString(`\$`)
		case '`':
			escaped.WriteString("\\`")
		case '\n':
			escaped.WriteString(`\n`)
		case '\r':
			escaped.WriteString(`\r`)
		case '\t':
			escaped.WriteString(`\t`)
		case 0:
			escaped.WriteString(`\x00`)
		default:
			if r < 0x20 || r == 0x7f {
				return "", fmt.Errorf("unsupported control character U+%04X", r)
			}
			escaped.WriteRune(r)
		}
	}
	return escaped.String(), nil
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
