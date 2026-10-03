package helper

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRunCappedReturnsCombinedOutputWithinLimit(t *testing.T) {
	output, err := RunCapped(context.Background(), exec.Command("sh", "-c", "echo out; echo err >&2"), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(output); !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Fatalf("output = %q; want both streams", got)
	}
}

// TestRunCappedStopsUnboundedWriterAndItsProcessGroup proves memory stays
// bounded while the child is still writing, and that descendants die too.
func TestRunCappedStopsUnboundedWriterAndItsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	output, err := RunCapped(ctx, exec.Command("sh", "-c", "sleep 60 & echo $! > '"+pidFile+"'; exec yes"), 4096)
	if !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("err = %v; want ErrOutputLimitExceeded", err)
	}
	if len(output) > 4096 {
		t.Fatalf("buffered %d bytes; limit is 4096", len(output))
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("unbounded writer was not stopped promptly")
	}
	requireProcessGone(t, pidFile)
}

func TestRunCappedSeparateBoundsEachStream(t *testing.T) {
	stdout, stderr, err := RunCappedSeparate(context.Background(), exec.Command("sh", "-c", "printf 0123456789; echo warning >&2"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(stdout) != "0123456789" || strings.TrimSpace(string(stderr)) != "warning" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	_, _, err = RunCappedSeparate(context.Background(), exec.Command("sh", "-c", "printf ok; exec yes >&2"), 1024, 1024)
	if !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("stderr flood err = %v; want ErrOutputLimitExceeded", err)
	}
}

func TestRunCappedKillsProcessGroupOnTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := RunCapped(ctx, exec.Command("sh", "-c", "sleep 60 & echo $! > '"+pidFile+"'; wait"), 1024)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v; want deadline exceeded", err)
	}
	requireProcessGone(t, pidFile)
}

func requireProcessGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var status unix.WaitStatus
		_, _ = unix.Wait4(pid, &status, unix.WNOHANG, nil)
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		if state, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); strings.Contains(string(state), ") Z ") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived process-group termination", pid)
}

// TestRunCappedToFileStreamsStdoutAndBoundsStderr covers the database dump
// path: stdout of any size goes to disk, stderr stays capped.
func TestRunCappedToFileStreamsStdoutAndBoundsStderr(t *testing.T) {
	file, err := os.Create(filepath.Join(t.TempDir(), "dump.sql"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stderr, err := RunCappedToFile(context.Background(), exec.Command("sh", "-c", "head -c 3000000 /dev/zero; echo note >&2"), file, 64)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := file.Stat(); info.Size() != 3000000 || strings.TrimSpace(string(stderr)) != "note" {
		t.Fatalf("file size = %d, stderr = %q", info.Size(), stderr)
	}
	if _, err := RunCappedToFile(context.Background(), exec.Command("sh", "-c", "exec yes >&2"), file, 1024); !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("stderr flood err = %v; want ErrOutputLimitExceeded", err)
	}
}
