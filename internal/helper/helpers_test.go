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
	if got := string(b.data); got != "hello" {
		t.Fatalf("buffer = %q", got)
	}

	b = BoundedBuffer{limit: 3}
	if n, err := b.Write([]byte("world")); err != nil || n != 5 {
		t.Fatalf("limited Write() = (%d, %v)", n, err)
	}
	if got := string(b.data); got != "wor" {
		t.Fatalf("limited buffer = %q", got)
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
	if got, err := RunBoundedCommandLimit(ctx, exec.Command("sh", "-c", "printf 123456"), 3); err != nil || string(got) != "123" {
		t.Fatalf("bounded command = (%q, %v)", got, err)
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
