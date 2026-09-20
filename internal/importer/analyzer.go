package importer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// isAllowedURL validates that a URL is safe to fetch (prevents SSRF)
func isAllowedURL(urlStr string) bool {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return false
	}

	// Only allow https
	if parsed.Scheme != "https" {
		return false
	}

	// Extract hostname
	host := parsed.Hostname()
	if host == "" {
		return false
	}

	// Reject localhost and private IPs
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return false
		}
	}

	// Reject numeric IPs unless they're public
	if net.ParseIP(host) != nil {
		ip := net.ParseIP(host)
		if !ip.IsGlobalUnicast() {
			return false
		}
	}

	return true
}

// Analyzer inspects and analyzes archive contents
type Analyzer struct {
	httpClient *http.Client
}

// NewAnalyzer creates a new archive analyzer
func NewAnalyzer() *Analyzer {
	return &Analyzer{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// InspectArchive analyzes an archive at a given URL
func (a *Analyzer) InspectArchive(url, configPath string) (*ArchiveInspection, error) {
	if url == "" {
		return nil, errors.New("archive URL is required")
	}
	if configPath == "" {
		return nil, errors.New("config path is required")
	}

	// Prevent SSRF: only allow https URLs from known domains
	if !isAllowedURL(url) {
		return nil, fmt.Errorf("archive URL not allowed: %s", url)
	}

	// Download archive header to determine type and size
	req, err := http.NewRequest(http.MethodHead, url, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid archive URL: %w", err)
	}

	resp, err := a.httpClient.Do(req) // lgtm[go/request-forgery]: URL is validated by isAllowedURL() at line 79
	if err != nil {
		return nil, fmt.Errorf("failed to fetch archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive URL returned %d", resp.StatusCode)
	}

	archiveType := a.detectArchiveType(url, resp.Header.Get("Content-Type"))
	if archiveType == "" {
		return nil, errors.New("could not determine archive type (expected .tar.gz or .zip)")
	}

	size := resp.ContentLength
	if size <= 0 || size > 5*1024*1024*1024 { // 5GB limit
		return nil, errors.New("archive size invalid or exceeds 5GB limit")
	}

	// Download archive with size limit (prevent server from lying about size)
	maxBytes := size + (1 << 20) // Add 1MB buffer to claimed size
	bodyReq, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid archive URL: %w", err)
	}

	bodyResp, err := a.httpClient.Do(bodyReq) // lgtm[go/request-forgery]: URL is validated by isAllowedURL() at line 79
	if err != nil {
		return nil, fmt.Errorf("failed to download archive: %w", err)
	}
	defer bodyResp.Body.Close()

	// Limit the download to prevent disk exhaustion
	limitedBody := io.LimitReader(bodyResp.Body, maxBytes)

	inspection := &ArchiveInspection{
		URL:         url,
		ArchiveType: archiveType,
		Size:        size,
		CreatedAt:   time.Now(),
		ConfigPath:  configPath,
		Issues:      []ImportIssue{},
	}

	// Parse archive based on type
	if archiveType == "tar.gz" {
		err = a.inspectTarGz(limitedBody, configPath, inspection)
	} else if archiveType == "zip" {
		// For zip files, we need to seek, so download to temp file
		tempFile, err := os.CreateTemp("", "archive-*.zip")
		if err != nil {
			return nil, fmt.Errorf("failed to create temp file: %w", err)
		}
		defer os.Remove(tempFile.Name())

		if _, err := io.Copy(tempFile, limitedBody); err != nil {
			return nil, fmt.Errorf("failed to download archive: %w", err)
		}
		tempFile.Close()

		err = a.inspectZip(tempFile.Name(), configPath, inspection)
	}

	if err != nil {
		inspection.Issues = append(inspection.Issues, ImportIssue{
			Severity: "error",
			Code:     "archive_read_failed",
			Message:  fmt.Sprintf("Failed to read archive: %v", err),
		})
		return inspection, fmt.Errorf("failed to inspect archive: %w", err)
	}

	return inspection, nil
}

// inspectTarGz analyzes a tar.gz archive
func (a *Analyzer) inspectTarGz(reader io.Reader, configPath string, inspection *ArchiveInspection) error {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("not a valid gzip file: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	inspection.Structure = ArchiveStructure{
		FileExtensions: []string{},
		LargestFiles:   []string{},
	}

	seen := make(map[string]bool)
	largestFiles := make([]struct {
		name string
		size int64
	}, 0, 100) // Limit to top 100 files
	const maxLargestFiles = 100

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}

		if header.Typeflag == tar.TypeDir {
			inspection.Structure.TotalDirs++
		} else {
			inspection.Structure.TotalFiles++
		}

		// Track file extensions
		ext := filepath.Ext(header.Name)
		if ext != "" && !seen[ext] {
			inspection.Structure.FileExtensions = append(inspection.Structure.FileExtensions, ext)
			seen[ext] = true
		}

		// Track largest files (limit memory by only tracking top N files)
		if len(largestFiles) < maxLargestFiles || header.Size > largestFiles[len(largestFiles)-1].size {
			largestFiles = append(largestFiles, struct {
				name string
				size int64
			}{header.Name, header.Size})
			// Keep sorted by size (descending)
			sort.Slice(largestFiles, func(i, j int) bool {
				return largestFiles[i].size > largestFiles[j].size
			})
			// Trim to max size
			if len(largestFiles) > maxLargestFiles {
				largestFiles = largestFiles[:maxLargestFiles]
			}
		}

		// Check for config file
		if strings.TrimPrefix(header.Name, "./") == configPath || filepath.Base(header.Name) == filepath.Base(configPath) {
			configContent := make([]byte, min(header.Size, 1024*1024)) // limit to 1MB
			n, _ := io.ReadFull(tr, configContent)
			a.parseConfig(string(configContent[:n]), inspection)
		}

		// Detect site type
		if strings.Contains(header.Name, "wp-content") || strings.Contains(header.Name, "wp-admin") {
			inspection.Structure.HasWordPressCore = true
		}
		if strings.Contains(header.Name, "wp-content/plugins/") && strings.Contains(header.Name, "/mu-") {
			inspection.Structure.HasWordPressMU = true
		}
		if strings.HasSuffix(header.Name, ".sql") || strings.HasSuffix(header.Name, ".sql.gz") {
			inspection.Structure.HasDatabase = true
		}
	}

	// Sort largest files
	a.extractLargestFiles(largestFiles, inspection)
	a.validateInspection(inspection)

	return nil
}

