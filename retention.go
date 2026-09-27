package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var restoreStagePattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[a-z0-9_-]{1,32}$`)

func CleanupImportStages(root string, maxAge time.Duration) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(root, entry.Name())
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
		strings.HasPrefix(name, "wpress-upload-") && strings.HasSuffix(name, ".wpress")
}

func availableBytes(path string) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), nil
}
