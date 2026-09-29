// Package state contains small durability primitives shared by control-plane
// state stores. It intentionally does not know about StePanel domains.
package state

import (
	"fmt"
	"os"
	"path/filepath"
)

var (
	stateMkdirAll   = os.MkdirAll
	stateCreateTemp = os.CreateTemp
	stateRename     = os.Rename
	stateOpen       = os.Open
	stateChmod      = func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) }
	stateWrite      = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
	stateSync       = func(file *os.File) error { return file.Sync() }
	stateClose      = func(file *os.File) error { return file.Close() }
)

// WriteAtomic writes data to path with the requested permissions, fsyncs the
// file, atomically replaces the destination, and fsyncs its parent directory.
// The temporary file is removed on every error path.
func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	root := filepath.Dir(path)
	if err := stateMkdirAll(root, 0750); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	tmp, err := stateCreateTemp(root, ".stepanel-state-*")
	if err != nil {
		return fmt.Errorf("create temporary state file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := stateChmod(tmp, mode); err != nil {
		_ = stateClose(tmp)
		return fmt.Errorf("set state file permissions: %w", err)
	}
	if _, err := stateWrite(tmp, data); err != nil {
		_ = stateClose(tmp)
		return fmt.Errorf("write state file: %w", err)
	}
	if err := stateSync(tmp); err != nil {
		_ = stateClose(tmp)
		return fmt.Errorf("sync state file: %w", err)
	}
	if err := stateClose(tmp); err != nil {
		return fmt.Errorf("close state file: %w", err)
	}
	if err := stateRename(tmpName, path); err != nil {
		return fmt.Errorf("replace state file: %w", err)
	}
	dir, err := stateOpen(root)
	if err != nil {
		return fmt.Errorf("open state directory for sync: %w", err)
	}
	syncErr := stateSync(dir)
	closeErr := stateClose(dir)
	if syncErr != nil {
		return fmt.Errorf("sync state directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close state directory: %w", closeErr)
	}
	return nil
}
