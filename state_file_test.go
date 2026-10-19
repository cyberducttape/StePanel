package stepanel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONStateLoadersEnforceReadTimeLimits(t *testing.T) {
	tests := []struct {
		name  string
		limit int64
		open  func(string) error
		want  string
	}{
		{"account", 1 << 20, func(path string) error { _, err := OpenAccountStore(path); return err }, "account state exceeds 1 MiB"},
		{"domain claims", maxDomainClaimStateBytes, func(path string) error { _, err := OpenDomainClaimStore(path); return err }, "domain claim state exceeds 4 MiB"},
		{"routes", 4 << 20, func(path string) error { _, err := OpenRouteStore(path); return err }, "route state exceeds 4 MiB"},
		{"backup schedules", 1 << 20, func(path string) error { _, err := openBackupSchedules(path); return err }, "backup schedule state exceeds 1 MiB"},
		{"resources", 4 << 20, func(path string) error { _, err := OpenResourceStore(path); return err }, "resource state exceeds 4 MiB"},
		{"PHP profiles", 4 << 20, func(path string) error { _, err := OpenPHPProfileStore(path); return err }, "PHP profile state exceeds 4 MiB"},
		{"SSH access", 4 << 20, func(path string) error { _, err := OpenSiteAccessStore(path); return err }, "SSH access state exceeds 4 MiB"},
		{"Redis allocations", 4 << 20, func(path string) error { _, err := OpenRedisAllocationStore(path); return err }, "Redis allocation state exceeds 4 MiB"},
		{"workers", 4 << 20, func(path string) error { _, err := OpenWorkerStore(path); return err }, "worker state exceeds 4 MiB"},
		{"tasks", 4 << 20, func(path string) error { _, err := OpenTaskStore(path); return err }, "task state exceeds 4 MiB"},
		{"Composer", 4 << 20, func(path string) error { _, err := OpenComposerStore(path); return err }, "Composer state exceeds 4 MiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			// Create a sparse file without allocating its full logical size in
			// memory.
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(test.limit + 1); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if err := test.open(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("oversized state error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReadStateFileBoundsAllocationAndPreservesNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if _, err := readStateFile(path, 8); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v, want os.ErrNotExist", err)
	}
	if err := os.WriteFile(path, []byte("12345678"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := readStateFile(path, 8)
	if err != nil || string(data) != "12345678" {
		t.Fatalf("bounded read = %q, %v", data, err)
	}
	if err := os.Truncate(path, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := readStateFile(path, 8); !errors.Is(err, errStateFileTooLarge) {
		t.Fatalf("oversized read error = %v, want errStateFileTooLarge", err)
	}
}
