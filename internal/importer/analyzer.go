package importer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cyberducttape/StePanel/internal/archivesafe"
	"github.com/cyberducttape/StePanel/internal/safehttp"
)

// archiveFetchPolicy is the shared StePanel outbound policy: public internet
// destinations only, enforced on the connected address.
var archiveFetchPolicy safehttp.Policy

// isReservedIP reports whether ip is outside the public internet.
func isReservedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	return !ok || !archiveFetchPolicy.Allows(addr)
}

// isAllowedURL validates that a URL is safe to fetch (prevents SSRF).
// Hostnames are checked again on the resolved address at connect time.
func isAllowedURL(urlStr string) bool {
	return archiveFetchPolicy.ValidateURL(urlStr) == nil
}

// NewSafeArchiveTransport creates an HTTP transport that only connects to
// public addresses. Used by both archive inspection (Analyzer) and import
// (Executor) so they share the safehttp policy. Granular timeouts instead of a
// global timeout support large file transfers.
func NewSafeArchiveTransport() *http.Transport {
	return archiveFetchPolicy.Transport(safehttp.TransportOptions{
		DialTimeout:           15 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	})
}

// Analyzer inspects and analyzes archive contents
type Analyzer struct {
	httpClient *http.Client
	spool      ArchiveSpool
}

// WithSpool sets where ZIP archives are spooled for inspection. ZIP
// inspection is refused without one rather than falling back to the system
// temporary directory.
func (a *Analyzer) WithSpool(spool ArchiveSpool) *Analyzer {
	a.spool = spool
	return a
}

const (
	maxArchiveRedirects      = 10
	maxArchiveInspectionTime = 15 * time.Minute
	maxConfigInspectionBytes = 1024 * 1024
	maxUniqueFileExtensions  = 1024
)

var errArchiveLimitExceeded = errors.New("archive inspection limit exceeded")

