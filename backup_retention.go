package stepanel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxBackupManifestBytes = 8 << 20

// CleanupBackupStages removes abandoned backup staging directories left by a
// process crash. A completed backup is atomically renamed away from the
// .backup-* namespace, so an old directory in that namespace cannot represent
// a publishable backup. Keep recent directories to avoid racing an operation
// that started before a restart; the next startup will remove them once they
// exceed the retention window.
func CleanupBackupStages(root string, maxAge time.Duration) error {
	if filepath.Clean(root) == "." || maxAge <= 0 {
		return errors.New("invalid backup stage cleanup policy")
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	removed := false
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".backup-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect backup staging directory %s: %w", entry.Name(), err)
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("remove abandoned backup staging directory %s: %w", entry.Name(), err)
		}
		removed = true
	}
	if removed {
		return syncDirectory(root)
	}
	return nil
}

func pruneSiteBackups(root string, site SiteCapability, keep int) error {
	siteName := site.Site()
	if safeUser(siteName) == "" || keep < 1 {
		return errors.New("invalid backup retention policy")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	type candidate struct {
		name, path string
		createdAt  time.Time
	}
	items := []candidate{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name()[0] == '.' {
			continue
		}
		path := filepath.Join(root, entry.Name())
		// Retention must still be able to remove stale artifacts when their
		// archive is damaged; full manifest/archive validation belongs to the
		// inventory and restore paths.
		manifestFile, openErr := os.Open(filepath.Join(path, "manifest.json"))
		var data []byte
		var readErr error
		if openErr == nil {
			data, readErr = io.ReadAll(io.LimitReader(manifestFile, maxBackupManifestBytes+1))
			if closeErr := manifestFile.Close(); readErr == nil {
				readErr = closeErr
			}
			if readErr == nil && len(data) > maxBackupManifestBytes {
				readErr = errors.New("backup manifest exceeds size limit")
			}
		} else {
			readErr = openErr
		}
		var manifest struct {
			Site      string    `json:"site"`
			CreatedAt time.Time `json:"created_at"`
		}
		if readErr == nil {
			readErr = json.Unmarshal(data, &manifest)
		}
		if readErr == nil && manifest.Site == siteName {
			items = append(items, candidate{name: entry.Name(), path: path, createdAt: manifest.CreatedAt})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].createdAt.IsZero() && !items[j].createdAt.IsZero() && !items[i].createdAt.Equal(items[j].createdAt) {
			return items[i].createdAt.After(items[j].createdAt)
		}
		return items[i].name > items[j].name
	})
	if len(items) <= keep {
		return syncDirectory(root)
	}
	for _, item := range items[keep:] {
		if err := os.RemoveAll(item.path); err != nil {
			return err
		}
	}
	return syncDirectory(root)
}
