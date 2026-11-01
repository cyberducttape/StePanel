package rootbroker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"golang.org/x/sys/unix"
)

// sealReadBatch bounds how many directory entries are held at once. Together
// with one open descriptor per directory level, the walk's memory is
// proportional to tree depth, not to the number of entries.
const sealReadBatch = 256

// maxSealDepth bounds directory nesting (and therefore open descriptors).
const maxSealDepth = 1024

// sealVisitHook, when set by tests, is called for every visited entry.
var sealVisitHook func()

// sealApplyHook, when set by tests, runs before each permission change and
// may fail it, to exercise interruption of the apply pass.
var sealApplyHook func(rel string) error

// secureSealTree seals only regular files and directories reached through
// descriptor-relative, no-symlink opens, so a customer cannot replace a
// checked path with a symlink between validation and mutation.
//
// It walks the tree twice without retaining it: the first pass validates
// every entry, so a symlink or special file present when the seal starts
// refuses it before anything changes; the second pass reopens each entry
// with openat2 (RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS), re-checks its type on
// the opened descriptor and fchmods it. Entries are opened O_NONBLOCK, so a
// FIFO swapped in between the passes cannot block the broker. Both passes
// honour ctx.
//
// The seal is not atomic. If the apply pass is interrupted (cancellation, a
// failed fchmod, or an unsafe entry created after validation), entries
// already visited keep their sealed mode and the rest keep their previous
// one; no other mode is ever written, and nothing outside the tree changes.
// The target state depends only on each entry's type (directories 0750,
// files 0640), so the seal is convergent: running it again completes it
// from any partial state. Callers own recovery through their lifecycle
// journals: a release activation interrupted before its journal commits is
// rolled back to the previous release on startup, and site creation,
// restore and promotion fail and discard or roll back the tree on a seal
// error.
func secureSealTree(ctx context.Context, root string) error {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open seal root: %w", err)
	}
	defer unix.Close(rootFD)
	if err := sealPass(ctx, rootFD, false); err != nil {
		return err
	}
	return sealPass(ctx, rootFD, true)
}

func sealPass(ctx context.Context, rootFD int, apply bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(rootFD, &stat); err != nil {
		return fmt.Errorf("stat seal root: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("seal root is not a directory")
	}
	if apply {
		if err := sealEntry(rootFD, unix.S_IFDIR, "."); err != nil {
			return err
		}
	}
	// Each pass reads the root through a fresh open file description: a
	// dup would share the directory offset and start the second pass at EOF.
	dirFD, err := unix.Openat2(rootFD, ".", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return fmt.Errorf("reopen seal root: %w", err)
	}
	return sealDirectory(ctx, dirFD, ".", 1, apply)
}

// sealDirectory visits the entries of the directory open on dirFD, which it
// takes ownership of and closes. It reads names only: d_type is unreliable
// (filesystems may report DT_UNKNOWN), and Go's ReadDir resolves an unknown
// type with a path lstat relative to the working directory, which would skip
// or misclassify entries. Every entry is classified by a descriptor-relative
// fstatat instead.
func sealDirectory(ctx context.Context, dirFD int, rel string, depth int, apply bool) error {
	dir := os.NewFile(uintptr(dirFD), rel)
	if dir == nil {
		unix.Close(dirFD)
		return fmt.Errorf("open seal directory %q", rel)
	}
	defer dir.Close()
	if depth > maxSealDepth {
		return fmt.Errorf("site tree is nested deeper than %d directories at %q", maxSealDepth, rel)
	}
	for {
		names, err := dir.Readdirnames(sealReadBatch)
		for _, name := range names {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if sealVisitHook != nil {
				sealVisitHook()
			}
			if err := sealChild(ctx, dirFD, name, path.Join(rel, name), depth, apply); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read seal directory %q: %w", rel, err)
		}
		if len(names) == 0 {
			return nil
		}
	}
}

// sealClassify returns the file type of name in dirFD without following a
// symlink, refusing anything but a regular file or directory.
func sealClassify(dirFD int, name, rel string) (uint32, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return 0, fmt.Errorf("stat seal entry %q: %w", rel, err)
	}
	switch mode := stat.Mode & unix.S_IFMT; mode {
	case unix.S_IFREG, unix.S_IFDIR:
		return mode, nil
	case unix.S_IFLNK:
		return 0, fmt.Errorf("refusing to seal symlink %q", rel)
	default:
		return 0, fmt.Errorf("unsupported seal entry type %q", rel)
	}
}

func sealChild(ctx context.Context, dirFD int, name, rel string, depth int, apply bool) error {
	kind, err := sealClassify(dirFD, name, rel)
	if err != nil {
		return err
	}
	// Validation needs only the type of a non-directory; avoid opening it.
	if !apply && kind == unix.S_IFREG {
		return nil
	}
	fd, err := unix.Openat2(dirFD, name, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return fmt.Errorf("refusing to seal symlink %q", rel)
		}
		return fmt.Errorf("open seal entry %q: %w", rel, err)
	}
	// Re-check the type on the opened descriptor: the entry may have been
	// replaced since it was classified.
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return fmt.Errorf("stat seal entry %q: %w", rel, err)
	}
	mode := stat.Mode & unix.S_IFMT
	if mode != unix.S_IFDIR && mode != unix.S_IFREG {
		unix.Close(fd)
		return fmt.Errorf("unsupported seal entry type %q", rel)
	}
	if apply {
		if err := sealEntry(fd, mode, rel); err != nil {
			unix.Close(fd)
			return err
		}
	}
	if mode == unix.S_IFDIR {
		return sealDirectory(ctx, fd, rel, depth+1, apply)
	}
	return unix.Close(fd)
}

func sealEntry(fd int, mode uint32, rel string) error {
	if sealApplyHook != nil {
		if err := sealApplyHook(rel); err != nil {
			return fmt.Errorf("chmod seal entry %q: %w", rel, err)
		}
	}
	permissions := uint32(0o640)
	if mode == unix.S_IFDIR {
		permissions = 0o750
	}
	if err := unix.Fchmod(fd, permissions); err != nil {
		return fmt.Errorf("chmod seal entry %q: %w", rel, err)
	}
	return nil
}

// SecureSealSiteTree applies the broker's descriptor-relative public-tree
// permission contract for offline identity recovery tooling.
func SecureSealSiteTree(ctx context.Context, root string) error {
	return secureSealTree(ctx, root)
}
