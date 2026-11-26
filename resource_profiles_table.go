package stepanel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Resource profiles live in the resource_profiles table, one row per site
// with a revision, instead of the whole-object "resources" state blob
// (docs/CONTROL_PLANE_DATABASE.md). ResourceStore keeps its in-memory map and
// lock, so callers are unchanged; persistLocked writes only the rows that
// differ from what this process last persisted, each guarded by its revision.
// Writes to different sites no longer conflict. A write to a row another
// process changed reloads the table and returns errControlPlaneStateRefreshed,
// the contract callers already retry on.

const resourceProfilesTable = `CREATE TABLE IF NOT EXISTS resource_profiles (
	site TEXT PRIMARY KEY,
	account TEXT NOT NULL DEFAULT '',
	cpu_percent INTEGER NOT NULL,
	cpu_weight INTEGER NOT NULL,
	memory_high_mb INTEGER NOT NULL,
	memory_mb INTEGER NOT NULL,
	io_weight INTEGER NOT NULL,
	tasks_max INTEGER NOT NULL,
	php_workers INTEGER NOT NULL,
	disk_mb INTEGER NOT NULL DEFAULT 0,
	inodes INTEGER NOT NULL DEFAULT 0,
	filesystem_quota_state TEXT NOT NULL DEFAULT '',
	applied_at TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	revision INTEGER NOT NULL DEFAULT 1
)`

const resourceProfileColumns = `account, cpu_percent, cpu_weight, memory_high_mb, memory_mb, io_weight, tasks_max, php_workers, disk_mb, inodes, filesystem_quota_state, applied_at, state`

// resourceProfileRow is a profile as stored: comparable, so a change is
// detected without encoding.
type resourceProfileRow struct {
	Account                                                                       string
	CPUPercent, CPUWeight, MemoryHighMB, MemoryMB, IOWeight, TasksMax, PHPWorkers int
	DiskMB, Inodes                                                                int
	FilesystemQuotaState, AppliedAt, State                                        string
}

func resourceProfileToRow(p ResourceProfile) resourceProfileRow {
	applied := ""
	if !p.AppliedAt.IsZero() {
		applied = p.AppliedAt.UTC().Format(time.RFC3339Nano)
	}
	return resourceProfileRow{Account: p.Account, CPUPercent: p.CPUPercent, CPUWeight: p.CPUWeight, MemoryHighMB: p.MemoryHighMB, MemoryMB: p.MemoryMB, IOWeight: p.IOWeight, TasksMax: p.TasksMax, PHPWorkers: p.PHPWorkers, DiskMB: p.DiskMB, Inodes: p.Inodes, FilesystemQuotaState: p.FilesystemQuotaState, AppliedAt: applied, State: p.State}
}

func resourceProfileFromRow(site string, r resourceProfileRow) (ResourceProfile, error) {
	p := ResourceProfile{Site: site, Account: r.Account, CPUPercent: r.CPUPercent, CPUWeight: r.CPUWeight, MemoryHighMB: r.MemoryHighMB, MemoryMB: r.MemoryMB, IOWeight: r.IOWeight, TasksMax: r.TasksMax, PHPWorkers: r.PHPWorkers, DiskMB: r.DiskMB, Inodes: r.Inodes, FilesystemQuotaState: r.FilesystemQuotaState, State: r.State}
	if r.AppliedAt != "" {
		applied, err := time.Parse(time.RFC3339Nano, r.AppliedAt)
		if err != nil {
			return ResourceProfile{}, fmt.Errorf("resource profile %s has an invalid applied_at: %w", site, err)
		}
		p.AppliedAt = applied
	}
	return p, nil
}

func resourceProfileArgs(r resourceProfileRow) []any {
	return []any{r.Account, r.CPUPercent, r.CPUWeight, r.MemoryHighMB, r.MemoryMB, r.IOWeight, r.TasksMax, r.PHPWorkers, r.DiskMB, r.Inodes, r.FilesystemQuotaState, r.AppliedAt, r.State}
}

