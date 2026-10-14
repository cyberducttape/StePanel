package stepanel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