// NewAnalyzer creates a new archive analyzer with secure redirect handling
func NewAnalyzer() *Analyzer {
	return &Analyzer{
		httpClient: &http.Client{
			// The request context remains the primary cancellation mechanism;
			// this is a final defense for callers that provide no deadline.
			Transport: NewSafeArchiveTransport(),
			Timeout:   maxArchiveInspectionTime,
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
	}
}

// normalizeArchivePath gives archive entries and caller-selected paths one
// canonical namespace, the same one extraction uses.
func normalizeArchivePath(name string) (string, error) {
	cleaned, err := archivesafe.NormalizeName(name)
	if err != nil {
		return "", err
	}
	if cleaned == "." {
		return "", errors.New("archive path names the archive root")
	}
	return cleaned, nil
}

func normalizeConfigArchivePath(configPath string) (string, error) {
	return normalizeArchivePath(configPath)
}

// InspectArchive analyzes an archive at a given URL
func (a *Analyzer) InspectArchive(ctx context.Context, url, configPath string) (*ArchiveInspection, error) {
	if url == "" {
		return nil, errors.New("archive URL is required")
	}
	if configPath == "" {
		return nil, errors.New("config path is required")
	}
	configPath, err := normalizeConfigArchivePath(configPath)
	if err != nil {
		return nil, fmt.Errorf("invalid config path: %w", err)
	}

	// Prevent SSRF: only allow https URLs from known domains
	if !isAllowedURL(url) {
		return nil, fmt.Errorf("archive URL not allowed: %s", url)
	}
	ctx, cancel := context.WithTimeout(ctx, maxArchiveInspectionTime)
	defer cancel()

	// Fetch the archive once. HEAD is intentionally avoided: valid chunked and
	// streaming endpoints often cannot provide Content-Length.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid archive URL: %w", err)
	}

	// Re-validate URL for static analysis (URL validated at line 176; redirects checked via CheckRedirect policy)
	if !isAllowedURL(url) {
		return nil, fmt.Errorf("archive URL not allowed: %s", url)
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch archive: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("archive URL returned %d", resp.StatusCode)
	}

	size := resp.ContentLength
	if size > maxArchiveSize {
		return nil, errors.New("archive exceeds 5GB limit")
	}
	limitedBody := &archiveByteLimiter{reader: resp.Body, remaining: maxArchiveSize + 1}

	// The format comes from the archive's bytes, exactly as in the executor.
	format, archive, err := archivesafe.DetectReader(inspectionContextReader{ctx: ctx, reader: limitedBody})
	if err != nil {
		return nil, fmt.Errorf("could not determine archive type: %w", err)
	}
	archiveType := string(format)

	inspection := &ArchiveInspection{
		URL:         url,
		ArchiveType: archiveType,
		Size:        maxInt64(size, 0),
		CreatedAt:   time.Now(),
		ConfigPath:  configPath,
		Issues:      []ImportIssue{},
	}

	if format == archivesafe.TarGzip {
		err = a.inspectTarGz(ctx, archive, configPath, inspection)
	} else {
		if a.spool == nil {
			return nil, errors.New("ZIP inspection requires a configured spool directory")
		}
		spooled, spoolErr := a.spool.Store(archive, maxArchiveSize)
		if spoolErr != nil {
			return nil, fmt.Errorf("failed to download archive: %w", spoolErr)
		}
		defer spooled.Close()
		inspection.Size = spooled.Size
		zr, zipErr := zip.NewReader(spooled, spooled.Size)
		if zipErr != nil {
			err = fmt.Errorf("not a valid zip file: %w", zipErr)
		} else {
			err = a.inspectZip(ctx, zr, configPath, inspection)
		}
	}
	if limitedBody.n > maxArchiveSize {
		err = fmt.Errorf("archive exceeds compressed size limit: %d bytes", maxArchiveSize)
	}
	if inspection.Size == 0 {
		inspection.Size = limitedBody.n
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

type largestArchiveFile struct {
	name string
	size int64
}

type largestArchiveFiles []largestArchiveFile

func (h largestArchiveFiles) Len() int            { return len(h) }
func (h largestArchiveFiles) Less(i, j int) bool  { return h[i].size < h[j].size }
func (h largestArchiveFiles) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *largestArchiveFiles) Push(x interface{}) { *h = append(*h, x.(largestArchiveFile)) }
func (h *largestArchiveFiles) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

func trackLargestFile(files *largestArchiveFiles, file largestArchiveFile) {
	if files.Len() < 100 {
		heap.Push(files, file)
		return
	}
	if file.size > (*files)[0].size {
		heap.Pop(files)
		heap.Push(files, file)
	}
}

func finalizeLargestFiles(files largestArchiveFiles, inspection *ArchiveInspection) {
	sort.Slice(files, func(i, j int) bool { return files[i].size > files[j].size })
	inspection.Structure.LargestFiles = inspection.Structure.LargestFiles[:0]
	for _, file := range files {
		inspection.Structure.LargestFiles = append(inspection.Structure.LargestFiles, file.name)
	}
}

// inspectTarGz analyzes a tar.gz archive
func (a *Analyzer) inspectTarGz(ctx context.Context, reader io.Reader, configPath string, inspection *ArchiveInspection) error {
	gz, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("not a valid gzip file: %w", err)
	}
	defer gz.Close()

	decompressed := &archiveByteLimiter{reader: inspectionContextReader{ctx: ctx, reader: gz}, remaining: maxDecompressedSize + 1}
	tr := tar.NewReader(decompressed)
	inspection.Structure = ArchiveStructure{
		FileExtensions: []string{},
		LargestFiles:   []string{},
	}

	seen := make(map[string]struct{}, maxUniqueFileExtensions)
	largestFiles := make(largestArchiveFiles, 0, 100)
	paths := archivesafe.NewPathSet()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}
		if inspection.Structure.TotalFiles+inspection.Structure.TotalDirs >= maxArchiveEntries {
			return fmt.Errorf("archive exceeds entry limit (%d total entries)", maxArchiveEntries)
		}
		if header.Size < 0 || header.Size > maxIndividualFileSize {
			return fmt.Errorf("file %s exceeds size limit (%d bytes)", header.Name, maxIndividualFileSize)
		}
		// Apply the extractor's entry rules so an archive that inspection
		// accepts is one extraction accepts.
		kind, err := archivesafe.TarKind(header)
		if err != nil {
			return err
		}
		if _, err := paths.Claim(header.Name, kind); err != nil {
			return err
		}

		if kind == archivesafe.Directory {
			inspection.Structure.TotalDirs++
			if inspection.Structure.TotalDirs > maxDirectoriesInArchive {
				return fmt.Errorf("archive exceeds directory limit (%d dirs)", maxDirectoriesInArchive)
			}
		} else {
			inspection.Structure.TotalFiles++
		}

		// Track file extensions
		ext := filepath.Ext(header.Name)
		if ext != "" {
			if _, ok := seen[ext]; !ok && len(seen) < maxUniqueFileExtensions {
				inspection.Structure.FileExtensions = append(inspection.Structure.FileExtensions, ext)
				seen[ext] = struct{}{}
			}
		}

		// Track largest files (limit memory by only tracking top N files)
		trackLargestFile(&largestFiles, largestArchiveFile{header.Name, header.Size})

		// Check for config file
		if normalized, normalizeErr := normalizeArchivePath(header.Name); normalizeErr == nil && normalized == configPath {
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
	if decompressed.n > maxDecompressedSize {
		return fmt.Errorf("archive exceeds decompressed size limit (%d bytes)", maxDecompressedSize)
	}

	// Sort largest files
	finalizeLargestFiles(largestFiles, inspection)
	a.validateInspection(inspection)

	return nil
}

