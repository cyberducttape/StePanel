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
	"syscall"
	"time"
)

const backupStageLockName = ".stepanel-stage.lock"

func acquireBackupStageLock(stage string) (*os.File, error) {
	lock, err := os.OpenFile(filepath.Join(stage, backupStageLockName), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func releaseBackupStageLock(lock *os.File) error {
	if lock == nil {
		return nil
	}
	err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if closeErr := lock.Close(); err == nil {
		err = closeErr
	}
	return err
}

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
		lockPath := filepath.Join(root, entry.Name(), backupStageLockName)
		var ownedLock *os.File
		lock, lockErr := os.OpenFile(lockPath, os.O_RDWR, 0600)
		if lockErr == nil {
			lockErr = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if lockErr != nil {
				_ = lock.Close()
				if errors.Is(lockErr, syscall.EWOULDBLOCK) || errors.Is(lockErr, syscall.EAGAIN) {
					continue // An active backup owns this stage.
				}
				return fmt.Errorf("inspect backup staging ownership %s: %w", entry.Name(), lockErr)
			}
			// The stage is not active. Keep the lock while removing it so a
			// concurrently starting cleanup/backup cannot race this decision.
			ownedLock = lock
		} else if !errors.Is(lockErr, os.ErrNotExist) {
			return fmt.Errorf("inspect backup staging ownership %s: %w", entry.Name(), lockErr)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect backup staging directory %s: %w", entry.Name(), err)
		}
		if info.ModTime().After(cutoff) {
			_ = releaseBackupStageLock(ownedLock)
			continue
		}
		removeErr := os.RemoveAll(filepath.Join(root, entry.Name()))
		if releaseErr := releaseBackupStageLock(ownedLock); removeErr == nil {
			removeErr = releaseErr
		}
		if removeErr != nil {
			return fmt.Errorf("remove abandoned backup staging directory %s: %w", entry.Name(), removeErr)
		}
		removed = true
	}
	if removed {
		return syncDirectory(root)
	}
	return nil
}

// pruneSiteBackups retains the newest manifest-dated backups and explicitly
// protects protectedVerifiedName, which must be the basename of the backup
// that just passed archive verification. The protected artifact takes one
// slot within keep, regardless of host clock ordering.
func pruneSiteBackups(root string, site SiteCapability, keep int, protectedVerifiedName string) error {
	siteName := site.Site()
	if safeUser(siteName) == "" || keep < 1 || (protectedVerifiedName != "" && !validBackupName(protectedVerifiedName)) {
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
	protectedFound := protectedVerifiedName == ""
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name()[0] == '.' {
			continue
		}
		path := filepath.Join(root, entry.Name())
		// Retention must still be able to remove stale artifacts when their
		// archive is damaged; full manifest/archive validation belongs to the
		// inventory and restore paths.
		manifestPath, pathErr := safePath(path, "manifest.json")
		var manifestFile *os.File
		var openErr error
		if pathErr == nil {
			manifestFile, _, openErr = openRegularNoFollow(manifestPath, nil)
		} else {
			openErr = pathErr
		}
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
		if readErr != nil {
			return fmt.Errorf("backup retention cannot establish ownership of %q: %w; operator review required", entry.Name(), readErr)
		}
		if safeUser(manifest.Site) == "" {
			return fmt.Errorf("backup retention found incomplete ownership metadata in %q; operator review required", entry.Name())
		}
		if manifest.Site == siteName {
			if manifest.CreatedAt.IsZero() {
				return fmt.Errorf("backup retention cannot establish creation order for %q: manifest has no created_at; operator review required", entry.Name())
			}
			items = append(items, candidate{name: entry.Name(), path: path, createdAt: manifest.CreatedAt})
			if entry.Name() == protectedVerifiedName {
				protectedFound = true
			}
		}
	}
	if !protectedFound {
		return fmt.Errorf("verified backup %q for site %s is missing from retention inventory; no backups pruned", protectedVerifiedName, siteName)
	}
	sort.Slice(items, func(i, j int) bool {
		// The just-created artifact has already passed strict verification in
		// the backup workflow. Protect it independently of wall-clock ordering:
		// host clock corrections must not make retention delete its own recovery
		// point.
		if items[i].name == protectedVerifiedName {
			return items[j].name != protectedVerifiedName
		}
		if items[j].name == protectedVerifiedName {
			return false
		}
		if !items[i].createdAt.Equal(items[j].createdAt) {
			return items[i].createdAt.After(items[j].createdAt)
		}
		if items[i].name != items[j].name {
			return items[i].name > items[j].name
		}
		return false
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
