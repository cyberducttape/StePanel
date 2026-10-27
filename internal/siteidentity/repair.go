package siteidentity

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

const repairJournalVersion = 1

var ErrNoLegacyIdentityCollisions = errors.New("no unresolved legacy site identity collisions were found")

// RepairAssignment is one existing site identity that must be moved away
// from its collision-prone legacy account.
type RepairAssignment struct {
	Site         string `json:"site"`
	PreviousUser string `json:"previous_user,omitempty"`
	LegacyUser   string `json:"legacy_user"`
	Username     string `json:"username"`
	Completed    bool   `json:"completed,omitempty"`
}

// RepairJournal is forward-only: after the mapping transaction commits, a
// failed host operation is resumed idempotently rather than rolled back to a
// shared Unix identity.
type RepairJournal struct {
	Version     int                `json:"version"`
	ID          string             `json:"id"`
	WebRoot     string             `json:"web_root"`
	State       string             `json:"state"`
	Assignments []RepairAssignment `json:"assignments"`
	Finalized   bool               `json:"finalized,omitempty"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

// RepairHost is implemented by the privileged maintenance command. Reconcile
// must be safe to repeat after interruption; Verify must confirm that the
// site's host state now uses the assigned account. BeforeCommit runs after
// the plan is durable and immediately before the mapping transaction; it may
// run more than once for one journal and must be idempotent.
type RepairHost interface {
	Preflight(context.Context, []RepairAssignment) error
	BeforeCommit(context.Context, RepairJournal) error
	Reconcile(context.Context, RepairAssignment) error
	Verify(context.Context, RepairAssignment) error
	Finalize(context.Context, []RepairAssignment) error
}

// PlanCollisionRepair discovers unresolved legacy account collisions and
// assigns every affected existing site a unique persisted account name.
func PlanCollisionRepair(db *sql.DB, webRoot string) (RepairJournal, error) {
	if db == nil || webRoot == "" {
		return RepairJournal{}, errors.New("site identity repair requires database and web root")
	}
	sitesRoot := filepath.Join(webRoot, "sites")
	rootInfo, err := os.Lstat(sitesRoot)
	if err != nil {
		return RepairJournal{}, fmt.Errorf("inspect existing site roots: %w", err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return RepairJournal{}, errors.New("existing site roots must be a real directory")
	}
	entries, err := os.ReadDir(sitesRoot)
	if err != nil {
		return RepairJournal{}, fmt.Errorf("scan existing site roots: %w", err)
	}
	index, err := loadIdentityIndex(context.Background(), db)
	if err != nil {
		return RepairJournal{}, err
	}
	// Plan exactly what the startup gate rejects, so a completed repair is
	// guaranteed to let the panel start.
	collisions := unresolvedLegacyCollisions(liveSites(entries), index)
	legacyUsers := make([]string, 0, len(collisions))
	for legacy := range collisions {
		legacyUsers = append(legacyUsers, legacy)
	}
	sort.Strings(legacyUsers)
	reserved := index.byUser
	var plan []RepairAssignment
	for _, legacy := range legacyUsers {
		for _, site := range collisions[legacy].Live {
			username := ""
			for attempt := 0; attempt < 1024; attempt++ {
				if candidate := allocatedUser(site, attempt); reserved[candidate] == "" {
					username = candidate
					reserved[candidate] = site
					break
				}
			}
			if username == "" {
				return RepairJournal{}, fmt.Errorf("could not allocate a unique account for site %q", site)
			}
			plan = append(plan, RepairAssignment{Site: site, PreviousUser: index.bySite[site], LegacyUser: legacy, Username: username})
		}
	}
	if len(plan) == 0 {
		return RepairJournal{}, ErrNoLegacyIdentityCollisions
	}
	var idBytes [16]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return RepairJournal{}, fmt.Errorf("create repair journal identity: %w", err)
	}
	return RepairJournal{
		Version:     repairJournalVersion,
		ID:          hex.EncodeToString(idBytes[:]),
		WebRoot:     filepath.Clean(webRoot),
		State:       "planned",
		Assignments: plan,
		UpdatedAt:   time.Now().UTC(),
	}, nil
}

// ExecuteCollisionRepair creates or resumes a root-owned durable journal,
// commits all new mappings atomically, then reconciles and verifies each host
// identity. It never rolls back a committed mapping to a shared account.
func ExecuteCollisionRepair(ctx context.Context, db *sql.DB, webRoot, journalPath string, host RepairHost) (RepairJournal, error) {
	if db == nil || host == nil || journalPath == "" {
		return RepairJournal{}, errors.New("site identity repair requires database, journal path, and host adapter")
	}
	if err := checkRepairJournalDirectory(filepath.Dir(journalPath)); err != nil {
		return RepairJournal{}, err
	}
	lock, err := acquireRepairJournalLock(journalPath)
	if err != nil {
		return RepairJournal{}, fmt.Errorf("lock site identity repair: %w", err)
	}
	defer func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}()
	journal, err := readRepairJournal(journalPath)
	planned := false
	if errors.Is(err, os.ErrNotExist) {
		journal, err = PlanCollisionRepair(db, webRoot)
		if err != nil {
			return RepairJournal{}, err
		}
		planned = true
	} else if err != nil {
		return RepairJournal{}, err
	}
	if journal.Version != repairJournalVersion || journal.WebRoot != filepath.Clean(webRoot) || len(journal.Assignments) == 0 || !validRepairJournalID(journal.ID) {
		return journal, errors.New("repair journal version, web root, or assignments are invalid")
	}
	if !filepath.IsAbs(journal.WebRoot) {
		return journal, errors.New("repair journal web root must be absolute")
	}
	if journal.State == "completed" {
		next, planErr := PlanCollisionRepair(db, webRoot)
		if errors.Is(planErr, ErrNoLegacyIdentityCollisions) {
			return journal, nil
		}
		if planErr != nil {
			return journal, fmt.Errorf("plan subsequent site identity repair: %w", planErr)
		}
		if err := archiveCompletedRepairJournal(journalPath, journal.ID); err != nil {
			return journal, fmt.Errorf("preserve completed site identity repair journal: %w", err)
		}
		journal = next
		planned = true
	}
	if journal.State != "planned" && journal.State != "mappings-committed" {
		return journal, fmt.Errorf("unsupported repair journal state %q", journal.State)
	}
	if err := host.Preflight(ctx, journal.Assignments); err != nil {
		return journal, fmt.Errorf("site identity repair preflight: %w", err)
	}
	if journal.State == "planned" {
		// A new plan becomes durable only once the host has accepted it, so
		// a refused preflight leaves nothing behind and the next run replans.
		// It must be durable before the mapping commit: after a crash between
		// the two, the journal is the only record of which sites still need
		// host reconciliation.
		if planned {
			if err := writeRepairJournal(journalPath, journal); err != nil {
				return journal, err
			}
		}
		if err := host.BeforeCommit(ctx, journal); err != nil {
			return journal, fmt.Errorf("prepare site identity mapping commit: %w", err)
		}
		if err := applyRepairMappings(ctx, db, journal.Assignments); err != nil {
			return journal, err
		}
		journal.State = "mappings-committed"
		journal.UpdatedAt = time.Now().UTC()
		if err := writeRepairJournal(journalPath, journal); err != nil {
			return journal, fmt.Errorf("record committed identity mappings: %w", err)
		}
	}

	for index := range journal.Assignments {
		assignment := journal.Assignments[index]
		if assignment.Completed {
			continue
		}
		if err := ensureRepairMapping(ctx, db, assignment); err != nil {
			return journal, err
		}
		if err := host.Reconcile(ctx, assignment); err != nil {
			return journal, fmt.Errorf("reconcile site %q as %q: %w", assignment.Site, assignment.Username, err)
		}
		if err := host.Verify(ctx, assignment); err != nil {
			return journal, fmt.Errorf("verify site %q as %q: %w", assignment.Site, assignment.Username, err)
		}
		journal.Assignments[index].Completed = true
		journal.UpdatedAt = time.Now().UTC()
		if err := writeRepairJournal(journalPath, journal); err != nil {
			return journal, fmt.Errorf("record reconciled site %q: %w", assignment.Site, err)
		}
	}
	if !journal.Finalized {
		if err := host.Finalize(ctx, journal.Assignments); err != nil {
			return journal, fmt.Errorf("finalize old site identities: %w", err)
		}
		journal.Finalized = true
		journal.UpdatedAt = time.Now().UTC()
		if err := writeRepairJournal(journalPath, journal); err != nil {
			return journal, fmt.Errorf("record identity repair finalization: %w", err)
		}
	}
	if err := MigrateExisting(db, webRoot); err != nil {
		return journal, fmt.Errorf("verify startup identity migration after repair: %w", err)
	}
	journal.State = "completed"
	journal.UpdatedAt = time.Now().UTC()
	if err := writeRepairJournal(journalPath, journal); err != nil {
		return journal, fmt.Errorf("record completed site identity repair: %w", err)
	}
	return journal, nil
}

func acquireRepairJournalLock(journalPath string) (*os.File, error) {
	path := journalPath + ".lock"
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), path)
	info, err := lock.Stat()
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !ok || int(stat.Uid) != os.Geteuid() {
		_ = lock.Close()
		return nil, errors.New("repair journal lock must be a private regular file owned by the effective user")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func validRepairJournalID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16
}

// archiveCompletedRepairJournal keeps the previous completed audit record
// before the stable active-journal path is reused for a later collision.
func archiveCompletedRepairJournal(path, id string) error {
	if !validRepairJournalID(id) {
		return errors.New("completed repair journal has an invalid identity")
	}
	directory := filepath.Dir(path)
	archive := filepath.Join(directory, "site-identity-repair.completed-"+id+".json")
	if _, err := os.Lstat(archive); err == nil {
		return fmt.Errorf("completed journal archive already exists: %s", filepath.Base(archive))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(path, archive); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func applyRepairMappings(ctx context.Context, db *sql.DB, assignments []RepairAssignment) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin site identity repair transaction: %w", err)
	}
	defer tx.Rollback()
	for _, assignment := range assignments {
		if err := applyRepairMapping(ctx, tx, assignment); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit site identity repair mappings: %w", err)
	}
	return nil
}

func applyRepairMapping(ctx context.Context, tx *sql.Tx, assignment RepairAssignment) error {
	var current string
	err := tx.QueryRowContext(ctx, `SELECT username FROM site_identities WHERE site = ?`, assignment.Site).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		if assignment.PreviousUser != "" {
			return fmt.Errorf("site %q identity disappeared before repair", assignment.Site)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_identities(site, username, created_at) VALUES (?, ?, ?)`, assignment.Site, assignment.Username, time.Now().UTC().Unix()); err != nil {
			return fmt.Errorf("assign repaired identity to %q: %w", assignment.Site, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read current identity for %q: %w", assignment.Site, err)
	}
	if current == assignment.Username {
		return nil
	}
	if current != assignment.PreviousUser {
		return fmt.Errorf("site %q identity changed from planned %q to %q", assignment.Site, assignment.PreviousUser, current)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE site_identities SET username = ? WHERE site = ? AND username = ?`, assignment.Username, assignment.Site, current); err != nil {
		return fmt.Errorf("reassign site identity for %q: %w", assignment.Site, err)
	}
	return nil
}

func ensureRepairMapping(ctx context.Context, db *sql.DB, assignment RepairAssignment) error {
	var current string
	if err := db.QueryRowContext(ctx, `SELECT username FROM site_identities WHERE site = ?`, assignment.Site).Scan(&current); err != nil {
		return fmt.Errorf("verify repaired identity for %q: %w", assignment.Site, err)
	}
	if current != assignment.Username {
		return fmt.Errorf("site %q is mapped to %q, expected %q", assignment.Site, current, assignment.Username)
	}
	return nil
}

func checkRepairJournalDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect repair journal directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("repair journal directory must be a private, real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("repair journal directory must be owned by the effective user")
	}
	return nil
}

func readRepairJournal(path string) (RepairJournal, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return RepairJournal{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 || info.Mode().Perm()&0o077 != 0 {
		return RepairJournal{}, errors.New("repair journal must be a private regular file no larger than 1 MiB")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return RepairJournal{}, errors.New("repair journal must be owned by the effective user")
	}
	f, err := os.Open(path)
	if err != nil {
		return RepairJournal{}, fmt.Errorf("open repair journal: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return RepairJournal{}, fmt.Errorf("read repair journal: %w", err)
	}
	if len(data) > 1<<20 {
		return RepairJournal{}, errors.New("repair journal exceeds 1 MiB")
	}
	var journal RepairJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		return RepairJournal{}, fmt.Errorf("decode repair journal: %w", err)
	}
	seenSites := make(map[string]bool, len(journal.Assignments))
	seenUsers := make(map[string]bool, len(journal.Assignments))
	for _, assignment := range journal.Assignments {
		if !validSite(assignment.Site) || !validPersistedUser(assignment.Username) || !validPersistedUser(assignment.LegacyUser) || assignment.LegacyUser != UnixUser(assignment.Site) || assignment.Username == assignment.LegacyUser || assignment.PreviousUser != "" && !validPersistedUser(assignment.PreviousUser) {
			return RepairJournal{}, errors.New("repair journal contains an invalid site assignment")
		}
		if seenSites[assignment.Site] || seenUsers[assignment.Username] {
			return RepairJournal{}, errors.New("repair journal contains duplicate site or account assignments")
		}
		seenSites[assignment.Site] = true
		seenUsers[assignment.Username] = true
	}
	return journal, nil
}

func writeRepairJournal(path string, journal RepairJournal) error {
	data, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return fmt.Errorf("encode repair journal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".site-identity-repair-*.tmp")
	if err != nil {
		return fmt.Errorf("create repair journal stage: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure repair journal stage: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write repair journal stage: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync repair journal stage: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close repair journal stage: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("publish repair journal: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open repair journal directory: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync repair journal directory: %w", err)
	}
	return nil
}