// inspectZip analyzes a zip archive
func (a *Analyzer) inspectZip(ctx context.Context, reader *zip.Reader, configPath string, inspection *ArchiveInspection) error {
	if len(reader.File) > maxArchiveEntries {
		return fmt.Errorf("archive exceeds entry limit (%d total entries)", maxArchiveEntries)
	}

	inspection.Structure = ArchiveStructure{
		FileExtensions: []string{},
		LargestFiles:   []string{},
	}

	seen := make(map[string]struct{}, maxUniqueFileExtensions)
	largestFiles := make(largestArchiveFiles, 0, 100)

	var totalDecompressed uint64
	paths := archivesafe.NewPathSet()
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		kind, err := archivesafe.ZipKind(file)
		if err != nil {
			return err
		}
		if _, err := paths.Claim(file.Name, kind); err != nil {
			return err
		}
		if file.UncompressedSize64 > uint64(maxIndividualFileSize) {
			return fmt.Errorf("file %s exceeds size limit (%d bytes)", file.Name, maxIndividualFileSize)
		}
		if totalDecompressed > uint64(maxDecompressedSize)-file.UncompressedSize64 {
			return fmt.Errorf("archive exceeds decompressed size limit (%d bytes)", maxDecompressedSize)
		}
		totalDecompressed += file.UncompressedSize64
		if kind == archivesafe.Directory {
			inspection.Structure.TotalDirs++
			if inspection.Structure.TotalDirs > maxDirectoriesInArchive {
				return fmt.Errorf("archive exceeds directory limit (%d dirs)", maxDirectoriesInArchive)
			}
		} else {
			inspection.Structure.TotalFiles++
		}

		// Track file extensions
		ext := filepath.Ext(file.Name)
		if ext != "" {
			if _, ok := seen[ext]; !ok && len(seen) < maxUniqueFileExtensions {
				inspection.Structure.FileExtensions = append(inspection.Structure.FileExtensions, ext)
				seen[ext] = struct{}{}
			}
		}

		// Track largest files (limit memory by only tracking top N files)
		fileSize := file.FileInfo().Size()
		trackLargestFile(&largestFiles, largestArchiveFile{file.Name, fileSize})

		// Check for config file
		if normalized, normalizeErr := normalizeArchivePath(file.Name); normalizeErr == nil && normalized == configPath {
			f, _ := file.Open()
			if f != nil {
				configContent := make([]byte, min(file.FileInfo().Size(), maxConfigInspectionBytes))
				n, readErr := io.ReadFull(inspectionContextReader{ctx: ctx, reader: io.LimitReader(f, maxConfigInspectionBytes)}, configContent)
				if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
					f.Close()
					return readErr
				}
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

	finalizeLargestFiles(largestFiles, inspection)
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
	for i := 0; i < len(content); {
		if next, ok := skipPHPCommentOrString(content, i); ok {
			i = next
			continue
		}
		if !isPHPIdentifierStart(content[i]) {
			i++
			continue
		}

		start := i
		for i < len(content) && isPHPIdentifierPart(content[i]) {
			i++
		}
		if !strings.EqualFold(content[start:i], "define") || (start > 0 && isPHPIdentifierPart(content[start-1])) {
			continue
		}

		pos := skipPHPWhitespace(content, i)
		if pos >= len(content) || content[pos] != '(' {
			continue
		}
		pos = skipPHPWhitespace(content, pos+1)
		name, pos, ok := readPHPStringArgument(content, pos)
		if !ok || name != key {
			continue
		}
		pos = skipPHPWhitespace(content, pos)
		if pos >= len(content) || content[pos] != ',' {
			continue
		}
		pos = skipPHPWhitespace(content, pos+1)
		value, _, ok := readPHPStringArgument(content, pos)
		if ok {
			return value
		}
	}
	return ""
}

func isPHPIdentifierStart(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func isPHPIdentifierPart(b byte) bool {
	return isPHPIdentifierStart(b) || b >= '0' && b <= '9'
}

func skipPHPWhitespace(content string, pos int) int {
	for pos < len(content) {
		switch content[pos] {
		case ' ', '\t', '\r', '\n', '\v', '\f':
			pos++
		case '/', '#':
			if next, ok := skipPHPCommentOrString(content, pos); ok {
				pos = next
				continue
			}
			return pos
		default:
			return pos
		}
	}
	return pos
}

// skipPHPCommentOrString skips quoted text or PHP comments so fake define calls
// in comments and string literals are not treated as executable code.
func skipPHPCommentOrString(content string, pos int) (int, bool) {
	if pos >= len(content) {
		return pos, false
	}
	if content[pos] == '\'' || content[pos] == '"' {
		quote := content[pos]
		for i := pos + 1; i < len(content); i++ {
			if content[i] == '\\' {
				i++
				continue
			}
			if content[i] == quote {
				return i + 1, true
			}
		}
		return len(content), true
	}
	if content[pos] == '#' || (content[pos] == '/' && pos+1 < len(content) && (content[pos+1] == '/' || content[pos+1] == '*')) {
		if content[pos] == '/' && content[pos+1] == '*' {
			if end := strings.Index(content[pos+2:], "*/"); end >= 0 {
				return pos + 2 + end + 2, true
			}
			return len(content), true
		}
		if end := strings.IndexByte(content[pos:], '\n'); end >= 0 {
			return pos + end + 1, true
		}
		return len(content), true
	}
	return pos, false
}

func readPHPStringArgument(content string, pos int) (string, int, bool) {
	if pos >= len(content) || content[pos] != '\'' && content[pos] != '"' {
		return "", pos, false
	}
	quote := content[pos]
	var value strings.Builder
	for i := pos + 1; i < len(content); i++ {
		if content[i] == '\\' && i+1 < len(content) {
			i++
			value.WriteByte(content[i])
			continue
		}
		if content[i] == quote {
			return value.String(), i + 1, true
		}
		value.WriteByte(content[i])
	}
	return "", len(content), false
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

type inspectionContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r inspectionContextReader) Read(p []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
	}
	return r.reader.Read(p)
}

// archiveByteLimiter permits one byte beyond the configured limit so callers
// can distinguish an exact-size archive from a response that continued past it.
type archiveByteLimiter struct {
	reader    io.Reader
	remaining int64
	n         int64
}

func (r *archiveByteLimiter) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errArchiveLimitExceeded
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.n += int64(n)
	r.remaining -= int64(n)
	return n, err
}

func maxInt64(value, fallback int64) int64 {
	if value > 0 {
		return value
	}
	return fallback
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
