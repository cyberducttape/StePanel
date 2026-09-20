package helper

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

const MaxCommandOutput = 64 << 10
const HelperCommandTimeout = 2 * time.Minute

type BoundedBuffer struct {
	data  []byte
	limit int
}

func (b *BoundedBuffer) Write(p []byte) (int, error) {
	limit := b.limit
	if limit <= 0 {
		limit = MaxCommandOutput
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

func RunBoundedCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	return RunBoundedCommandLimit(ctx, cmd, MaxCommandOutput)
}

// RunBoundedCommandLimit enforces ctx directly, independent of how cmd was
// constructed. All current callers build cmd with exec.CommandContext, which
// already enforces the deadline—but that is a caller convention. This function
// now verifies it, so a cmd built with plain exec.Command cannot silently
// ignore its deadline here.
func RunBoundedCommandLimit(ctx context.Context, cmd *exec.Cmd, limit int) ([]byte, error) {
	var output BoundedBuffer
	output.limit = limit
	if limit > MaxCommandOutput {
		output.data = make([]byte, 0, min(limit, 64<<10))
	}
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return output.data, fmt.Errorf("%w: %s", err, string(output.data))
	}

	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
	}()

	select {
	case err := <-waitErr:
		if err != nil {
			return output.data, fmt.Errorf("%w: %s", err, string(output.data))
		}
		return output.data, nil
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-waitErr
		return output.data, fmt.Errorf("%w: %s", ctx.Err(), string(output.data))
	}
}

func RunBoundedCommandInput(ctx context.Context, cmd *exec.Cmd, input io.Reader) ([]byte, error) {
	cmd.Stdin = input
	return RunBoundedCommand(ctx, cmd)
}

// HelperCommand runs narrowly scoped privileged helpers through sudo when the
// packaged installation configures it. Development and test configurations can
// leave Sudo empty and execute their helper directly.
func HelperCommand(sudo string, path string, args ...string) *exec.Cmd {
	if sudo == "" {
		return exec.Command(path, args...)
	}
	return exec.Command(sudo, append([]string{"--non-interactive", path}, args...)...)
}

func HelperCommandContext(ctx context.Context, sudo string, path string, args ...string) *exec.Cmd {
	if sudo == "" {
		return exec.CommandContext(ctx, path, args...)
	}
	return exec.CommandContext(ctx, sudo, append([]string{"--non-interactive", path}, args...)...)
}

// SafePath joins path components beneath root and rejects absolute components,
// traversal, and symlinked parents. Callers should use this for any path that
// contains request data or persisted metadata.
func SafePath(root string, parts ...string) (string, error) {
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
	if err := EnsureInside(root, target); err != nil {
		return "", err
	}
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path component is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return target, nil
}

// EnsureInside verifies that target is within root and that no parent in the
// path from root to target is a symlink.
func EnsureInside(root, target string) error {
	r, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	t, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if t != r && !strings.HasPrefix(t, r+string(os.PathSeparator)) {
		return errors.New("path escapes configured root")
	}
	return RejectSymlinkParents(t, r)
}

// RunHelperCommand executes a privileged helper with a bounded lifetime and
// bounded output. Every request-facing helper invocation should use this
// wrapper so a wedged systemd/webserver/database helper cannot exhaust worker
// capacity or prevent graceful shutdown.
func RunHelperCommand(ctx context.Context, sudo, path string, args ...string) error {
	if path == "" {
		return errors.New("helper is not configured")
	}
	commandCtx, cancel := context.WithTimeout(ctx, HelperCommandTimeout)
	defer cancel()
	_, err := RunBoundedCommand(commandCtx, HelperCommandContext(commandCtx, sudo, path, args...))
	return err
}

func SiteHelper(sudo, siteCtl, action, site string) error {
	if siteCtl == "" {
		return nil
	}
	return RunHelperCommand(context.Background(), sudo, siteCtl, action, site)
}

// OpenRegularNoFollow opens a file descriptor without following symlinks and
// verifies that it is still the same inode observed during a directory walk.
// This closes the validation/use race for site files, which are writable by
// separate site identities while backups and scans run.
func OpenRegularNoFollow(path string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
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
	if !info.Mode().IsRegular() || expected != nil && !SameFileInfo(expected, info) {
		_ = file.Close()
		return nil, nil, errors.New("file changed or is not a regular file")
	}
	return file, info, nil
}

// OpenWriteNoFollow opens the destination itself without following a symlink.
func OpenWriteNoFollow(path string, mode os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func SameFileInfo(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return false
	}
	aStat, aOK := a.Sys().(*syscall.Stat_t)
	bStat, bOK := b.Sys().(*syscall.Stat_t)
	if aOK && bOK {
		return aStat.Dev == bStat.Dev && aStat.Ino == bStat.Ino && aStat.Ctim == bStat.Ctim
	}
	return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

func WriteAtomic(path string, data []byte, mode os.FileMode) error {
	return statefile.WriteAtomic(path, data, mode)
}

func RejectSymlinkParents(path, root string) error {
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

func AcquireProcessLock(path string) (*os.File, error) {
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
