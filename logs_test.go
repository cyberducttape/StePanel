package stepanel

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenManagedSiteLogRejectsSymlinkAndReadsRegularFile(t *testing.T) {
	web := t.TempDir()
	logs := filepath.Join(web, "sites", "site1", "logs")
	if err := os.MkdirAll(logs, 0700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(logs, "access.log")
	if err := os.WriteFile(want, []byte("safe\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openManagedSiteLog(web, "site1", "access.log")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(want); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, want); err != nil {
		t.Fatal(err)
	}
	if file, err := openManagedSiteLog(web, "site1", "access.log"); err == nil {
		_ = file.Close()
		t.Fatal("symlinked log file was opened")
	}
}

func BenchmarkReadLogTailFiltered16MiB(b *testing.B) {
	path := filepath.Join(b.TempDir(), "filtered.log")
	line := []byte("INFO " + strings.Repeat("x", 4090) + "\n")
	content := bytes.Repeat(line, int(maxFilteredLogBytes)/len(line)+1)
	if err := os.WriteFile(path, content, 0600); err != nil {
		b.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()
	b.SetBytes(maxFilteredLogBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := readLogTail(file, 200, maxFilteredLogBytes, "not-present"); err != nil {
			b.Fatal(err)
		}
	}
}

func TestReadLogTailReturnsRecentLinesWithoutReadingWholeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	content := strings.Repeat("old entry\n", 2<<20)
	content += "recent one\nrecent two\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lines, err := readLogTail(file, 2, 1024, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lines, "|") != "recent one|recent two" {
		t.Fatalf("tail = %#v", lines)
	}
}

func TestReadLogTailBoundsLongLinesAndFiltersRecentMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.log")
	content := strings.Repeat("x", 2048) + "\nmatch earlier\nno\nMATCH latest\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lines, err := readLogTail(file, 1, 4096, "match")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "MATCH latest" {
		t.Fatalf("filtered tail = %#v", lines)
	}
	lines, err = readLogTail(file, 10, 128, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("bounded tail lines = %d, want 3", len(lines))
	}
}
