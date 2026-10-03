package helper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"golang.org/x/sys/unix"
)

// ErrOutputLimitExceeded reports that a subprocess wrote more than its
// output budget. The process group is terminated as soon as the budget is
// exceeded, so memory stays bounded however much the child tries to write.
var ErrOutputLimitExceeded = errors.New("command output exceeds limit")

// cappedBuffer stores at most limit bytes. Unlike BoundedBuffer it does not
// silently truncate: the first write past the limit trips exceeded so the
// caller can kill the process and fail the operation.
type cappedBuffer struct {
	mu       sync.Mutex
	data     []byte
	limit    int
	exceeded chan struct{}
	once     *sync.Once
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.data)+len(p) > b.limit {
		if room := b.limit - len(b.data); room > 0 {
			b.data = append(b.data, p[:room]...)
		}
		b.once.Do(func() { close(b.exceeded) })
		return 0, ErrOutputLimitExceeded
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *cappedBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}

// RunCapped runs cmd with stdout and stderr sharing one capped buffer and
// returns the combined output. See RunCappedSeparate for the guarantees.
func RunCapped(ctx context.Context, cmd *exec.Cmd, limit int) ([]byte, error) {
	return runCappedWithCleanup(ctx, cmd, limit, nil)
}

// RunCappedWithCleanup is RunCapped with a bounded external-resource cleanup
// callback invoked before the subprocess group is killed on timeout or output
// overflow. It is for commands that create work outside their process tree.
func RunCappedWithCleanup(ctx context.Context, cmd *exec.Cmd, limit int, cleanup func()) ([]byte, error) {
	return runCappedWithCleanup(ctx, cmd, limit, cleanup)
}

func runCappedWithCleanup(ctx context.Context, cmd *exec.Cmd, limit int, cleanup func()) ([]byte, error) {
	exceeded := make(chan struct{})
	once := &sync.Once{}
	output := &cappedBuffer{limit: limit, exceeded: exceeded, once: once}
	err := runCapped(ctx, cmd, output, output, exceeded, cleanup)
	return output.bytes(), err
}

// RunCappedSeparate runs cmd with independently capped stdout and stderr.
// It enforces all four subprocess invariants: ctx bounds the run, both
// streams are bounded while the child runs, and cancellation, timeout, or an
// exceeded budget kill the whole process group. Exceeding a budget returns an
// error wrapping ErrOutputLimitExceeded.
func RunCappedSeparate(ctx context.Context, cmd *exec.Cmd, stdoutLimit, stderrLimit int) (stdout, stderr []byte, err error) {
	exceeded := make(chan struct{})
	once := &sync.Once{}
	out := &cappedBuffer{limit: stdoutLimit, exceeded: exceeded, once: once}
	errOut := &cappedBuffer{limit: stderrLimit, exceeded: exceeded, once: once}
	err = runCapped(ctx, cmd, out, errOut, exceeded, nil)
	return out.bytes(), errOut.bytes(), err
}

// RunCappedToFile runs cmd with stdout written straight to file and stderr
// capped, for output that belongs on disk rather than in memory (database
// dumps). The same cancellation and process-group guarantees apply.
func RunCappedToFile(ctx context.Context, cmd *exec.Cmd, file *os.File, stderrLimit int) (stderr []byte, err error) {
	exceeded := make(chan struct{})
	errOut := &cappedBuffer{limit: stderrLimit, exceeded: exceeded, once: &sync.Once{}}
	err = runCapped(ctx, cmd, file, errOut, exceeded, nil)
	return errOut.bytes(), err
}

func runCapped(ctx context.Context, cmd *exec.Cmd, stdout io.Writer, stderr *cappedBuffer, exceeded <-chan struct{}, cleanup func()) error {
	if cmd.Stdout != nil || cmd.Stderr != nil {
		return errors.New("capped command must not have preassigned output")
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	PrepareCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	killGroup := func() {
		if cleanup != nil {
			cleanup()
		}
		if cmd.Process != nil {
			_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
		}
	}
	select {
	case err := <-waitErr:
		select {
		case <-exceeded:
			return fmt.Errorf("%w", ErrOutputLimitExceeded)
		default:
		}
		return err
	case <-exceeded:
		killGroup()
		<-waitErr
		return fmt.Errorf("%w", ErrOutputLimitExceeded)
	case <-ctx.Done():
		killGroup()
		<-waitErr
		return ctx.Err()
	}
}
