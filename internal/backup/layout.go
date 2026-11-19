package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	h "github.com/cyberducttape/StePanel/internal/helper"
)

// Backup layout. Published backups live in one directory per site:
//
//	BACKUP_ROOT/sites/<site>/<timestamp>-<site>/
//
// so listing, retention and restore read only the requesting site's
// entries, however many sites and backups the host holds. Staging
// directories (.backup-*), database safety copies (.database-deletions) and
// anything whose owner could not be proven stay in BACKUP_ROOT itself.

// SitesDirectory is the BACKUP_ROOT entry that holds every site's backups.
const SitesDirectory = "sites"

// nameTimestampFormat is the timestamp that starts every backup name.
const nameTimestampFormat = "20060102-150405.000000000"

// NameTimestampLength is the length of the timestamp that starts a backup
// name; the site follows it after a '-'.
const NameTimestampLength = len(nameTimestampFormat)

// SitesRoot is the directory holding every site's backup directory.
func SitesRoot(root string) string {
	return filepath.Join(root, SitesDirectory)
}

// SiteDirectory is the directory holding one site's backups.
func SiteDirectory(root, site string) (string, error) {
	if !validSite(site) {
		return "", fmt.Errorf("invalid backup site %q", site)
	}
	return h.SafePath(SitesRoot(root), site)
}

// Path resolves a published backup name to its directory. The name carries
// the site it was taken for, so no listing is needed. The manifest, not the
// name, still proves ownership wherever a backup is used.
func Path(root, name string) (string, error) {
	if !ValidName(name) {
		return "", errors.New("invalid backup name")
	}
	site := NameSite(name)
	if site == "" {
		return "", errors.New("backup name does not identify a site")
	}
	directory, err := SiteDirectory(root, site)
	if err != nil {
		return "", err
	}
	return h.SafePath(directory, name)
}

// ValidName reports whether name is safe to use as a single backup path
// component: 1 to 160 ASCII letters, digits, '.', '_' or '-', and not "."
// or "..".
func ValidName(name string) bool {
	if len(name) < 1 || len(name) > 160 || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

// NameSite returns the site a backup name was published for, or "" when
// the name does not start with a backup timestamp followed by a valid site.
func NameSite(name string) string {
	if len(name) <= NameTimestampLength+1 || name[NameTimestampLength] != '-' {
		return ""
	}
	if _, err := time.Parse(nameTimestampFormat, name[:NameTimestampLength]); err != nil {
		return ""
	}
	if site := name[NameTimestampLength+1:]; validSite(site) {
		return site
	}
	return ""
}

// SiteNames lists the sites that have a backup directory. A missing sites
// root is an empty list.
func SiteNames(root string) ([]string, error) {
	entries, err := os.ReadDir(SitesRoot(root))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sites := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && validSite(entry.Name()) {
			sites = append(sites, entry.Name())
		}
	}
	return sites, nil
}

// validSite applies the panel's site-name rule: 1 to 32 characters from
// lowercase letters, digits, '_' and '-'.
func validSite(site string) bool {
	if site == "" || len(site) > 32 {
		return false
	}
	for _, r := range site {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
