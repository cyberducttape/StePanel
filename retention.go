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
	coordination, err := acquireImportStageCoordination(root)
	if err != nil {
		return "", nil, err
	}
	defer releaseBackupStageLock(coordination)

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

// createNamedImportStage creates a retained, timestamped cPanel restore tree
// with the same cross-process ownership lease as temporary import stages.
// The lease stays held while extraction and restore work use the tree; cleanup
// may use age only to nominate it after the lease has been released.
func createNamedImportStage(root, name string) (string, *os.File, error) {
	if !restoreStagePattern.MatchString(name) {
		return "", nil, errors.New("invalid named import stage")
	}
	coordination, err := acquireImportStageCoordination(root)
	if err != nil {
		return "", nil, err
	}
	defer releaseBackupStageLock(coordination)

	path := filepath.Join(root, name)
	if err := os.Mkdir(path, 0700); err != nil {
		return "", nil, err
	}
	lock, err := os.OpenFile(importStageLockPath(path), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		if lock != nil {
			_ = lock.Close()
			_ = os.Remove(importStageLockPath(path))
		}
		_ = os.RemoveAll(path)
		return "", nil, err
	}
	return path, lock, nil
}

func importStageLockPath(path string) string {
	return filepath.Join(filepath.Dir(path), ".stepanel-stage-lock-"+filepath.Base(path))
}

// acquireImportStageCoordination serializes stage-lock pathname lifecycle
// operations. Per-stage flock files protect long-running work; this stable
// root lock prevents an opener from racing an unlink and acquiring a lock on
// a replacement inode. The coordination lock itself is never removed.
func acquireImportStageCoordination(root string) (*os.File, error) {
	lock, err := os.OpenFile(filepath.Join(root, ".stepanel-stage-coordination.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func removeImportStage(path string, lock *os.File) {
	// The held stage lock already keeps cleanup away from this tree, so remove
	// it before taking the root-wide coordination lock: a large delete must not
	// stall every other import, restore and backup stage.
	_ = os.RemoveAll(path)
	coordination, err := acquireImportStageCoordination(filepath.Dir(path))
	if err != nil {
		// Keep the lock pathname if coordination cannot be established;
		// unlinking without it would reintroduce the split-inode race. Cleanup
		// reclaims the pathname once this process has released the lock.
		_ = releaseBackupStageLock(lock)
		return
	}
	defer releaseBackupStageLock(coordination)
	_ = releaseBackupStageLock(lock)
	_ = os.Remove(importStageLockPath(path))
}

// outsideStageCoordination runs a tree removal with the root coordination
// lock released. Coordination guards lock-file pathnames; active-stage cleanup
// also holds the per-stage lease. Age-only removal is retained solely for
// legacy trees that predate leases.
func outsideStageCoordination(coordination *os.File, remove func() error) error {
	if err := syscall.Flock(int(coordination.Fd()), syscall.LOCK_UN); err != nil {
		return err
	}
	removeErr := remove()
	if err := syscall.Flock(int(coordination.Fd()), syscall.LOCK_EX); err != nil {
		return errors.Join(removeErr, err)
	}
	return removeErr
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
// orphanedStagingMinAge. A live stage lease always prevents removal. A missing
// root has nothing to clean.
func CleanupImportStages(root string, maxAge time.Duration) error {
	coordination, err := acquireImportStageCoordination(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer releaseBackupStageLock(coordination)

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
				}
				if lockErr == nil {
					if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
						_ = releaseBackupStageLock(lock)
						return err
					}
					_ = releaseBackupStageLock(lock)
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
				if err := outsideStageCoordination(coordination, func() error { return os.RemoveAll(path) }); err != nil {
					_ = releaseBackupStageLock(lock)
					return err
				}
				_ = releaseBackupStageLock(lock)
				if lock != nil {
					if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
						return err
					}
				}
			}
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if entry.IsDir() && restoreStagePattern.MatchString(entry.Name()) {
			lockPath := importStageLockPath(path)
			stageLock, lockErr := os.OpenFile(lockPath, os.O_RDWR, 0600)
			if lockErr == nil {
				lockErr = syscall.Flock(int(stageLock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			}
			if errors.Is(lockErr, syscall.EWOULDBLOCK) || errors.Is(lockErr, syscall.EAGAIN) {
				_ = stageLock.Close()
				continue
			}
			if lockErr != nil && !errors.Is(lockErr, os.ErrNotExist) {
				if stageLock != nil {
					_ = stageLock.Close()
				}
				return lockErr
			}
			if err := outsideStageCoordination(coordination, func() error { return os.RemoveAll(path) }); err != nil {
				_ = releaseBackupStageLock(stageLock)
				return err
			}
			if stageLock != nil {
				if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					_ = releaseBackupStageLock(stageLock)
					return err
				}
				if err := releaseBackupStageLock(stageLock); err != nil {
					return err
				}
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
