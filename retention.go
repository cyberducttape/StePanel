package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var restoreStagePattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[a-z0-9_-]{1,32}$`)

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
		if entry.IsDir() && isTemporaryImportStage(entry.Name()) {
			if info.ModTime().Before(scratchCutoff) {
				if err := os.RemoveAll(path); err != nil {
					return err
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
