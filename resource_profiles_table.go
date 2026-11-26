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

type persistedResourceProfile struct {
	row      resourceProfileRow
	revision int64
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

// bindResourceProfiles makes db the store's persistence: it loads the table,
// or, when the table is empty, records the profiles already in memory (from
// a legacy pre-database JSON file).
func bindResourceProfiles(s *ResourceStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db = db
	s.persisted = map[string]persistedResourceProfile{}
	loaded, persisted, err := loadResourceProfiles(db)
	if err != nil {
		return err
	}
	if len(loaded) == 0 && len(s.values) > 0 {
		return s.persistTableLocked()
	}
	replaceResourceProfiles(s.values, loaded)
	s.persisted = persisted
	return nil
}

func loadResourceProfiles(db *sql.DB) (map[string]ResourceProfile, map[string]persistedResourceProfile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), controlPlaneStateReadTimeout)
	defer cancel()
	rows, err := db.QueryContext(ctx, `SELECT site, revision, `+resourceProfileColumns+` FROM resource_profiles`)
	if err != nil {
		return nil, nil, fmt.Errorf("load resource profiles: %w", classifyControlPlaneStorageError(err))
	}
	defer rows.Close()
	values := map[string]ResourceProfile{}
	persisted := map[string]persistedResourceProfile{}
	for rows.Next() {
		var site string
		var revision int64
		var r resourceProfileRow
		if err := rows.Scan(&site, &revision, &r.Account, &r.CPUPercent, &r.CPUWeight, &r.MemoryHighMB, &r.MemoryMB, &r.IOWeight, &r.TasksMax, &r.PHPWorkers, &r.DiskMB, &r.Inodes, &r.FilesystemQuotaState, &r.AppliedAt, &r.State); err != nil {
			return nil, nil, fmt.Errorf("read resource profile: %w", err)
		}
		profile, err := resourceProfileFromRow(site, r)
		if err != nil {
			return nil, nil, err
		}
		values[site] = profile
		persisted[site] = persistedResourceProfile{row: r, revision: revision}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("load resource profiles: %w", classifyControlPlaneStorageError(err))
	}
	return values, persisted, nil
}

// replaceResourceProfiles replaces dst's contents in place, so callers that
// hold the store's map see the reloaded profiles.
func replaceResourceProfiles(dst, src map[string]ResourceProfile) {
	for site := range dst {
		delete(dst, site)
	}
	for site, profile := range src {
		dst[site] = profile
	}
}

// persistTableLocked writes the rows that differ from what this process last
// persisted in one transaction. Caller holds s.mu.
func (s *ResourceStore) persistTableLocked() error {
	type change struct {
		site     string
		row      resourceProfileRow
		existed  bool
		remove   bool
		revision int64
	}
	var changes []change
	for site, profile := range s.values {
		row := resourceProfileToRow(profile)
		previous, existed := s.persisted[site]
		if existed && previous.row == row {
			continue
		}
		changes = append(changes, change{site: site, row: row, existed: existed, revision: previous.revision})
	}
	for site, previous := range s.persisted {
		if _, ok := s.values[site]; !ok {
			changes = append(changes, change{site: site, remove: true, revision: previous.revision})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlPlaneStateWriteTimeout)
	defer cancel()
	storageFailure := func(err error) error {
		err = classifyControlPlaneStorageError(err)
		recordControlPlaneStorageResult(err)
		return fmt.Errorf("persist resource profiles: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback() //nolint:errcheck // best-effort: a no-op after Commit
	conflict := ""
	for _, c := range changes {
		var result sql.Result
		switch {
		case c.remove:
			result, err = tx.ExecContext(ctx, `DELETE FROM resource_profiles WHERE site = ? AND revision = ?`, c.site, c.revision)
		case c.existed:
			result, err = tx.ExecContext(ctx, `UPDATE resource_profiles SET account = ?, cpu_percent = ?, cpu_weight = ?, memory_high_mb = ?, memory_mb = ?, io_weight = ?, tasks_max = ?, php_workers = ?, disk_mb = ?, inodes = ?, filesystem_quota_state = ?, applied_at = ?, state = ?, revision = revision + 1 WHERE site = ? AND revision = ?`, append(resourceProfileArgs(c.row), c.site, c.revision)...)
		default:
			result, err = tx.ExecContext(ctx, `INSERT INTO resource_profiles (site, `+resourceProfileColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(site) DO NOTHING`, append([]any{c.site}, resourceProfileArgs(c.row)...)...)
		}
		if err != nil {
			return storageFailure(err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return storageFailure(err)
		}
		if affected != 1 {
			conflict = c.site
			break
		}
	}
	if conflict != "" {
		if err := tx.Rollback(); err != nil {
			return storageFailure(err)
		}
		loaded, persisted, err := loadResourceProfiles(s.db)
		if err != nil {
			recordControlPlaneStorageResult(err)
			return fmt.Errorf("reload resource profiles after a conflict on %s: %w", conflict, err)
		}
		replaceResourceProfiles(s.values, loaded)
		s.persisted = persisted
		return fmt.Errorf("%w: resource profile %s was changed by another process", errControlPlaneStateRefreshed, conflict)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}
	recordControlPlaneStorageResult(nil)
	for _, c := range changes {
		switch {
		case c.remove:
			delete(s.persisted, c.site)
		case c.existed:
			s.persisted[c.site] = persistedResourceProfile{row: c.row, revision: c.revision + 1}
		default:
			s.persisted[c.site] = persistedResourceProfile{row: c.row, revision: 1}
		}
	}
	return nil
}
