package main

import (
	"context"
	"errors"
	"fmt"
	statefile "github.com/itchyitchy123/StePanel/internal/state"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const maxCommandOutput = 64 << 10
const helperCommandTimeout = 2 * time.Minute

type boundedBuffer struct {
	data  []byte
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	limit := b.limit
	if limit <= 0 {
		limit = maxCommandOutput
	}
	if len(b.data) < limit {
		n := limit - len(b.data)
		if n > len(p) {
			n = len(p)
		}
		b.data = append(b.data, p[:n]...)
	}
	return len(p), nil
}

func runBoundedCommand(_ context.Context, cmd *exec.Cmd) ([]byte, error) {
	return runBoundedCommandLimit(cmd, maxCommandOutput)
}

func runBoundedCommandLimit(cmd *exec.Cmd, limit int) ([]byte, error) {
	var output boundedBuffer
	output.limit = limit
	if limit > maxCommandOutput {
		output.data = make([]byte, 0, min(limit, 64<<10))
	}
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if err != nil {
		return output.data, fmt.Errorf("%w: %s", err, string(output.data))
	}
	return output.data, nil
}

func runBoundedCommandInput(ctx context.Context, cmd *exec.Cmd, input io.Reader) ([]byte, error) {
	cmd.Stdin = input
	return runBoundedCommand(ctx, cmd)
}

// helperCommand runs narrowly scoped privileged helpers through sudo when the
// packaged installation configures it. Development and test configurations can
// leave Sudo empty and execute their helper directly.
func helperCommand(cfg Config, path string, args ...string) *exec.Cmd {
	if cfg.Sudo == "" {
		return exec.Command(path, args...)
	}
	return exec.Command(cfg.Sudo, append([]string{"--non-interactive", path}, args...)...)
}

func helperCommandContext(ctx context.Context, cfg Config, path string, args ...string) *exec.Cmd {
	if cfg.Sudo == "" {
		return exec.CommandContext(ctx, path, args...)
	}
	return exec.CommandContext(ctx, cfg.Sudo, append([]string{"--non-interactive", path}, args...)...)
}

// safePath joins path components beneath root and rejects absolute components,
// traversal, and symlinked parents. Callers should use this for any path that
// contains request data or persisted metadata.
func safePath(root string, parts ...string) (string, error) {
	if root == "" {
		return "", errors.New("path root is empty")
	}
	target := root
	for _, part := range parts {
		if part == "" || filepath.IsAbs(part) {
			return "", errors.New("path component is invalid")
		}
		target = filepath.Join(target, part)
	}
	if err := ensureInside(root, target); err != nil {
		return "", err
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path component is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return target, nil
}

// runHelperCommand executes a privileged helper with a bounded lifetime and
// bounded output. Every request-facing helper invocation should use this
// wrapper so a wedged systemd/webserver/database helper cannot exhaust worker
// capacity or prevent graceful shutdown.
func runHelperCommand(ctx context.Context, cfg Config, path string, args ...string) error {
	if path == "" {
		return errors.New("helper is not configured")
	}
	commandCtx, cancel := context.WithTimeout(ctx, helperCommandTimeout)
	defer cancel()
	_, err := runBoundedCommand(commandCtx, helperCommandContext(commandCtx, cfg, path, args...))
	return err
}

func siteHelper(cfg Config, action, site string) error {
	if cfg.SiteCtl == "" {
		return nil
	}
	return runHelperCommand(context.Background(), cfg, cfg.SiteCtl, action, site)
}

// openRegularNoFollow opens a file descriptor without following symlinks and
// verifies that it is still the same inode observed during a directory walk.
// This closes the validation/use race for site files, which are writable by
// separate site identities while backups and scans run.
func openRegularNoFollow(path string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || expected != nil && !sameFileInfo(expected, info) {
		_ = file.Close()
		return nil, nil, errors.New("file changed or is not a regular file")
	}
	return file, info, nil
}

// openWriteNoFollow opens the destination itself without following a symlink.
func openWriteNoFollow(path string, mode os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func sameFileInfo(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return false
	}
	aStat, aOK := a.Sys().(*syscall.Stat_t)
	bStat, bOK := b.Sys().(*syscall.Stat_t)
	if aOK && bOK {
		// Inodes can be reused immediately after an unlink. ctime changes for a
		// replacement (and for any metadata/content mutation), so retain it with
		// the device/inode identity captured by the directory walk.
		return aStat.Dev == bStat.Dev && aStat.Ino == bStat.Ino && aStat.Ctim == bStat.Ctim
	}
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	return statefile.WriteAtomic(path, data, mode)
}

func rejectSymlinkParents(path, root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return errors.New("path escapes configured root")
	}
	if info, statErr := os.Lstat(root); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("configured root is a symlink")
	}
	rel, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return err
	}
	current := root
	if rel != "." {
		for _, part := range strings.Split(rel, string(os.PathSeparator)) {
			current = filepath.Join(current, part)
			info, statErr := os.Lstat(current)
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return statErr
			}
			if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
				return errors.New("destination contains a symlinked parent")
			}
		}
	}
	return nil
}

func acquireProcessLock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), path)
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("another StePanel instance is already running")
	}
	return lock, nil
}
