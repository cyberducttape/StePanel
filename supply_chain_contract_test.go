package stepanel

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every script StePanel ships that downloads code or archives from the
// internet must verify what it fetched against a pinned SHA-256 before using
// it. Lab and test scripts, which only talk to a local panel, are excluded.
func TestShippedDownloadsAreChecksumVerified(t *testing.T) {
	download := regexp.MustCompile(`\b(curl|wget)\b[^\n]*https://`)
	files := []string{"install.sh", "scripts/go-toolchain.sh"}
	for _, dir := range []string{"deploy/integrations", "deploy/lib"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				files = append(files, filepath.Join(dir, entry.Name()))
			}
		}
	}
	checked := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		var downloads []string
		for _, line := range strings.Split(text, "\n") {
			if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "#") && download.MatchString(line) {
				downloads = append(downloads, trimmed)
			}
		}
		if len(downloads) == 0 {
			continue
		}
		checked++
		if !strings.Contains(text, "sha256sum --check") {
			t.Errorf("%s downloads without verifying a pinned SHA-256:\n  %s", file, strings.Join(downloads, "\n  "))
		}
	}
	if checked == 0 {
		t.Fatal("no shipped downloads were found; the contract no longer recognizes them")
	}
}
