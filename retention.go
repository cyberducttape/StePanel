package stepanel

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// cPanel restore stages append the first 12 characters of randomSecret
// (base64.RawURLEncoding) to the site name. Accept the mixed-case URL-safe
// suffix while retaining the legacy timestamp-site form.
var restoreStagePattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[a-z0-9_-]{1,32}(-[A-Za-z0-9_-]{12})?$`)

// temporaryImportStagePrefixes are the os.MkdirTemp prefixes workflows use
// for scratch trees under the import root. Unlike retained restore stages,
// they hold no audit value and are removed once a crash has abandoned them.
// TestEveryImportRootTempDirIsCleaned keeps this list complete.
var temporaryImportStagePrefixes = []string{
	"backup-database-restore-",
	"backup-files-restore-",
	"backup-rehearsal-",
	"backup-restore-",
	"offsite-restore-",
	"staging-db-",
	"wpress-restore-",
}

// createImportStage gives a long-running import/restore exclusive ownership of
// its scratch directory. Cleanup only removes stages whose ownership lock is
// not held, so a slow transfer cannot be mistaken for abandoned work.
func createImportStage(root, prefix string) (string, *os.File, error) {
	path, err := os.MkdirTemp(root, prefix)
	if err != nil {
		return "", nil, err
	}
	lock, err := os.OpenFile(importStageLockPath(path), os.O_CREATE|os.O_RDWR, 0600)
	if err == nil {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		if lock != nil {
			_ = lock.Close()
		}
		_ = os.RemoveAll(path)
		return "", nil, err
	}
	return path, lock, nil
}

func importStageLockPath(path string) string {
	return filepath.Join(filepath.Dir(path), ".stepanel-stage-lock-"+filepath.Base(path))
}

func removeImportStage(path string, lock *os.File) {
	_ = os.RemoveAll(path)
	_ = releaseBackupStageLock(lock)
	_ = os.Remove(importStageLockPath(path))
}

func isTemporaryImportStage(name string) bool {
	for _, prefix := range temporaryImportStagePrefixes {
		if rest, ok := strings.CutPrefix(name, prefix); ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
			return true
		}
	}
	return false
}

// CleanupImportStages removes retained restore stages and orphaned uploads
// older than maxAge, and temporary scratch trees abandoned for longer than
// orphanedStagingMinAge. A missing root has nothing to clean.
func CleanupImportStages(root string, maxAge time.Duration) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	scratchCutoff := time.Now().Add(-orphanedStagingMinAge)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		path := filepath.Join(root, entry.Name())
		if strings.HasPrefix(entry.Name(), ".stepanel-stage-lock-") && !entry.IsDir() {
			stage := filepath.Join(root, strings.TrimPrefix(entry.Name(), ".stepanel-stage-lock-"))
			if _, err := os.Lstat(stage); errors.Is(err, os.ErrNotExist) {
				lock, lockErr := os.OpenFile(path, os.O_RDWR, 0600)
				if lockErr == nil {
					lockErr = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
					if errors.Is(lockErr, syscall.EWOULDBLOCK) || errors.Is(lockErr, syscall.EAGAIN) {
						_ = lock.Close()
						continue
					}
					if lockErr != nil {
						_ = lock.Close()
						return lockErr
					}
					_ = releaseBackupStageLock(lock)
				}
				if lockErr == nil {
					if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
						return err
					}
				}
			}
			continue
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), archiveSpoolPrefix) {
			// A ZIP spool left by a crashed inspection or import.
			if info.ModTime().Before(scratchCutoff) {
				if err := os.Remove(path); err != nil {
					return err
				}
			}
			continue
		}
		if entry.IsDir() && isTemporaryImportStage(entry.Name()) {
			if info.ModTime().Before(scratchCutoff) {
				lockPath := importStageLockPath(path)
				lock, lockErr := os.OpenFile(lockPath, os.O_RDWR, 0600)
				if lockErr == nil {
					lockErr = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
					if errors.Is(lockErr, syscall.EWOULDBLOCK) || errors.Is(lockErr, syscall.EAGAIN) {
						_ = lock.Close()
						continue
					}
					if lockErr != nil {
						_ = lock.Close()
						return lockErr
					}
				} else if !errors.Is(lockErr, os.ErrNotExist) {
					return lockErr
				}
				if err := os.RemoveAll(path); err != nil {
					_ = releaseBackupStageLock(lock)
					return err
				}
				_ = releaseBackupStageLock(lock)
				if lock != nil {
					_ = os.Remove(lockPath)
				}
			}
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if entry.IsDir() && restoreStagePattern.MatchString(entry.Name()) {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
		} else if !entry.IsDir() && isOrphanUpload(entry.Name()) {
			if err := os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

func isOrphanUpload(name string) bool {
	return strings.HasPrefix(name, "upload-") && strings.HasSuffix(name, ".tar.gz") ||
		strings.HasPrefix(name, "upload-") && strings.HasSuffix(name, ".json") ||
		strings.HasPrefix(name, "wpress-upload-") && strings.HasSuffix(name, ".wpress")
}

func availableBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
