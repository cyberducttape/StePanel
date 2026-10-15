package importer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

var errInjectedConfigIO = errors.New("injected config I/O failure")

type faultConfigReplaceFile struct {
	*os.File
	fail string
}

func (f *faultConfigReplaceFile) Write(p []byte) (int, error) {
	if f.fail == "write" {
		n := len(p) / 2
		if n > 0 {
			_, _ = f.File.Write(p[:n])
		}
		return n, errInjectedConfigIO
	}
	return f.File.Write(p)
}

func (f *faultConfigReplaceFile) Chmod(mode os.FileMode) error {
	if f.fail == "chmod" {
		return errInjectedConfigIO
	}
	return f.File.Chmod(mode)
}

func (f *faultConfigReplaceFile) Chown(uid, gid int) error {
	if f.fail == "chown" {
		return errInjectedConfigIO
	}
	return f.File.Chown(uid, gid)
}

func (f *faultConfigReplaceFile) Sync() error {
	if f.fail == "sync" {
		return errInjectedConfigIO
	}
	return f.File.Sync()
}

func (f *faultConfigReplaceFile) Close() error {
	closeErr := f.File.Close()
	if f.fail == "close" {
		return errors.Join(closeErr, errInjectedConfigIO)
	}
	return closeErr
}

func newFaultConfigReplaceOps(t *testing.T, fail string) (configReplaceOps, *bool) {
	t.Helper()
	removed := false
	ops := systemConfigReplaceOps()
	ops.createTemp = func(dir, pattern string) (configReplaceTempFile, error) {
		file, err := os.CreateTemp(dir, pattern)
		if err != nil {
			return nil, err
		}
		return &faultConfigReplaceFile{File: file, fail: fail}, nil
	}
	baseRename := ops.rename
	ops.rename = func(oldPath, newPath string) error {
		if fail == "rename" {
			return errInjectedConfigIO
		}
		return baseRename(oldPath, newPath)
	}
	baseRemove := ops.remove
	ops.remove = func(path string) error {
		if fail == "remove" {
			return errInjectedConfigIO
		}
		err := baseRemove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			removed = true
		}
		return err
	}
	baseSyncDir := ops.syncDir
	ops.syncDir = func(path string) error {
		if fail == "dir-sync" {
			return errInjectedConfigIO
		}
		return baseSyncDir(path)
	}
	return ops, &removed
}

func TestDurableReplaceConfigPreservesMetadataAndSyncsParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wp-config.php")
	if err := os.WriteFile(path, []byte("old config"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat := before.Sys().(*syscall.Stat_t)
	ops, _ := newFaultConfigReplaceOps(t, "")
	directorySynced := false
	baseSync := ops.syncDir
	ops.syncDir = func(dir string) error {
		directorySynced = true
		return baseSync(dir)
	}

	if err := durableReplaceConfigWithOps(path, []byte("new config"), before, ops); err != nil {
		t.Fatalf("durable replacement failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new config" {
		t.Fatalf("replacement content = %q, %v", got, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	afterStat := after.Sys().(*syscall.Stat_t)
	if after.Mode().Perm() != before.Mode().Perm() || afterStat.Uid != beforeStat.Uid || afterStat.Gid != beforeStat.Gid {
		t.Fatalf("replacement metadata mode=%#o uid=%d gid=%d; want mode=%#o uid=%d gid=%d", after.Mode().Perm(), afterStat.Uid, afterStat.Gid, before.Mode().Perm(), beforeStat.Uid, beforeStat.Gid)
	}
	if !directorySynced {
		t.Fatal("successful replacement did not sync parent directory")
	}
}

func TestDurableReplaceConfigPrePublicationFailuresPreserveOriginal(t *testing.T) {
	for _, fail := range []string{"write", "chmod", "chown", "sync", "close", "rename"} {
		t.Run(fail, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wp-config.php")
			const original = "old config"
			if err := os.WriteFile(path, []byte(original), 0640); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0640); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			ops, removed := newFaultConfigReplaceOps(t, fail)
			if err := durableReplaceConfigWithOps(path, []byte("new config"), info, ops); err == nil || !errors.Is(err, errInjectedConfigIO) {
				t.Fatalf("replacement error = %v, want injected %s failure", err, fail)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != original {
				t.Fatalf("failed replacement changed original: content=%q err=%v", got, err)
			}
			if !*removed {
				t.Fatal("failed pre-publication replacement did not remove its staging file")
			}
		})
	}
}

func TestDurableReplaceConfigReportsCleanupFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wp-config.php")
	if err := os.WriteFile(path, []byte("old config"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ops, _ := newFaultConfigReplaceOps(t, "write")
	var stagedPath string
	baseCreate := ops.createTemp
	ops.createTemp = func(dir, pattern string) (configReplaceTempFile, error) {
		file, err := baseCreate(dir, pattern)
		if err == nil {
			stagedPath = file.Name()
		}
		return file, err
	}
	ops.remove = func(path string) error {
		return errInjectedConfigIO
	}
	if err := durableReplaceConfigWithOps(path, []byte("new config"), info, ops); err == nil || !strings.Contains(err.Error(), "cannot remove temp config file") || !errors.Is(err, errInjectedConfigIO) {
		t.Fatalf("cleanup failure = %v, want reported injected cleanup error", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "old config" {
		t.Fatalf("cleanup failure changed original: content=%q err=%v", got, err)
	}
	if _, err := os.Stat(stagedPath); err != nil {
		t.Fatalf("injected cleanup failure should leave staging file for diagnosis, stat err=%v", err)
	}
}

func TestDurableReplaceConfigReportsPostRenameDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wp-config.php")
	if err := os.WriteFile(path, []byte("old config"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ops, removed := newFaultConfigReplaceOps(t, "dir-sync")
	err = durableReplaceConfigWithOps(path, []byte("new config"), info, ops)
	if err == nil || !errors.Is(err, errInjectedConfigIO) || !strings.Contains(err.Error(), "replacement committed but parent directory sync failed") {
		t.Fatalf("directory sync error = %v, want explicit indeterminate publication diagnostic", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != "new config" {
		t.Fatalf("post-rename failure should expose committed replacement: content=%q err=%v", got, readErr)
	}
	if *removed {
		t.Fatal("published replacement staging path should not be removed")
	}
}
