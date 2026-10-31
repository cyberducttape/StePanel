package helper

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBoundedBuffer(t *testing.T) {
	var b BoundedBuffer
	if n, err := b.Write([]byte("hello")); err != nil || n != 5 {
		t.Fatalf("Write() = (%d, %v)", n, err)
	}
	if got := string(b.Bytes()); got != "hello" {
		t.Fatalf("buffer = %q", got)
	}

	b = BoundedBuffer{limit: 3}
	if n, err := b.Write([]byte("world")); err != nil || n != 5 {
		t.Fatalf("limited Write() = (%d, %v)", n, err)
	}
	if got := string(b.Bytes()); !strings.HasPrefix(got, "w") || !strings.HasSuffix(got, "ld") || !strings.Contains(got, "2 command output bytes omitted") {
		t.Fatalf("limited buffer = %q", got)
	}
}

func TestBoundedBufferRetainsTrailingDiagnosticsAndCountsOmission(t *testing.T) {
	var b BoundedBuffer
	b.limit = 64
	if _, err := b.Write([]byte(strings.Repeat("context ", 32))); err != nil {
		t.Fatal(err)
	}
	const finalError = "ERROR: unique-final-database-failure"
	if _, err := b.Write([]byte("\n" + finalError + "\n")); err != nil {
		t.Fatal(err)
	}
	got := string(b.Bytes())
	if !strings.HasPrefix(got, "context ") {
		t.Fatalf("head context was not retained: %q", got)
	}
	if !strings.Contains(got, finalError) {
		t.Fatalf("final diagnostic was not retained: %q", got)
	}
	if !strings.Contains(got, "command output bytes omitted") {
		t.Fatalf("omitted byte count missing: %q", got)
	}
	if retained := len(b.head) + b.tailLen; retained > b.limit {
		t.Fatalf("retained %d bytes beyond limit %d", retained, b.limit)
	}
}

func TestRunBoundedCommandLimit(t *testing.T) {
	ctx := context.Background()
	if got, err := RunBoundedCommandLimit(ctx, exec.Command("sh", "-c", "printf hello"), 64); err != nil || string(got) != "hello" {
		t.Fatalf("successful command = (%q, %v)", got, err)
	}
	if got, err := RunBoundedCommandLimit(ctx, exec.Command("sh", "-c", "printf output; exit 7"), 64); err == nil || !strings.Contains(err.Error(), "output") {
		t.Fatalf("failed command = (%q, %v)", got, err)
	}
	if got, err := RunBoundedCommandLimit(ctx, exec.Command("sh", "-c", "printf 123456"), 3); err != nil || !strings.HasPrefix(string(got), "1") || !strings.HasSuffix(string(got), "56") {
		t.Fatalf("bounded command = (%q, %v)", got, err)
	}
	if got, err := RunBoundedCommandLimit(ctx, exec.Command("sh", "-c", "printf '%050d' 0 >&2; printf 'UNIQUE_FINAL_ERROR' >&2; exit 7"), 32); err == nil || !strings.Contains(string(got), "UNIQUE_FINAL_ERROR") {
		t.Fatalf("large failing command did not retain trailing error: (%q, %v)", got, err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunBoundedCommandLimit(cancelCtx, exec.Command("sh", "-c", "sleep 10"), 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled command error = %v", err)
	}

	if _, err := RunBoundedCommandInput(context.Background(), exec.Command("cat"), strings.NewReader("input")); err != nil {
		t.Fatalf("input command: %v", err)
	}
}

func TestRunBoundedCommandDoesNotStartForCanceledContext(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := exec.Command("sh", "-c", "printf started > \"$1\"", "sh", marker)
	if _, err := RunBoundedCommandLimit(ctx, cmd, 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunBoundedCommandLimit() error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command started despite canceled context (stat error %v)", err)
	}
}

func TestRunBoundedCommandGivesSignalHandlersTimeToCleanUp(t *testing.T) {
	root := t.TempDir()
	ready := filepath.Join(root, "ready")
	stopped := filepath.Join(root, "stopped")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", `trap 'printf stopped > "$1"; exit 0' TERM; printf ready > "$2"; while :; do sleep .01; done`, "sh", stopped, ready)
	done := make(chan error, 1)
	go func() {
		_, err := RunBoundedCommandLimit(ctx, cmd, 1024)
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("command cancellation error = %v, want context.Canceled", err)
	}
	if data, err := os.ReadFile(stopped); err != nil || string(data) != "stopped" {
		t.Fatalf("TERM cleanup marker = %q, %v", data, err)
	}
}

func TestRunBoundedCommandCancellationDuringOutputAndStdin(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  *exec.Cmd
	}{
		{name: "continuous output", cmd: exec.Command("sh", "-c", "while :; do printf 'streaming output\\n'; done")},
		{name: "blocked stdin", cmd: exec.Command("sh", "-c", "sleep 10")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			if tc.name == "blocked stdin" {
				tc.cmd.Stdin = strings.NewReader(strings.Repeat("large streamed input", 1<<18))
			}
			done := make(chan error, 1)
			go func() {
				_, err := RunBoundedCommandLimit(ctx, tc.cmd, 1024)
				done <- err
			}()
			time.Sleep(30 * time.Millisecond)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("command error = %v, want context.Canceled", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("command did not stop after cancellation")
			}
		})
	}
}

