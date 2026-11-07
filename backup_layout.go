package stepanel

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Backup layout. Published backups live in one directory per site:
//
//	BACKUP_ROOT/sites/<site>/<timestamp>-<site>/
//
// so listing, retention and restore read only the requesting site's
// entries, however many sites and backups the host holds. Staging
// directories (.backup-*), database safety copies (.database-deletions) and
// anything whose owner could not be proven stay in BACKUP_ROOT itself.
// Releases before this layout kept every backup directly in BACKUP_ROOT;
// migrateBackupLayout moves them at startup.
const backupSitesDirectory = "sites"

// backupSitesRoot is the directory holding every site's backup directory.
func backupSitesRoot(root string) string {
	return filepath.Join(root, backupSitesDirectory)
}

// backupSiteDirectory is the directory holding one site's backups.
func backupSiteDirectory(root, site string) (string, error) {
	if site == "" || safeUser(site) != site {
		return "", fmt.Errorf("invalid backup site %q", site)
	}
	return safePath(backupSitesRoot(root), site)
}

// backupPath resolves a published backup name to its directory. The name
// carries the site it was taken for, so no listing is needed. The manifest,
// not the name, still proves ownership wherever a backup is used.
func backupPath(root, name string) (string, error) {
	if !validBackupName(name) {
		return "", errors.New("invalid backup name")
	}
	site := backupNameSite(name)
	if site == "" {
		return "", errors.New("backup name does not identify a site")
	}
	directory, err := backupSiteDirectory(root, site)
	if err != nil {
		return "", err
	}
	return safePath(directory, name)
}

// backupDirectory resolves name for this configuration: the downloaded
// offsite backup when the configuration was prepared for one, otherwise the
// published backup in BACKUP_ROOT.
func (c Config) backupDirectory(name string) (string, error) {
	if c.downloadedBackup != "" {
		if !validBackupName(name) {
			return "", errors.New("invalid backup name")
		}
		return c.downloadedBackup, nil
	}
	return backupPath(c.BackupRoot, name)
}

// withDownloadedBackup returns a configuration whose backup lookups resolve
// to directory, a downloaded offsite backup that is not in BACKUP_ROOT.
func (c Config) withDownloadedBackup(directory string) Config {
	c.downloadedBackup = directory
	return c
}

// ensureBackupSiteDirectory creates a site's backup directory (and the
// sites directory) durably, owner-only like BACKUP_ROOT.
func ensureBackupSiteDirectory(root, site string) (string, error) {
	directory, err := backupSiteDirectory(root, site)
	if err != nil {
		return "", err
	}
	for _, dir := range []string{backupSitesRoot(root), directory} {
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", err
			}
			if err := syncDirectory(filepath.Dir(dir)); err != nil {
				return "", err
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("backup directory %s is not a directory", dir)
		}
	}
	return directory, nil
}

