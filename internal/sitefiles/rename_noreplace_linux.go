//go:build linux

package sitefiles

import (
	"os"
	"path"

	"golang.org/x/sys/unix"
)

// renameNoReplace publishes a rename relative to already-open, os.Root-bound
// parent directories. RENAME_NOREPLACE makes the destination check and
// publication one atomic operation; unsupported kernels/filesystems fail
// closed instead of falling back to an overwriting rename.
func renameNoReplace(root *os.Root, old, new string) error {
	oldParent, err := root.OpenRoot(path.Dir(old))
	if err != nil {
		return err
	}
	defer oldParent.Close()
	newParent, err := root.OpenRoot(path.Dir(new))
	if err != nil {
		return err
	}
	defer newParent.Close()

	oldDir, err := oldParent.Open(".")
	if err != nil {
		return err
	}
	defer oldDir.Close()
	newDir, err := newParent.Open(".")
	if err != nil {
		return err
	}
	defer newDir.Close()

	return unix.Renameat2(int(oldDir.Fd()), path.Base(old), int(newDir.Fd()), path.Base(new), unix.RENAME_NOREPLACE)
}
