package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicReplacesAndProtectsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	if err := WriteAtomic(path, []byte("new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Fatalf("state = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("state mode = %o, want 600", info.Mode().Perm())
	}
	if err := WriteAtomic(path, []byte("replacement\n"), 0640); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "replacement\n" {
		t.Fatalf("replacement = %q err=%v", data, err)
	}
}

func TestWriteAtomicFailurePreservesDestinationAndCleansTemporaryFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}

	// A directory at the destination forces Rename to fail after the temporary
	// file has been fully written, while keeping the parent directory usable.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(path, []byte("new\n"), 0600); err == nil {
		t.Fatal("expected replacement failure")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" || !entries[0].IsDir() {
		t.Fatalf("destination directory or temporary files changed: %#v", entries)
	}
}
