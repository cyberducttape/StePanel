package state

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func preserveStateHooks(t *testing.T) {
	t.Helper()
	mkdirAll, createTemp, remove, rename, open := stateMkdirAll, stateCreateTemp, stateRemove, stateRename, stateOpen
	chmod, write, syncFile, closeFile := stateChmod, stateWrite, stateSync, stateClose
	t.Cleanup(func() {
		stateMkdirAll, stateCreateTemp, stateRemove, stateRename, stateOpen = mkdirAll, createTemp, remove, rename, open
		stateChmod, stateWrite, stateSync, stateClose = chmod, write, syncFile, closeFile
	})
}

func TestRemoveDurableRemovesAndSyncsParent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "journal.json")
	if err := os.WriteFile(path, []byte("journal"), 0600); err != nil {
		t.Fatal(err)
	}

	synced := 0
	preserveStateHooks(t)
	stateSync = func(*os.File) error {
		synced++
		return nil
	}
	if err := RemoveDurable(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal stat error = %v, want not-exist", err)
	}
	if synced != 1 {
		t.Fatalf("directory syncs = %d, want 1", synced)
	}

	// Cleanup is idempotent and still syncs the containing directory.
	if err := RemoveDurable(path); err != nil {
		t.Fatal(err)
	}
	if synced != 2 {
		t.Fatalf("directory syncs after missing removal = %d, want 2", synced)
	}
}

func TestRemoveDurableReportsDurabilityFailures(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "journal.json")
	if err := os.WriteFile(path, []byte("journal"), 0600); err != nil {
		t.Fatal(err)
	}

	t.Run("remove", func(t *testing.T) {
		preserveStateHooks(t)
		stateRemove = func(string) error { return errors.New("remove failed") }
		if err := RemoveDurable(path); err == nil || !strings.Contains(err.Error(), "remove failed") {
			t.Fatalf("RemoveDurable error = %v, want remove failure", err)
		}
	})
	t.Run("directory open", func(t *testing.T) {
		preserveStateHooks(t)
		stateOpen = func(string) (*os.File, error) { return nil, errors.New("open failed") }
		if err := RemoveDurable(path); err == nil || !strings.Contains(err.Error(), "open failed") {
			t.Fatalf("RemoveDurable error = %v, want directory open failure", err)
		}
	})
	t.Run("directory sync", func(t *testing.T) {
		preserveStateHooks(t)
		stateSync = func(*os.File) error { return errors.New("sync failed") }
		if err := RemoveDurable(path); err == nil || !strings.Contains(err.Error(), "sync failed") {
			t.Fatalf("RemoveDurable error = %v, want directory sync failure", err)
		}
	})
}

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

func TestWriteAtomicReportsInjectedDurabilityFailures(t *testing.T) {
	tests := []struct {
		name   string
		inject func()
	}{
		{name: "mkdir", inject: func() { stateMkdirAll = func(string, os.FileMode) error { return errors.New("mkdir failed") } }},
		{name: "create temp", inject: func() {
			stateCreateTemp = func(string, string) (*os.File, error) { return nil, errors.New("create failed") }
		}},
		{name: "chmod", inject: func() { stateChmod = func(*os.File, os.FileMode) error { return errors.New("chmod failed") } }},
		{name: "write", inject: func() { stateWrite = func(*os.File, []byte) (int, error) { return 0, errors.New("write failed") } }},
		{name: "file sync", inject: func() { stateSync = func(*os.File) error { return errors.New("sync failed") } }},
		{name: "file close", inject: func() { stateClose = func(*os.File) error { return errors.New("close failed") } }},
		{name: "rename", inject: func() { stateRename = func(string, string) error { return errors.New("rename failed") } }},
		{name: "directory open", inject: func() { stateOpen = func(string) (*os.File, error) { return nil, errors.New("open failed") } }},
		{name: "directory sync", inject: func() {
			calls := 0
			stateSync = func(*os.File) error {
				calls++
				if calls == 2 {
					return errors.New("directory sync failed")
				}
				return nil
			}
		}},
		{name: "directory close", inject: func() {
			calls := 0
			stateClose = func(*os.File) error {
				calls++
				if calls == 2 {
					return errors.New("directory close failed")
				}
				return nil
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			preserveStateHooks(t)
			test.inject()
			if err := WriteAtomic(filepath.Join(t.TempDir(), "state"), []byte("data"), 0600); err == nil {
				t.Fatal("WriteAtomic unexpectedly succeeded")
			}
		})
	}
}

func TestWriteAtomicRejectsShortWrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}

	preserveStateHooks(t)
	stateWrite = func(_ *os.File, data []byte) (int, error) {
		return len(data) - 1, nil
	}
	if err := WriteAtomic(path, []byte("new\n"), 0600); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("WriteAtomic error = %v, want io.ErrShortWrite", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old\n" {
		t.Fatalf("destination = %q, want original state", data)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("temporary files remain: %#v", entries)
	}
}
