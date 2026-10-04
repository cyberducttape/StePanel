package stepanel

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishStagedDirectoryRollsBackWhenParentSyncFails(t *testing.T) {
	root := t.TempDir()
	staged := filepath.Join(root, ".backup-staged")
	final := filepath.Join(root, "final")
	if err := os.Mkdir(staged, 0700); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected ENOSPC")
	calls := 0
	original := publishSyncDirectory
	publishSyncDirectory = func(string) error {
		calls++
		if calls == 1 {
			return injected
		}
		return nil
	}
	defer func() { publishSyncDirectory = original }()

	err := publishStagedDirectory(staged, final, root)
	if !errors.Is(err, injected) {
		t.Fatalf("publish error = %v, want injected sync failure", err)
	}
	var indeterminate *PublishIndeterminateError
	if errors.As(err, &indeterminate) {
		t.Fatalf("successful rollback reported as indeterminate: %v", err)
	}
	if _, err := os.Stat(final); !os.IsNotExist(err) {
		t.Fatalf("final path still present after rollback: %v", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("staged path not restored: %v", err)
	}
}

func TestPublishStagedDirectoryReportsIndeterminateWhenRollbackFails(t *testing.T) {
	root := t.TempDir()
	staged := filepath.Join(root, ".backup-staged")
	final := filepath.Join(root, "final")
	if err := os.Mkdir(staged, 0700); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected EIO")
	original := publishSyncDirectory
	publishSyncDirectory = func(string) error {
		// Occupy the staged name with a non-empty directory so the reverse
		// rename cannot succeed.
		if err := os.MkdirAll(filepath.Join(staged, "blocker"), 0700); err != nil {
			t.Fatal(err)
		}
		return injected
	}
	defer func() { publishSyncDirectory = original }()

	err := publishStagedDirectory(staged, final, root)
	var indeterminate *PublishIndeterminateError
	if !errors.As(err, &indeterminate) {
		t.Fatalf("publish error = %v, want *PublishIndeterminateError", err)
	}
	if indeterminate.Path != final || !errors.Is(err, injected) || indeterminate.RollbackErr == nil {
		t.Fatalf("indeterminate error is incomplete: %+v", indeterminate)
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatalf("indeterminate error must name a path that still exists: %v", err)
	}
}
