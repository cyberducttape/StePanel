package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if _, err := ReadFileBounded(path, 8); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v, want os.ErrNotExist", err)
	}
	if err := os.WriteFile(path, []byte("12345678"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFileBounded(path, 8)
	if err != nil || string(data) != "12345678" {
		t.Fatalf("exact-bound read = %q, %v", data, err)
	}
	if err := os.Truncate(path, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFileBounded(path, 8); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized read error = %v, want ErrFileTooLarge", err)
	}
	if _, err := ReadFileBounded(filepath.Dir(path), 8); err == nil {
		t.Fatal("reading a directory unexpectedly succeeded")
	}
}

func TestReadFileBoundedRejectsInvalidLimits(t *testing.T) {
	for _, limit := range []int64{-1, int64(^uint64(0) >> 1)} {
		if _, err := ReadFileBounded("unused", limit); err == nil {
			t.Fatalf("limit %d unexpectedly succeeded", limit)
		}
	}
}
