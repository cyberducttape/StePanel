package siteidentity

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var persistedUserPattern = regexp.MustCompile(`^sp-[a-z0-9][a-z0-9-]{1,28}$`)

// ResolveOrAllocate returns the immutable Unix account assigned to site. The
// mapping is allocated transactionally and remains reserved after deletion,
// so an account name can never be silently reassigned to another site.
func ResolveOrAllocate(db *sql.DB, site string) (string, error) {
	if db == nil || !validSite(site) {
		return "", errors.New("site identity database or site is invalid")
	}
	tx, err := db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin site identity transaction: %w", err)
	}
	defer tx.Rollback()
	var username string
	err = tx.QueryRow(`SELECT username FROM site_identities WHERE site = ?`, site).Scan(&username)
	if err == nil {
		if !validPersistedUser(username) {
			return "", fmt.Errorf("site %q has invalid persisted Unix account %q", site, username)
		}
		return username, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read site identity: %w", err)
	}
	for attempt := 0; attempt < 32; attempt++ {
		username := allocatedUser(site, attempt)
		result, insertErr := tx.Exec(`INSERT INTO site_identities(site, username, created_at) VALUES (?, ?, ?)`, site, username, time.Now().UTC().Unix())
		if insertErr == nil {
			if _, err := result.RowsAffected(); err != nil {
				return "", fmt.Errorf("confirm site identity allocation: %w", err)
			}
			return username, tx.Commit()
		}
		var conflictSite string
		if scanErr := tx.QueryRow(`SELECT site FROM site_identities WHERE username = ?`, username).Scan(&conflictSite); scanErr == nil {
			continue
		}
		return "", fmt.Errorf("allocate Unix account for site %q: %w", site, insertErr)
	}
	return "", fmt.Errorf("allocate Unix account for site %q: exhausted collision retries", site)
}

// MigrateExisting records the legacy identity of each existing site root. It
// fails closed on a collision instead of allowing two existing sites to share
// one Linux account. Tombstones are retained by the table and are never
// removed here.
func MigrateExisting(db *sql.DB, webRoot string) error {
	if db == nil || webRoot == "" {
		return errors.New("site identity migration requires database and web root")
	}
	entries, err := os.ReadDir(filepath.Join(webRoot, "sites"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("scan existing site roots: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validSite(entry.Name()) {
			continue
		}
		legacy := UnixUser(entry.Name())
		var owner string
		err := db.QueryRow(`SELECT site FROM site_identities WHERE username = ?`, legacy).Scan(&owner)
		if err == nil && owner != entry.Name() {
			return fmt.Errorf("site identity collision: %q and %q both map to Unix account %q", owner, entry.Name(), legacy)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("inspect site identity %q: %w", entry.Name(), err)
		}
		if _, err := db.Exec(`INSERT INTO site_identities(site, username, created_at) VALUES (?, ?, ?) ON CONFLICT(site) DO NOTHING`, entry.Name(), legacy, time.Now().UTC().Unix()); err != nil {
			return fmt.Errorf("migrate site identity %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func allocatedUser(site string, attempt int) string {
	payload := site
	if attempt > 0 {
		payload = fmt.Sprintf("%s#%d", site, attempt)
	}
	sum := sha256.Sum256([]byte(payload))
	// 96 digest bits plus a stable sp- prefix fit Linux's 32-byte account
	// limit. The database UNIQUE constraint, not the digest, is authoritative.
	return "sp-" + hex.EncodeToString(sum[:])[:24]
}

func validSite(site string) bool {
	if len(site) < 1 || len(site) > 32 {
		return false
	}
	for _, r := range site {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validPersistedUser(user string) bool {
	return persistedUserPattern.MatchString(strings.TrimSpace(user)) && len(user) <= 32
}