// backupSiteNames lists the sites that have a backup directory.
func backupSiteNames(root string) ([]string, error) {
	entries, err := os.ReadDir(backupSitesRoot(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sites := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && safeUser(entry.Name()) == entry.Name() {
			sites = append(sites, entry.Name())
		}
	}
	return sites, nil
}

// backupLayoutMigration reports what migrateBackupLayout did.
type backupLayoutMigration struct {
	Moved int
	// Unattributed counts entries left in BACKUP_ROOT because their owner
	// could not be proven; Names holds the first few.
	Unattributed int
	Names        []string
}

const backupLayoutLockName = ".layout.lock"

// migrateBackupLayout moves backups published by earlier releases from
// BACKUP_ROOT into their site's directory. A backup moves only when its
// manifest names the same site as its name; anything else (unreadable or
// foreign manifests, names that do not follow the layout, a name already
// taken in the site's directory) stays where it is, is reported, and is
// never deleted automatically. Each move is one rename within the same
// filesystem, so an interruption leaves every backup in exactly one place,
// and the next start finishes the rest. The panel and the worker serialize
// on a lock file.
func migrateBackupLayout(root string) (backupLayoutMigration, error) {
	var report backupLayoutMigration
	lock, err := os.OpenFile(filepath.Join(root, backupLayoutLockName), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return report, fmt.Errorf("open backup layout lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return report, fmt.Errorf("lock backup layout: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	entries, err := os.ReadDir(root)
	if err != nil {
		return report, err
	}
	touched := map[string]bool{}
	leave := func(name string, cause error) {
		report.Unattributed++
		if len(report.Names) < maxReportedUnattributedBackups {
			report.Names = append(report.Names, name)
		}
		log.Printf("backup layout migration left %s in %s: %v", name, root, cause)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") || name == backupSitesDirectory {
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			leave(name, errors.New("not a backup directory"))
			continue
		}
		site := backupNameSite(name)
		if site == "" || !validBackupName(name) {
			leave(name, errors.New("name does not follow <timestamp>-<site>"))
			continue
		}
		owner, err := backupManifestSite(filepath.Join(root, name))
		if err != nil {
			leave(name, err)
			continue
		}
		if owner != site {
			leave(name, fmt.Errorf("named for site %q but manifest names %q", site, owner))
			continue
		}
		directory, err := ensureBackupSiteDirectory(root, site)
		if err != nil {
			return report, fmt.Errorf("create backup directory for %s: %w", site, err)
		}
		destination := filepath.Join(directory, name)
		if _, err := os.Lstat(destination); err == nil {
			leave(name, errors.New("a backup with this name already exists in the site's directory"))
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return report, err
		}
		if err := os.Rename(filepath.Join(root, name), destination); err != nil {
			return report, fmt.Errorf("move backup %s: %w", name, err)
		}
		touched[directory] = true
		report.Moved++
		if report.Moved%1000 == 0 {
			log.Printf("moved %d backups into per-site directories so far", report.Moved)
		}
	}
	// Each move is a single rename, so a crash before these syncs leaves
	// some backups still in the root, where the next start moves them
	// again. Syncing each directory once, not per backup, keeps the
	// one-time migration of a large backup root short.
	if report.Moved > 0 {
		for directory := range touched {
			if err := syncDirectory(directory); err != nil {
				return report, err
			}
		}
		if err := syncDirectory(root); err != nil {
			return report, err
		}
	}
	return report, nil
}

// backupManifestSite reads the site a backup's manifest names, bounded and
// without following symlinks. It proves placement only; signatures and
// archives are verified wherever the backup is used.
func backupManifestSite(directory string) (string, error) {
	manifestPath, err := safePath(directory, "manifest.json")
	if err != nil {
		return "", err
	}
	file, _, err := openRegularNoFollow(manifestPath, nil)
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBackupManifestBytes+1))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if len(data) > maxBackupManifestBytes {
		return "", errors.New("backup manifest exceeds size limit")
	}
	var manifest struct {
		Site string `json:"site"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", err
	}
	if safeUser(manifest.Site) == "" {
		return "", errors.New("manifest has no valid site")
	}
	return manifest.Site, nil
}

// flattenBackupLayout moves backups out of their site directories back into
// BACKUP_ROOT, the layout releases before per-site directories read. It is
// the downgrade step: the installer runs it when it reverts an upgrade that
// introduced the layout, and operators run it before returning to such a
// release by hand. Interrupted retention deletions are finished first; an
// entry whose name is already taken in the root, or that does not belong to
// its directory, stays and is reported.
func flattenBackupLayout(root string) (backupLayoutMigration, error) {
	var report backupLayoutMigration
	lock, err := os.OpenFile(filepath.Join(root, backupLayoutLockName), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return report, fmt.Errorf("open backup layout lock: %w", err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return report, fmt.Errorf("lock backup layout: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	sites, err := backupSiteNames(root)
	if err != nil {
		return report, err
	}
	for _, site := range sites {
		directory, err := backupSiteDirectory(root, site)
		if err != nil {
			return report, err
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return report, err
		}
		if err := removeDirectoryPruneTombstones(directory, entries); err != nil {
			return report, err
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if !entry.IsDir() || !validBackupName(name) || backupNameSite(name) != site {
				report.Unattributed++
				if len(report.Names) < maxReportedUnattributedBackups {
					report.Names = append(report.Names, filepath.Join(backupSitesDirectory, site, name))
				}
				continue
			}
			if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
				report.Unattributed++
				if len(report.Names) < maxReportedUnattributedBackups {
					report.Names = append(report.Names, filepath.Join(backupSitesDirectory, site, name))
				}
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return report, err
			}
			if err := os.Rename(filepath.Join(directory, name), filepath.Join(root, name)); err != nil {
				return report, fmt.Errorf("move backup %s: %w", name, err)
			}
			if err := syncDirectory(root); err != nil {
				return report, err
			}
			if err := syncDirectory(directory); err != nil {
				return report, err
			}
			report.Moved++
		}
		// An emptied site directory is removed; one still holding entries
		// stays for the operator.
		if err := os.Remove(directory); err != nil && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
			return report, err
		}
	}
	if err := os.Remove(backupSitesRoot(root)); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
		return report, err
	}
	return report, syncDirectory(root)
}

// runBackupLayoutFlatten is the `stepanel backup-layout flatten
// BACKUP_ROOT` command.
func runBackupLayoutFlatten(root string) error {
	report, err := flattenBackupLayout(root)
	fmt.Printf("moved %d backup(s) back into %s\n", report.Moved, root)
	if err != nil {
		return err
	}
	// The command succeeds only when an older release will see every
	// backup: nothing may be left below the per-site directory.
	if report.Unattributed > 0 {
		return fmt.Errorf("%d entr(ies) remain in %s (first: %s); a release that reads only %s cannot see them. Move or remove them, then run this again", report.Unattributed, backupSitesRoot(root), strings.Join(report.Names, ", "), root)
	}
	if _, statErr := os.Lstat(backupSitesRoot(root)); statErr == nil {
		return fmt.Errorf("%s still exists; stop StePanel (a backup may have been published during the flatten) and run this again", backupSitesRoot(root))
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	return nil
}
