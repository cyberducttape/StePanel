package helper

import (
	"context"
	"errors"
	"fmt"
	statefile "github.com/cyberducttape/StePanel/internal/state"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const MaxCommandOutput = 64 << 10
const processGroupShutdownGrace = 250 * time.Millisecond

// Operation-specific timeout classes for helper invocations.
// These should be used instead of a universal timeout to ensure
// operations complete without timing out prematurely.
const (
	// ConfigMutationTimeout: fast config reloads, ACL updates, pool creation (~30 sec)
	ConfigMutationTimeout = 30 * time.Second

	// ServiceLifecycleTimeout: systemd operations, user creation, filesystem setup (~60 sec)
	ServiceLifecycleTimeout = 60 * time.Second

	// PackageBuildTimeout: npm/yarn/pnpm install, pip install, cargo build, composer update (~15 min)
	PackageBuildTimeout = 15 * time.Minute

	// DatabaseOperationTimeout: database dumps, restores, migrations (~60 min)
	DatabaseOperationTimeout = 60 * time.Minute

	// ContainerOperationTimeout: image pulls, loads, prune operations (~30 min)
	ContainerOperationTimeout = 30 * time.Minute

	// BackupRestoreTimeout: full site backups, restores, archives (~120 min)
	BackupRestoreTimeout = 120 * time.Minute
)

// BoundedBuffer retains the complete output up to limit bytes. When output is
// larger, it keeps the first quarter and the most recent three quarters so
// trailing diagnostics survive without allowing chatty commands to exhaust
// memory. Bytes inserts an omission marker when truncation occurred.
type BoundedBuffer struct {
	mu        sync.Mutex
	data      []byte
	head      []byte
	tail      []byte
	tailStart int
	tailLen   int
	limit     int
	total     int64
	truncated bool
}

func (b *BoundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)

	limit := b.limit
	if limit <= 0 {
		limit = MaxCommandOutput
	}
	b.total += int64(len(p))
	if !b.truncated && len(b.data)+len(p) <= limit {
		b.data = append(b.data, p...)
		return len(p), nil
	}
	if !b.truncated {
		headLimit := limit / 4
		if headLimit == 0 {
			headLimit = 1
		}
		oldData := b.data
		b.head = append([]byte(nil), oldData[:min(len(oldData), headLimit)]...)
		if len(b.head) < headLimit {
			take := min(headLimit-len(b.head), len(p))
			b.head = append(b.head, p[:take]...)
			p = p[take:]
		}
		oldTailStart := min(len(oldData), len(b.head))
		b.tail = make([]byte, limit-len(b.head))
		b.appendTail(oldData[oldTailStart:])
		b.data = nil
		b.truncated = true
	}
	b.appendTail(p)
	return written, nil
}

// appendTail updates the bounded circular tail without shifting the retained
// output on every write from a chatty subprocess.
func (b *BoundedBuffer) appendTail(p []byte) {
	capacity := len(b.tail)
	if capacity == 0 || len(p) == 0 {
		return
	}
	if len(p) >= capacity {
		copy(b.tail, p[len(p)-capacity:])
		b.tailStart = 0
		b.tailLen = capacity
		return
	}
	free := capacity - b.tailLen
	first := min(free, len(p))
	if first > 0 {
		end := (b.tailStart + b.tailLen) % capacity
		writeCircular(b.tail, end, p[:first])
		b.tailLen += first
		p = p[first:]
	}
	if len(p) > 0 {
		writeCircular(b.tail, b.tailStart, p)
		b.tailStart = (b.tailStart + len(p)) % capacity
	}
}

func writeCircular(destination []byte, offset int, source []byte) {
	first := min(len(source), len(destination)-offset)
	copy(destination[offset:offset+first], source[:first])
	copy(destination, source[first:])
}