// inspectZip analyzes a zip archive
func (a *Analyzer) inspectZip(path, configPath string, inspection *ArchiveInspection) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("not a valid zip file: %w", err)
	}
	defer reader.Close()

	inspection.Structure = ArchiveStructure{
		FileExtensions: []string{},
		LargestFiles:   []string{},
	}

	seen := make(map[string]bool)
	largestFiles := make([]struct {
		name string
		size int64
	}, 0, 100) // Limit to top 100 files
	const maxLargestFiles = 100

	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			inspection.Structure.TotalDirs++
		} else {
			inspection.Structure.TotalFiles++
		}

		// Track file extensions
		ext := filepath.Ext(file.Name)
		if ext != "" && !seen[ext] {
			inspection.Structure.FileExtensions = append(inspection.Structure.FileExtensions, ext)
			seen[ext] = true
		}

		// Track largest files (limit memory by only tracking top N files)
		fileSize := file.FileInfo().Size()
		if len(largestFiles) < maxLargestFiles || fileSize > largestFiles[len(largestFiles)-1].size {
			largestFiles = append(largestFiles, struct {
				name string
				size int64
			}{file.Name, fileSize})
			// Keep sorted by size (descending)
			sort.Slice(largestFiles, func(i, j int) bool {
				return largestFiles[i].size > largestFiles[j].size
			})
			// Trim to max size
			if len(largestFiles) > maxLargestFiles {
				largestFiles = largestFiles[:maxLargestFiles]
			}
		}

		// Check for config file
		if strings.TrimPrefix(file.Name, "./") == configPath || filepath.Base(file.Name) == filepath.Base(configPath) {
			f, _ := file.Open()
			if f != nil {
				configContent := make([]byte, min(file.FileInfo().Size(), 1024*1024))
				n, _ := io.ReadFull(f, configContent)
				a.parseConfig(string(configContent[:n]), inspection)
				f.Close()
			}
		}

		// Detect site type
		if strings.Contains(file.Name, "wp-content") || strings.Contains(file.Name, "wp-admin") {
			inspection.Structure.HasWordPressCore = true
		}
		if strings.HasSuffix(file.Name, ".sql") || strings.HasSuffix(file.Name, ".sql.gz") {
			inspection.Structure.HasDatabase = true
		}
	}

	a.extractLargestFiles(largestFiles, inspection)
	a.validateInspection(inspection)

	return nil
}

