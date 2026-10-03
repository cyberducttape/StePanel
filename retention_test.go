package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestCleanupImportStagesRemovesOnlyExpiredRestoreStages(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "20200101-010101-account")
	oldCPMove := filepath.Join(root, "20200101-010102-abcdefghijklmnopqrstuvwxyz012345-AbCdef012_-x")
	keep := filepath.Join(root, "20990101-010101-account")
	nonStage := filepath.Join(root, "notes")
	orphanUpload := filepath.Join(root, "upload-old.tar.gz")
	orphanWPress := filepath.Join(root, "wpress-upload-old.wpress")
	for _, path := range []string{old, oldCPMove, keep, nonStage} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{orphanUpload, orphanWPress} {
		if err := os.WriteFile(path, []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Unix(0, 0), time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{old, oldCPMove} {
		if err := os.Chtimes(path, time.Unix(0, 0), time.Unix(0, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupImportStages(root, time.Hour); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{old, oldCPMove} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("old stage still exists: %s: %v", path, err)
		}
	}
	for _, path := range []string{orphanUpload, orphanWPress} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("orphan upload still exists: %s: %v", path, err)
		}
	}
	for _, path := range []string{keep, nonStage} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unexpected removal of %s: %v", path, err)
		}
	}
}

// Every scratch tree created under the import root must be one that
// CleanupImportStages recognises, or a crash leaks it forever.
func TestEveryImportRootTempDirIsCleaned(t *testing.T) {
	pattern := regexp.MustCompile(`MkdirTemp\([a-zA-Z.]*ImportRoot, "([^"]+)"\)`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(data), -1) {
			found++
			if !isTemporaryImportStage(match[1] + "123") {
				t.Errorf("%s creates import-root scratch tree %q that cleanup does not recognise", file, match[1])
			}
		}
	}
	if found == 0 {
		t.Fatal("no import-root scratch trees found; the pattern no longer matches the code")
	}
}

func TestCleanupImportStagesRemovesAbandonedScratchTreesOnly(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-orphanedStagingMinAge - time.Hour)
	for _, name := range []string{"backup-files-restore-123", "backup-rehearsal-9", "keep-me-123"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(root, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "backup-files-restore-456"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := CleanupImportStages(root, 168*time.Hour); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"backup-files-restore-123": false, "backup-rehearsal-9": false, "keep-me-123": true, "backup-files-restore-456": true} {
		_, err := os.Stat(filepath.Join(root, name))
		if exists := err == nil; exists != want {
			t.Errorf("%s exists=%v, want %v", name, exists, want)
		}
	}
	if err := CleanupImportStages(filepath.Join(root, "missing"), time.Hour); err != nil {
		t.Fatalf("missing root: %v", err)
	}
}