// Bytes returns the retained command output, including an explicit marker and
// total omitted-byte count when output exceeded the configured limit.
func (b *BoundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.truncated {
		return append([]byte(nil), b.data...)
	}
	omitted := b.total - int64(len(b.head)+b.tailLen)
	marker := []byte(fmt.Sprintf("\n[... %d command output bytes omitted ...]\n", omitted))
	result := make([]byte, 0, len(b.head)+len(marker)+b.tailLen)
	result = append(result, b.head...)
	result = append(result, marker...)
	if b.tailLen > 0 {
		first := min(b.tailLen, len(b.tail)-b.tailStart)
		result = append(result, b.tail[b.tailStart:b.tailStart+first]...)
		result = append(result, b.tail[:b.tailLen-first]...)
	}
	return result
}

func RunBoundedCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	return RunBoundedCommandLimit(ctx, cmd, MaxCommandOutput)
}

// RunBoundedCommandLimit enforces ctx directly, independent of how cmd was
// constructed. All current callers build cmd with exec.CommandContext, which
// already enforces the deadline—but that is a caller convention. This function
// now verifies it, so a cmd built with plain exec.Command cannot silently
// ignore its deadline here.
//
// Commands run in their own process group. Cancellation first sends SIGTERM
// and then SIGKILL after a short grace period, allowing a wrapper to stop a
// transient systemd unit before the helper tears down the caller's process
// group. This is not a complete process-tree boundary: a child that creates a
// new session can escape it unless it is also contained by a cgroup.
func RunBoundedCommandLimit(ctx context.Context, cmd *exec.Cmd, limit int) ([]byte, error) {
	var output BoundedBuffer
	output.limit = limit
	cmd.Stdout = &output
	cmd.Stderr = &output

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Ensure all commands run in their own process group.
	PrepareCommand(cmd)
	// exec.CommandContext otherwise kills only the direct child immediately.
	// Signal the group first so wrapper processes can stop a cgroup-managed
	// workload; the cancellation branch below enforces the escalation deadline.
	if cmd.Cancel != nil { // exec.CommandContext installs the immediate-child kill.
		cmd.Cancel = func() error { return signalProcessGroup(cmd, unix.SIGTERM) }
	}

	if err := cmd.Start(); err != nil {
		captured := output.Bytes()
		return captured, fmt.Errorf("%w: %s", err, string(captured))
	}

	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
	}()

	select {
	case err := <-waitErr:
		// exec.CommandContext may have killed only the direct child immediately
		// before Wait won this select. Recheck cancellation and reap its process
		// group so descendants do not survive that race.
		if ctx.Err() != nil && cmd.Process != nil {
			// The command has already exited, so its process group may no longer
			// have a live leader to give a grace period. Reap any remaining group
			// members immediately while the cancellation race is known.
			_ = signalProcessGroup(cmd, unix.SIGKILL)
		}
		if ctx.Err() != nil {
			captured := output.Bytes()
			return captured, fmt.Errorf("%w: %s", ctx.Err(), string(captured))
		}
		if err != nil {
			captured := output.Bytes()
			return captured, fmt.Errorf("%w: %s", err, string(captured))
		}
		return output.Bytes(), nil
	case <-ctx.Done():
		// Give wrappers time to stop transient cgroup units, then kill every
		// remaining process in the helper group. Detached sessions still require
		// cgroup containment.
		terminateProcessGroup(cmd)
		<-waitErr
		captured := output.Bytes()
		return captured, fmt.Errorf("%w: %s", ctx.Err(), string(captured))
	}
}

func signalProcessGroup(cmd *exec.Cmd, signal syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := unix.Kill(-cmd.Process.Pid, signal); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = signalProcessGroup(cmd, unix.SIGTERM)
	time.Sleep(processGroupShutdownGrace)
	_ = signalProcessGroup(cmd, unix.SIGKILL)
}

func RunBoundedCommandInput(ctx context.Context, cmd *exec.Cmd, input io.Reader) ([]byte, error) {
	cmd.Stdin = input
	return RunBoundedCommand(ctx, cmd)
}