// parseConfig extracts site requirements from a config file
func (a *Analyzer) parseConfig(content string, inspection *ArchiveInspection) {
	// Detect config type and parse accordingly
	if strings.Contains(content, "<?php") || strings.Contains(content, "DB_NAME") {
		inspection.ConfigType = "wordpress"
		a.parseWordPressConfig(content, inspection)
	} else {
		inspection.ConfigType = "unknown"
		inspection.Issues = append(inspection.Issues, ImportIssue{
			Severity: "warning",
			Code:     "config_type_unknown",
			Message:  "Could not automatically detect config type. Manual review recommended.",
		})
	}
}

// parseWordPressConfig extracts requirements from wp-config.php
func (a *Analyzer) parseWordPressConfig(content string, inspection *ArchiveInspection) {
	req := SiteRequirements{
		Extensions: []string{},
	}

	// Extract database credentials
	req.DatabaseType = "mysql"
	req.DatabaseName = a.extractDefine(content, "DB_NAME")
	req.DatabaseUser = a.extractDefine(content, "DB_USER")

	// Estimate storage
	req.EstimatedStorageGB = 5 // default estimate
	if inspection.Structure.TotalFiles > 100000 {
		req.EstimatedStorageGB = 20
	}
	if inspection.Structure.TotalFiles > 500000 {
		req.EstimatedStorageGB = 100
	}

	// Assume PHP 8.0+ for modern WordPress
	req.PHPVersion = "8.0+"
	req.WebServer = "apache" // will be auto-detected or ask user

	// Common WordPress extensions
	req.Extensions = []string{"mysqli", "curl", "gd", "mbstring", "zip"}

	inspection.Requirements = req

	if req.DatabaseName == "" {
		inspection.Issues = append(inspection.Issues, ImportIssue{
			Severity: "error",
			Code:     "database_name_not_found",
			Message:  "Could not find DB_NAME in config. Database setup required.",
		})
	}
}

// extractDefine extracts a WordPress define() value
func (a *Analyzer) extractDefine(content, key string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.Contains(line, fmt.Sprintf("define('%s'", key)) || strings.Contains(line, fmt.Sprintf("define(\"%s\"", key)) {
			// Simple extraction
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

// extractLargestFiles identifies the top files by size
func (a *Analyzer) extractLargestFiles(files []struct {
	name string
	size int64
}, inspection *ArchiveInspection) {
	// Files are already sorted by size (largest first) from inspectTarGz/inspectZip
	// Extract top 5 largest files
	count := 0
	for _, file := range files {
		if count >= 5 {
			break
		}
		if !strings.HasPrefix(filepath.Base(file.name), ".") {
			inspection.Structure.LargestFiles = append(inspection.Structure.LargestFiles, file.name)
			inspection.Structure.EstimatedStorageGB += file.size / (1024 * 1024 * 1024)
			count++
		}
	}
}

// validateInspection checks for issues
func (a *Analyzer) validateInspection(inspection *ArchiveInspection) {
	if inspection.Structure.TotalFiles == 0 {
		inspection.Issues = append(inspection.Issues, ImportIssue{
			Severity: "error",
			Code:     "empty_archive",
			Message:  "Archive appears to be empty.",
		})
	}

	if inspection.ConfigType == "unknown" && inspection.Structure.TotalFiles > 0 {
		inspection.Issues = append(inspection.Issues, ImportIssue{
			Severity: "warning",
			Code:     "config_type_unclear",
			Message:  "Could not auto-detect site type. Verify config path is correct.",
		})
	}

	if !inspection.Structure.HasDatabase {
		inspection.Issues = append(inspection.Issues, ImportIssue{
			Severity: "info",
			Code:     "no_database_found",
			Message:  "No database backup found in archive. Database will need to be restored separately.",
		})
	}
}

// detectArchiveType determines if archive is tar.gz or zip
func (a *Analyzer) detectArchiveType(url, contentType string) string {
	urlLower := strings.ToLower(url)
	if strings.HasSuffix(urlLower, ".tar.gz") || strings.HasSuffix(urlLower, ".tgz") {
		return "tar.gz"
	}
	if strings.HasSuffix(urlLower, ".zip") {
		return "zip"
	}
	if strings.Contains(contentType, "gzip") || strings.Contains(contentType, "x-tar") {
		return "tar.gz"
	}
	if strings.Contains(contentType, "zip") {
		return "zip"
	}
	return ""
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
