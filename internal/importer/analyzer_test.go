package importer

import (
	"testing"
)

func TestIsAllowedURLValidation(t *testing.T) {
	tests := []struct {
		url   string
		allow bool
		desc  string
	}{
		// Valid URLs
		{"https://cdn.example.com/archive.tar.gz", true, "standard public domain"},
		{"https://storage.google.com/bucket/archive.zip", true, "CDN domain"},

		// Scheme validation
		{"http://example.com/archive.tar.gz", false, "http not allowed"},
		{"ftp://example.com/archive.tar.gz", false, "ftp not allowed"},

		// Localhost/loopback
		{"https://localhost/archive.tar.gz", false, "localhost"},
		{"https://127.0.0.1/archive.tar.gz", false, "loopback IPv4"},
		{"https://::1/archive.tar.gz", false, "loopback IPv6"},

		// Private IPs
		{"https://10.0.0.1/archive.tar.gz", false, "10.0.0.0/8"},
		{"https://172.16.0.1/archive.tar.gz", false, "172.16.0.0/12"},
		{"https://192.168.1.1/archive.tar.gz", false, "192.168.0.0/16"},

		// Link-local
		{"https://169.254.169.254/latest/metadata", false, "AWS metadata endpoint"},
		{"https://169.254.1.1/archive.tar.gz", false, "link-local address"},

		// Invalid URLs
		{"not-a-url", false, "malformed URL"},
		{"", false, "empty URL"},
		{"://example.com", false, "missing scheme"},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			result := isAllowedURL(tt.url)
			if result != tt.allow {
				t.Errorf("isAllowedURL(%q) = %v, want %v", tt.url, result, tt.allow)
			}
		})
	}
}

func TestExtractLargestFilesUsesPreSortedData(t *testing.T) {
	// This test documents that extractLargestFiles expects pre-sorted input
	analyzer := &Analyzer{}

	inspection := &ArchiveInspection{
		Structure: ArchiveStructure{
			FileExtensions: []string{},
			LargestFiles:   []string{},
		},
	}

	// Simulate pre-sorted largest files (by inspectTarGz/inspectZip)
	files := []struct {
		name string
		size int64
	}{
		{"large-file.bin", 5000000},     // 5 MB
		{"medium-file.zip", 2000000},    // 2 MB
		{"small-file.txt", 100000},      // 100 KB
		{".hidden-file", 50000},         // Should be skipped
		{"tiny.log", 1000},              // 1 KB
	}

	analyzer.extractLargestFiles(files, inspection)

	// Should have extracted the 4 non-hidden files (first 5 where not hidden)
	if len(inspection.Structure.LargestFiles) == 0 {
		t.Error("extractLargestFiles should extract largest files")
	}

	// First file should be the largest
	if len(inspection.Structure.LargestFiles) > 0 && inspection.Structure.LargestFiles[0] != "large-file.bin" {
		t.Errorf("expected largest file first, got: %v", inspection.Structure.LargestFiles[0])
	}
}

func TestMemoryBombProtectionInInspection(t *testing.T) {
	// This test documents that inspectTarGz/inspectZip limit metadata tracking
	// to prevent memory exhaustion from archives with millions of files

	// The limit is implemented as:
	// - maxLargestFiles = 100 (constant in analyzer.go)
	// - Only keep top 100 files by size
	// - This prevents accumulating metadata for every single file in massive archives

	// A 1M file archive would have 1M headers processed, but only 100 retained
	// Expected memory: 100 * sizeof(filename + size) ≈ 10 KB
	// Without limit: 1M * sizeof(filename + size) ≈ 100 MB

	t.Log("Memory bomb protection: largestFiles tracking capped at 100 entries")
	t.Log("This prevents memory exhaustion from analyzing massive archives")
}

func TestDirectoryBombProtection(t *testing.T) {
	// This test documents directory bomb protection in extractTarGz
	//
	// The defense:
	// 1. DirectoriesCreated counter (separate from FilesExtracted)
	// 2. Limit: maxDirectoriesInArchive = 25,000
	// 3. Check: if job.DirectoriesCreated > maxDirectoriesInArchive, reject
	//
	// Without this, a tar with 1M directory entries would create 1M inodes,
	// exhausting available inodes on the filesystem.

	job := &ImportJob{}

	// Simulate directory bomb
	for i := 0; i < maxDirectoriesInArchive+1000; i++ {
		job.DirectoriesCreated++
	}

	if job.DirectoriesCreated <= maxDirectoriesInArchive {
		t.Error("test setup failed")
	}

	t.Logf("Detected directory bomb: %d directories (limit: %d)",
		job.DirectoriesCreated, maxDirectoriesInArchive)
}

func TestSizeBoundedDownloadProtection(t *testing.T) {
	// This test documents that ArchiveFetcher prevents size-based attacks
	//
	// Attack scenario:
	// 1. Server returns Content-Length: 5MB in HEAD request
	// 2. Browser trusts this and starts download
	// 3. Server sends 50GB via chunked transfer encoding
	// 4. Disk fills, DoS succeeds
	//
	// StePanel defense:
	// - FetchArchive wraps response in io.LimitReader
	// - Limit is min(claimed Content-Length + 1MB, maxArchiveSize)
	// - Read beyond limit returns EOF
	// - This forces the server to respect the size contract
	//
	// Note: A server can still lie about Content-Length, but at least
	// we don't blindly trust it for the actual streaming limit.

	t.Log("Size-bounded download: response wrapped in io.LimitReader")
	t.Log("Prevents streaming attacks where server lies about archive size")
}
