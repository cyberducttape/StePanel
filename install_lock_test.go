package stepanel

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

func TestInstallerLockRejectsSymlinkedPathsAndDoesNotTruncate(t *testing.T) {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	lib, err := filepath.Abs("deploy/lib/install-lock.sh")
	if err != nil {
		t.Fatal(err)
	}
	run := func(directory string) error {
		cmd := exec.Command("bash", "-c", `. "$LIB"; install_acquire_host_lock "$LOCK_DIR" "$UID_VALUE" "$GID_VALUE"`)
		cmd.Env = append(os.Environ(), "LIB="+lib, "LOCK_DIR="+directory, "UID_VALUE="+uid, "GID_VALUE="+gid)
		return cmd.Run()
	}

	directory := filepath.Join(t.TempDir(), "private-lock")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(directory, "installer.lock")
	prior := []byte("existing lock metadata must remain intact\n")
	if err := os.WriteFile(lockFile, prior, 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(directory); err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if got, err := os.ReadFile(lockFile); err != nil || string(got) != string(prior) {
		t.Fatalf("lock open truncated or changed existing bytes: %q, %v", got, err)
	}
	if info, err := os.Stat(directory); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("lock directory mode = %v, %v; want 0700", info, err)
	}
	if info, err := os.Stat(lockFile); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("lock file mode = %v, %v; want 0600", info, err)
	}

	target := filepath.Join(t.TempDir(), "do-not-truncate")
	if err := os.WriteFile(target, prior, 0600); err != nil {
		t.Fatal(err)
	}
	symlinkDir := filepath.Join(t.TempDir(), "lock-dir-link")
	if err := os.Symlink(filepath.Dir(target), symlinkDir); err != nil {
		t.Fatal(err)
	}
	if err := run(symlinkDir); err == nil {
		t.Fatal("symlinked lock directory was accepted")
	}
	if err := os.Remove(lockFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, lockFile); err != nil {
		t.Fatal(err)
	}
	if err := run(directory); err == nil {
		t.Fatal("symlinked lock file was accepted")
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(prior) {
		t.Fatalf("symlink target changed: %q, %v", got, err)
	}
}

func TestInstallerLockRejectsConcurrentAcquisition(t *testing.T) {
	uid, gid := strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	directory := filepath.Join(t.TempDir(), "private-lock")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(directory, "installer.lock")
	file, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()

	lib, err := filepath.Abs("deploy/lib/install-lock.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `. "$LIB"; install_acquire_host_lock "$LOCK_DIR" "$UID_VALUE" "$GID_VALUE"`)
	cmd.Env = append(os.Environ(), "LIB="+lib, "LOCK_DIR="+directory, "UID_VALUE="+uid, "GID_VALUE="+gid)
	var exitErr *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exitErr) || exitErr.ExitCode() != 75 {
		t.Fatalf("contending installer exit = %v; want 75", err)
	}
}
