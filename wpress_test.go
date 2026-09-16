package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSQLIdent(t *testing.T) {
	cases := map[string]string{
		"simple":    "`simple`",
		"with`tick": "`with``tick`",
		"":          "``",
	}
	for input, want := range cases {
		if got := sqlIdent(input); got != want {
			t.Errorf("sqlIdent(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "present.txt")
	if err := os.WriteFile(present, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if !fileExists(present) {
		t.Error("fileExists reported false for an existing file")
	}
	if fileExists(filepath.Join(dir, "missing.txt")) {
		t.Error("fileExists reported true for a missing file")
	}
}

func TestCopyFilePreservesContentAndMode(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	if err := os.WriteFile(src, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst.txt")
	if err := copyFile(src, dst, 0640); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Fatalf("copied content = %q, want %q", data, "payload")
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("copied file mode = %v, want 0640", info.Mode().Perm())
	}
	if err := copyFile(filepath.Join(dir, "missing.txt"), filepath.Join(dir, "dst2.txt"), 0640); err == nil {
		t.Fatal("copyFile succeeded on a missing source")
	}
}

func TestFindWordPressRoot(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "wp-content-export", "sitename")
	if err := os.MkdirAll(nested, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "database.sql"), []byte("--"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "wp-config.php"), []byte("<?php"), 0600); err != nil {
		t.Fatal(err)
	}
	if found := findWordPressRoot(root); found != nested {
		t.Fatalf("findWordPressRoot = %q, want %q", found, nested)
	}

	empty := t.TempDir()
	if found := findWordPressRoot(empty); found != "" {
		t.Fatalf("findWordPressRoot on an archive with no WordPress payload = %q, want empty", found)
	}
}

// TestValidateExtractionLayoutRejectsEscapedOutput is the archive/path-safety
// case the WPress extraction step depends on: a compromised or malicious
// wpress-extract binary that writes files outside the directory it was
// invoked to extract into must be caught, not silently trusted.
func TestValidateExtractionLayoutRejectsEscapedOutput(t *testing.T) {
	stage := t.TempDir()
	extracted := filepath.Join(stage, "expected-output")
	if err := os.MkdirAll(extracted, 0750); err != nil {
		t.Fatal(err)
	}
	if err := validateExtractionLayout(stage, extracted); err != nil {
		t.Fatalf("validateExtractionLayout rejected a clean extraction: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(stage, "tmp"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := validateExtractionLayout(stage, extracted); err != nil {
		t.Fatalf("validateExtractionLayout rejected the expected tmp directory: %v", err)
	}

	if err := os.WriteFile(filepath.Join(stage, "escaped.php"), []byte("<?php"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateExtractionLayout(stage, extracted); err == nil {
		t.Fatal("validateExtractionLayout accepted an entry written outside the designated output directory")
	}
}