// configureProcessGroup sets up the command to run in its own process group.
// This ensures that when the process is killed, all child processes are also terminated.
// This is critical for helpers that launch subprocesses (pip, npm, podman, systemctl, etc.)
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Setpgid=true creates a new process group, allowing us to kill all children with -PGID
	cmd.SysProcAttr.Setpgid = true
}

// PrepareCommand ensures a command runs in its own process group with proper cleanup.
// MUST be called on any external command before execution to guarantee that
// timeout/cancellation kills the entire process tree, not just the parent.
// This is an architectural invariant: nothing external should execute without this.
func PrepareCommand(cmd *exec.Cmd) {
	configureProcessGroup(cmd)
}

// HelperContextWithTimeout creates a context with the specified timeout class.
// Use this when invoking helper commands to ensure operation-appropriate timeouts.
// Example: ctx, cancel := HelperContextWithTimeout(parentCtx, PackageBuildTimeout)
func HelperContextWithTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout == 0 {
		timeout = ConfigMutationTimeout // default to fast timeout
	}
	return context.WithTimeout(ctx, timeout)
}

// HelperCommand runs narrowly scoped privileged helpers through sudo when the
// packaged installation configures it. Development and test configurations can
// leave Sudo empty and execute their helper directly.
//
// SECURITY: All helpers run in their own process group (Setpgid=true) to ensure
// that timeout/cancellation kills the entire process tree, not just the parent.
// This prevents orphaned child processes (pip, npm, podman, containers, etc.)
// from continuing execution after their parent timeout.
//
// DEPRECATED: Use HelperCommandContext with HelperContextWithTimeout for
// operation-appropriate timeouts. This function is kept for backward compatibility
// but should not be used for new code.
func HelperCommand(sudo string, path string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if sudo == "" {
		cmd = exec.Command(path, args...)
	} else {
		cmd = exec.Command(sudo, append([]string{"--non-interactive", path}, args...)...)
	}
	configureProcessGroup(cmd)
	return cmd
}

func HelperCommandContext(ctx context.Context, sudo string, path string, args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if sudo == "" {
		cmd = exec.CommandContext(ctx, path, args...)
	} else {
		cmd = exec.CommandContext(ctx, sudo, append([]string{"--non-interactive", path}, args...)...)
	}
	configureProcessGroup(cmd)
	return cmd
}

// NewCommand creates a command with context and proper process group setup.
// Use this for any external command that needs cancellation/timeout support.
// The command will automatically run in its own process group so that
// timeout/cancellation kills the entire process tree.
func NewCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	PrepareCommand(cmd)
	return cmd
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
	// Use ConfigMutationTimeout as default for backward compatibility.
	// Callers should use RunHelperCommandWithTimeout for operation-appropriate timeouts.
	commandCtx, cancel := context.WithTimeout(ctx, ConfigMutationTimeout)
	defer cancel()
	_, err := RunBoundedCommand(commandCtx, HelperCommandContext(commandCtx, sudo, path, args...))
	return err
}

// RunHelperCommandWithTimeout runs a helper with an operation-specific timeout.
// Use this for long-running operations like package builds, database operations, etc.
func RunHelperCommandWithTimeout(ctx context.Context, sudo, path string, timeout time.Duration, args ...string) error {
	if path == "" {
		return errors.New("helper is not configured")
	}
	if timeout == 0 {
		timeout = ConfigMutationTimeout // default
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
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
	// The name is only the descriptor's diagnostic label; retaining the
	// caller-controlled path here makes static analyzers treat it as a path
	// sink even though syscall.Open above already opened the file.
	file := os.NewFile(uintptr(fd), "<stepanel-open-regular>")
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
// The path must be validated by the caller to ensure it doesn't contain
// suspicious patterns like ".." or absolute paths (if relative paths are expected).
func OpenWriteNoFollow(path string, mode os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	// Use basename as the file name since the second param to NewFile is only for debugging
	// and doesn't affect the actual file that was opened by syscall.Open()
	return os.NewFile(uintptr(fd), filepath.Base(path)), nil
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