func TestCancellationKillsCommandDescendants(t *testing.T) {
	for name, run := range map[string]func(context.Context, *exec.Cmd) error{
		"bounded": func(ctx context.Context, cmd *exec.Cmd) error {
			_, err := RunBoundedCommandLimit(ctx, cmd, 64)
			return err
		},
		"capped": func(ctx context.Context, cmd *exec.Cmd) error { _, err := RunCapped(ctx, cmd, 64); return err },
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			pidFile := filepath.Join(root, "child.pid")
			marker := filepath.Join(root, "orphan-ran")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			command := exec.CommandContext(ctx, "sh", "-c", "(sleep 1; echo orphan > \"$1\") & child=$!; echo $child > \"$2\"; wait", "sh", marker, pidFile)
			done := make(chan error, 1)
			go func() { done <- run(ctx, command) }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(pidFile); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel result = %v", err)
			}
			time.Sleep(1100 * time.Millisecond)
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("descendant survived cancellation (marker stat error %v)", err)
			}
		})
	}
}

func TestCommandConstruction(t *testing.T) {
	ctx := context.Background()
	for name, cmd := range map[string]*exec.Cmd{
		"plain":     HelperCommand("", "echo", "ok"),
		"sudo":      HelperCommand("sudo", "echo", "ok"),
		"plain ctx": HelperCommandContext(ctx, "", "echo", "ok"),
		"sudo ctx":  HelperCommandContext(ctx, "sudo", "echo", "ok"),
		"new":       NewCommand(ctx, "echo", "ok"),
	} {
		if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
			t.Errorf("%s command missing process group setup", name)
		}
	}
	if HelperCommand("", "echo").Path == "" {
		t.Fatal("HelperCommand did not resolve executable")
	}
	if ctx, cancel := HelperContextWithTimeout(context.Background(), 0); ctx == nil || cancel == nil {
		t.Fatal("default helper timeout was not created")
	} else {
		cancel()
	}
	if _, cancel := HelperContextWithTimeout(context.Background(), time.Millisecond); cancel == nil {
		t.Fatal("custom helper timeout has no cancel function")
	} else {
		cancel()
	}
}

