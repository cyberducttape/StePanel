package siteidentity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
	// A site placed inside a hosting-account home (ADR-0007) uses the
	// account's Unix user and must never also get a per-site account.
	var placed int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM site_placements WHERE site = ?`, site).Scan(&placed); err != nil && !strings.Contains(err.Error(), "no such table") {
		return "", fmt.Errorf("check site placement: %w", err)
	}
	if placed > 0 {
		return "", fmt.Errorf("site %q is placed in a hosting account and has no per-site Unix account", site)
	}
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
	sites := liveSites(entries)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open site identity migration connection: %w", err)
	}
	defer conn.Close()
	// The panel and worker can start concurrently and both run this migration.
	// Acquire SQLite's writer reservation before reading so the second process
	// cannot hold a stale WAL snapshot and fail while upgrading it to a writer.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin site identity migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	index, err := loadIdentityIndex(ctx, conn)
	if err != nil {
		return err
	}
	if err := legacyCollisionError(sites, index); err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	for _, site := range sites {
		// A site that already has a mapping (legacy or repaired) is resolved;
		// the collision check above has proven it does not share an account.
		if _, mapped := index.bySite[site]; mapped {
			continue
		}
		legacy := UnixUser(site)
		if owner, taken := index.byUser[legacy]; taken {
			return fmt.Errorf("site identity collision: %q and %q both map to Unix account %q", owner, site, legacy)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO site_identities(site, username, created_at) VALUES (?, ?, ?) ON CONFLICT(site) DO NOTHING`, site, legacy, now); err != nil {
			return fmt.Errorf("migrate site identity %q: %w", site, err)
		}
		index.bySite[site] = legacy
		index.byUser[legacy] = site
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit site identity migration: %w", err)
	}
	committed = true
	return nil
}

// identityIndex is one consistent read of site_identities in both
// directions. The table holds one row per site ever created, so loading it
// once replaces a pair of point queries per site root.
type identityIndex struct {
	bySite map[string]string
	byUser map[string]string
}

type identityQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func loadIdentityIndex(ctx context.Context, q identityQuerier) (identityIndex, error) {
	rows, err := q.QueryContext(ctx, `SELECT site, username FROM site_identities`)
	if err != nil {
		return identityIndex{}, fmt.Errorf("read site identity assignments: %w", err)
	}
	defer rows.Close()
	index := identityIndex{bySite: make(map[string]string), byUser: make(map[string]string)}
	for rows.Next() {
		var site, username string
		if err := rows.Scan(&site, &username); err != nil {
			return identityIndex{}, fmt.Errorf("read site identity assignment: %w", err)
		}
		index.bySite[site] = username
		index.byUser[username] = site
	}
	if err := rows.Err(); err != nil {
		return identityIndex{}, fmt.Errorf("finish site identity scan: %w", err)
	}
	return index, nil
}

// liveSites returns the names of real site directories, sorted.
func liveSites(entries []os.DirEntry) []string {
	sites := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && validSite(entry.Name()) {
			sites = append(sites, entry.Name())
		}
	}
	sort.Strings(sites)
	return sites
}

// legacyCollision is one legacy account that more than one site claims.
// Live lists the existing site roots that must move to a distinct account;
// PersistedOwner is a tombstoned or absent site that still holds the name.
type legacyCollision struct {
	Live           []string
	PersistedOwner string
}

// unresolvedLegacyCollisions is the single definition of a blocking legacy
// identity collision, shared by the startup gate and the offline repair
// planner so a completed repair always satisfies the next startup.
//
// A site is resolved when it is mapped to an account other than its legacy
// digest: a completed repair assigns every colliding site a distinct,
// persisted account. The legacy digest stays the same by definition, but it
// no longer describes the host identity. An absent or still-legacy mapping
// is unresolved. A legacy account collides when two live sites share it and
// any is unresolved, or when an unresolved site's legacy name is persisted
// for a different site.
func unresolvedLegacyCollisions(sites []string, index identityIndex) map[string]legacyCollision {
	groups := make(map[string][]string)
	for _, site := range sites {
		legacy := UnixUser(site)
		groups[legacy] = append(groups[legacy], site)
	}
	collisions := make(map[string]legacyCollision)
	for legacy, group := range groups {
		unresolved := false
		owner := ""
		for _, site := range group {
			if assigned, ok := index.bySite[site]; ok && assigned != legacy {
				continue
			}
			unresolved = true
			if persisted, ok := index.byUser[legacy]; ok && persisted != site {
				owner = persisted
			}
		}
		if unresolved && (len(group) > 1 || owner != "") {
			collisions[legacy] = legacyCollision{Live: group, PersistedOwner: owner}
		}
	}
	return collisions
}

// legacyCollisionError reports every conflicting account in one
// deterministic error, giving operators a complete recovery inventory
// without partially inserting mappings or stopping at the first collision.
func legacyCollisionError(sites []string, index identityIndex) error {
	collisions := unresolvedLegacyCollisions(sites, index)
	if len(collisions) == 0 {
		return nil
	}
	usernames := make([]string, 0, len(collisions))
	for username := range collisions {
		usernames = append(usernames, username)
	}
	sort.Strings(usernames)
	details := make([]string, 0, len(usernames))
	for _, username := range usernames {
		collision := collisions[username]
		claimants := append([]string(nil), collision.Live...)
		if collision.PersistedOwner != "" {
			claimants = append(claimants, collision.PersistedOwner)
		}
		sort.Strings(claimants)
		quoted := make([]string, len(claimants))
		for index, site := range claimants {
			quoted[index] = strconv.Quote(site)
		}
		details = append(details, fmt.Sprintf("%s shared by %s", username, strings.Join(quoted, ", ")))
	}
	return fmt.Errorf("legacy site identity collisions block startup; offline recovery is required: %s", strings.Join(details, "; "))
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
