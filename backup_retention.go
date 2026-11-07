package stepanel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

// backupRetentionReport lists entries one retention run kept because their
// ownership could not be established. They are never deleted automatically.
type backupRetentionReport struct {
	// Unattributed holds the first maxReportedUnattributedBackups names.
	Unattributed []string
	// UnattributedCount counts every such entry.
	UnattributedCount int
	firstCause        error
}

// maxReportedUnattributedBackups bounds the names carried in a report and
// in its log line.
const maxReportedUnattributedBackups = 20

func (r *backupRetentionReport) unattributed(name string, cause error) {
	r.UnattributedCount++
	if r.firstCause == nil {
		r.firstCause = fmt.Errorf("%s: %w", name, cause)
	}
	if len(r.Unattributed) < maxReportedUnattributedBackups {
		r.Unattributed = append(r.Unattributed, name)
	}
}

// logSummary writes one bounded line per run, however many entries were
// kept, so a damaged inventory cannot grow the log by its size every run.
func (r *backupRetentionReport) logSummary(site string) {
	if r.UnattributedCount == 0 {
		return
	}
	log.Printf("backup retention for %s kept %d entries whose ownership could not be established (first %d: %s; first cause: %v)", site, r.UnattributedCount, len(r.Unattributed), strings.Join(r.Unattributed, ", "), r.firstCause)
}

// backupNameTimestampLength is the width of the creation timestamp that
// starts every published backup name (see createSiteBackupContext).
const backupNameTimestampLength = len("20060102-150405.000000000")

// backupNameSite returns the site a published backup is named for, or "" for
// a name that does not follow the "<timestamp>-<site>" layout. Names are only
// a scan filter: ownership is always proven by the manifest before deletion.
func backupNameSite(name string) string {
	if len(name) <= backupNameTimestampLength+1 || name[backupNameTimestampLength] != '-' {
		return ""
	}
	if _, err := time.Parse("20060102-150405.000000000", name[:backupNameTimestampLength]); err != nil {
		return ""
	}
	return safeUser(name[backupNameTimestampLength+1:])
}

// pruneSiteBackups retains the newest manifest-dated backups and explicitly
// protects protectedVerifiedName, which must be the basename of the backup
// that just passed archive verification. The protected artifact takes one
// slot within keep, regardless of host clock ordering.
//
// Retention is partitioned by site: an entry named for another site is
// skipped without reading its manifest, so one site's damaged backup cannot
// stop retention for every other site, and each run reads only its own
// site's manifests plus any entries with non-standard names. Only entries
// whose manifest proves they belong to this site are ever deleted; entries
// whose owner cannot be read are kept and returned in the report.
func pruneSiteBackups(root string, site SiteCapability, keep int, protectedVerifiedName string) (backupRetentionReport, error) {
	var report backupRetentionReport
	siteName := site.Site()
	if safeUser(siteName) == "" || keep < 1 || (protectedVerifiedName != "" && !validBackupName(protectedVerifiedName)) {
		return report, errors.New("invalid backup retention policy")
	}
	// One listing serves both steps. Names are listed without being
	// stat'ed or opened; only this site's entries are examined further.
	entries, err := os.ReadDir(root)
	if err != nil {
		return report, err
	}
	defer report.logSummary(siteName)
	if err := removeSitePruneTombstones(root, siteName, entries); err != nil {
		return report, err
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
		namedFor := backupNameSite(entry.Name())
		if namedFor != "" && namedFor != siteName {
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
			report.unattributed(entry.Name(), readErr)
			continue
		}
		if safeUser(manifest.Site) == "" {
			report.unattributed(entry.Name(), errors.New("manifest has no valid site"))
			continue
		}
		if namedFor != "" && manifest.Site != namedFor {
			report.unattributed(entry.Name(), fmt.Errorf("named for site %q but manifest names %q", namedFor, manifest.Site))
			continue
		}
		if manifest.Site == siteName {
			if manifest.CreatedAt.IsZero() {
				return report, fmt.Errorf("backup retention cannot establish creation order for %q: manifest has no created_at; operator review required", entry.Name())
			}
			items = append(items, candidate{name: entry.Name(), path: path, createdAt: manifest.CreatedAt})
			if entry.Name() == protectedVerifiedName {
				protectedFound = true
			}
		}
	}
	if !protectedFound {
		return report, fmt.Errorf("verified backup %q for site %s is missing from retention inventory; no backups pruned", protectedVerifiedName, siteName)
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
		return report, syncDirectory(root)
	}
	for _, item := range items[keep:] {
		if err := discardBackup(root, item.name); err != nil {
			return report, err
		}
	}
	return report, syncDirectory(root)
}

// backupPruneTombstonePrefix names a backup that retention has taken out of
// the inventory but not yet finished deleting. The leading dot keeps it out
// of every backup listing.
const backupPruneTombstonePrefix = ".pruning-"

// discardBackup removes one backup so that a crash or power loss at any
// point leaves either the whole backup or none of it in the inventory: it is
// renamed to a tombstone (one atomic, durable step), then deleted. Deleting
// in place could leave a backup without its manifest, and retention refuses
// to prune a directory whose owner it cannot read.
func discardBackup(root, name string) error {
	if !validBackupName(name) {
		return fmt.Errorf("refusing to discard invalid backup name %q", name)
	}
	tombstone := filepath.Join(root, backupPruneTombstonePrefix+name)
	if err := os.Rename(filepath.Join(root, name), tombstone); err != nil {
		return fmt.Errorf("retire backup %s: %w", name, err)
	}
	if err := syncDirectory(root); err != nil {
		return fmt.Errorf("record retirement of backup %s: %w", name, err)
	}
	processKillInjection("backup", "prune")
	if err := os.RemoveAll(tombstone); err != nil {
		return fmt.Errorf("delete retired backup %s: %w", name, err)
	}
	return nil
}

// removePruneTombstones finishes every deletion an interrupted retention
// left, for all sites. Startup runs it once; retention runs only finish
// their own site's (removeSitePruneTombstones).
func removePruneTombstones(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	return removeSitePruneTombstones(root, "", entries)
}

// removeSitePruneTombstones finishes deletions an interrupted retention left
// for site, or for every site when site is "". A tombstone whose name does
// not identify a site is finished by any run.
func removeSitePruneTombstones(root, site string, entries []os.DirEntry) error {
	removed := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), backupPruneTombstonePrefix) {
			continue
		}
		if owner := backupNameSite(strings.TrimPrefix(entry.Name(), backupPruneTombstonePrefix)); site != "" && owner != "" && owner != site {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return fmt.Errorf("finish deleting retired backup %s: %w", entry.Name(), err)
		}
		removed = true
	}
	if removed {
		return syncDirectory(root)
	}
	return nil
}