func TestSafePathAndRejectSymlinkParents(t *testing.T) {
	root := t.TempDir()
	if got, err := SafePath(root, "site", "data"); err != nil || got != filepath.Join(root, "site", "data") {
		t.Fatalf("SafePath = (%q, %v)", got, err)
	}
	for _, parts := range [][]string{{""}, {"/etc"}, {"..", "escape"}} {
		if _, err := SafePath(root, parts...); err == nil {
			t.Errorf("SafePath(%v) accepted invalid component", parts)
		}
	}
	if _, err := SafePath("", "file"); err == nil {
		t.Error("SafePath accepted empty root")
	}
	if err := EnsureInside(root, filepath.Join(root, "ok")); err != nil {
		t.Fatalf("EnsureInside valid path: %v", err)
	}
	if err := EnsureInside(root, filepath.Join(root, "..", "escape")); err == nil {
		t.Error("EnsureInside accepted escape")
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := RejectSymlinkParents(filepath.Join(link, "file"), root); err == nil {
		t.Error("RejectSymlinkParents accepted symlink parent")
	}
	if _, err := SafePath(root, "link", "file"); err == nil {
		t.Error("SafePath accepted symlink parent")
	}
	if _, err := SafePath(root, "link"); err == nil {
		t.Error("SafePath accepted symlink leaf")
	}
	if err := RejectSymlinkParents(filepath.Join(root, "file"), root); err != nil {
		t.Fatalf("missing parent should be allowed: %v", err)
	}
	if err := EnsureInside(filepath.Join(root, "link"), filepath.Join(root, "link", "file")); err == nil {
		t.Error("EnsureInside accepted symlink root")
	}
}

func TestFileSafetyAndAtomicWrites(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := WriteAtomic(path, []byte("one"), 0600); err != nil {
		t.Fatalf("WriteAtomic: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "one" {
		t.Fatalf("atomic file = (%q, %v)", got, err)
	}
	file, err := OpenWriteNoFollow(filepath.Join(root, "write"), 0600)
	if err != nil {
		t.Fatalf("OpenWriteNoFollow: %v", err)
	}
	if _, err := file.WriteString("data"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	read, checked, err := OpenRegularNoFollow(path, info)
	if err != nil {
		t.Fatalf("OpenRegularNoFollow: %v", err)
	}
	if !SameFileInfo(info, checked) {
		t.Error("opened file did not preserve identity")
	}
	read.Close()
	if _, _, err := OpenRegularNoFollow(filepath.Join(root, "missing"), nil); err == nil {
		t.Error("missing file opened")
	}
	if SameFileInfo(nil, info) || SameFileInfo(info, nil) {
		t.Error("nil file info considered equal")
	}

	link := filepath.Join(root, "file-link")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := OpenWriteNoFollow(link, 0600); err == nil {
		t.Error("OpenWriteNoFollow followed symlink")
	}
}

func TestAcquireProcessLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := AcquireProcessLock(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer first.Close()
	second, err := AcquireProcessLock(path)
	if err == nil || second != nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second lock = (%v, %v)", second, err)
	}
	if _, err := AcquireProcessLock(filepath.Join(t.TempDir(), "missing", "lock")); err == nil {
		t.Error("lock in missing directory succeeded")
	}
}

func TestHelperCommands(t *testing.T) {
	if _, err := RunBoundedCommand(context.Background(), exec.Command("definitely-not-a-real-stepanel-command")); err == nil {
		t.Error("missing command succeeded")
	}
	if err := RunHelperCommand(context.Background(), "", "true"); err != nil {
		t.Fatalf("RunHelperCommand: %v", err)
	}
	if err := RunHelperCommand(context.Background(), "", "false"); err == nil {
		t.Error("RunHelperCommand accepted failure")
	}
	if err := RunHelperCommand(context.Background(), "", ""); err == nil {
		t.Error("RunHelperCommand accepted empty helper")
	}
	if err := RunHelperCommandWithTimeout(context.Background(), "", "true", 0); err != nil {
		t.Fatalf("RunHelperCommandWithTimeout: %v", err)
	}
	if err := RunHelperCommandWithTimeout(context.Background(), "", "", time.Second); err == nil {
		t.Error("RunHelperCommandWithTimeout accepted empty helper")
	}
	if err := SiteHelper("", "", "ignored", "ignored"); err != nil {
		t.Fatalf("empty SiteHelper: %v", err)
	}
	if err := SiteHelper("", "true", "ignored", "ignored"); err != nil {
		t.Fatalf("configured SiteHelper: %v", err)
	}
}

func TestOpenRegularRejectsChangedOrNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dir")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenRegularNoFollow(dir, info); err == nil {
		t.Error("directory accepted as regular file")
	}
	if _, _, err := OpenRegularNoFollow(filepath.Join(root, "missing"), info); err == nil {
		t.Error("missing file accepted")
	}
	if err := syscall.Flock(-1, syscall.LOCK_EX); err == nil {
		t.Error("invalid flock unexpectedly succeeded")
	}
}

func TestRunBoundedCommandKillsGroupThatIgnoresTERM(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// trap "" TERM is inherited by the sleep children, so only SIGKILL ends them.
	cmd := exec.CommandContext(ctx, "sh", "-c", `trap "" TERM; printf ready > "$1"; while :; do sleep .01; done`, "sh", ready)
	done := make(chan error, 1)
	go func() {
		_, err := RunBoundedCommandLimit(ctx, cmd, 1024)
		done <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a process group ignoring SIGTERM was not killed after the grace period")
	}
}