// migrateResourceProfilesBlob moves the "resources" state blob, if any, into
// resource_profiles and deletes it. Recovering a completed upgrade restores
// the pre-migration database snapshot, so the blob is not kept in step.
func migrateResourceProfilesBlob(tx *sql.Tx) error {
	if _, err := tx.Exec(resourceProfilesTable); err != nil {
		return err
	}
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'resources'`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read resource profile state: %w", err)
	}
	var profiles map[string]ResourceProfile
	if err := json.Unmarshal(payload, &profiles); err != nil {
		return fmt.Errorf("decode resource profile state: %w", err)
	}
	for site, profile := range profiles {
		profile = normalizeResourceProfile(profile)
		if profile.Site == "" {
			profile.Site = site
		}
		if profile.Site != site {
			return fmt.Errorf("resource profile state holds site %q under key %q", profile.Site, site)
		}
		args := append([]any{site}, resourceProfileArgs(resourceProfileToRow(profile))...)
		if _, err := tx.Exec(`INSERT INTO resource_profiles (site, `+resourceProfileColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...); err != nil {
			return fmt.Errorf("migrate resource profile %s: %w", site, err)
		}
	}
	_, err = tx.Exec(`DELETE FROM state_blobs WHERE name = 'resources'`)
	return err
}

var resourceProfileRows = keyedRowTable[ResourceProfile]{
	name:  "resource profiles",
	equal: func(a, b ResourceProfile) bool { return resourceProfileToRow(a) == resourceProfileToRow(b) },
	load:  loadResourceProfiles,
	insert: func(ctx context.Context, tx *sql.Tx, site string, p ResourceProfile) (bool, error) {
		return oneRowAffected(tx.ExecContext(ctx, `INSERT INTO resource_profiles (site, `+resourceProfileColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(site) DO NOTHING`, append([]any{site}, resourceProfileArgs(resourceProfileToRow(p))...)...))
	},
	update: func(ctx context.Context, tx *sql.Tx, site string, p ResourceProfile, revision int64) (bool, error) {
		return oneRowAffected(tx.ExecContext(ctx, `UPDATE resource_profiles SET account = ?, cpu_percent = ?, cpu_weight = ?, memory_high_mb = ?, memory_mb = ?, io_weight = ?, tasks_max = ?, php_workers = ?, disk_mb = ?, inodes = ?, filesystem_quota_state = ?, applied_at = ?, state = ?, revision = revision + 1 WHERE site = ? AND revision = ?`, append(resourceProfileArgs(resourceProfileToRow(p)), site, revision)...))
	},
	remove: func(ctx context.Context, tx *sql.Tx, site string, revision int64) (bool, error) {
		return oneRowAffected(tx.ExecContext(ctx, `DELETE FROM resource_profiles WHERE site = ? AND revision = ?`, site, revision))
	},
}

// bindResourceProfiles makes the resource_profiles table the store's
// persistence (keyedRowTable.bind).
func bindResourceProfiles(s *ResourceStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = &keyedRowState[ResourceProfile]{}
	}
	return resourceProfileRows.bind(s.rows, db, s.values)
}

func loadResourceProfiles(ctx context.Context, db *sql.DB) (map[string]ResourceProfile, map[string]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT site, revision, `+resourceProfileColumns+` FROM resource_profiles`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	values := map[string]ResourceProfile{}
	revisions := map[string]int64{}
	for rows.Next() {
		var site string
		var revision int64
		var r resourceProfileRow
		if err := rows.Scan(&site, &revision, &r.Account, &r.CPUPercent, &r.CPUWeight, &r.MemoryHighMB, &r.MemoryMB, &r.IOWeight, &r.TasksMax, &r.PHPWorkers, &r.DiskMB, &r.Inodes, &r.FilesystemQuotaState, &r.AppliedAt, &r.State); err != nil {
			return nil, nil, err
		}
		profile, err := resourceProfileFromRow(site, r)
		if err != nil {
			return nil, nil, err
		}
		values[site] = profile
		revisions[site] = revision
	}
	return values, revisions, rows.Err()
}
