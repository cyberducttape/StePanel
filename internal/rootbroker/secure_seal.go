package rootbroker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// secureSealTree seals only regular files and directories reached through a
// descriptor-relative, no-symlink path. filepath.Walk is used for enumeration
// only; the actual chmod uses openat2/fchmod so a customer cannot replace a
// checked path with a symlink between validation and mutation.
func secureSealTree(ctx context.Context, root string) error {
	paths := make([]string, 0)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to seal symlink %q", path)
		}
		paths = append(paths, path)
		return ctx.Err()
	}); err != nil {
		return err
	}

	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open seal root: %w", err)
	}
	defer unix.Close(rootFD)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fd := rootFD
		closeFD := false
		if rel != "." {
			how := &unix.OpenHow{
				Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
				Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
			}
			fd, err = unix.Openat2(rootFD, rel, how)
			if err != nil {
				return fmt.Errorf("open seal entry %q: %w", rel, err)
			}
			closeFD = true
		}
		var stat unix.Stat_t
		err = unix.Fstat(fd, &stat)
		if err == nil {
			mode := stat.Mode & unix.S_IFMT
			if mode != unix.S_IFDIR && mode != unix.S_IFREG {
				err = fmt.Errorf("unsupported seal entry type %q", rel)
			} else {
				permissions := uint32(0o640)
				if mode == unix.S_IFDIR {
					permissions = 0o750
				}
				if chmodErr := unix.Fchmod(fd, permissions); chmodErr != nil {
					err = fmt.Errorf("chmod seal entry %q: %w", rel, chmodErr)
				}
			}
		}
		if closeFD {
			closeErr := unix.Close(fd)
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}
