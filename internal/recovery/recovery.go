// Package recovery records restore rehearsals and turns the evidence about a
// site's backups into a recovery status an operator can act on.
//
// A rehearsal proves that a specific backup can actually be recovered, not
// only that it was written. Each rehearsal records the proof Level it
// reached, so the status never claims more than was demonstrated: an
// archive-level rehearsal shows the backup decrypts, extracts, and contains
// its database dumps, but not that the application starts on that data.
package recovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Level is how far a rehearsal carried the restore.
type Level string

const (
	// LevelArchive: signed manifest verified, archive decrypted and fully
	// extracted into a disposable directory, file tree validated (no
	// symlinks or special files), and every database dump present.
	LevelArchive Level = "archive"
	// LevelApplication is recorded only after the configured proof command
	// verifies the restored database, generated configuration, services, and
	// application HTTP response.
	LevelApplication Level = "application"
)

// Describe states in plain language what a level proves and what it does
// not, for display next to the result.
func (l Level) Describe() string {
	switch l {
	case LevelArchive:
		return "Backup signature verified, archive decrypted and extracted, files and database dumps recovered. Database import and application start are not yet rehearsed."
	case LevelApplication:
		return "Backup was restored by the configured proof command, which verified database import, generated configuration, service activation, and an application HTTP response."
	default:
		return "Unknown rehearsal level."
	}
}

type Outcome string

const (
	OutcomePassed Outcome = "passed"
	OutcomeFailed Outcome = "failed"
)

type Trigger string

const (
	TriggerScheduled Trigger = "scheduled"
	TriggerManual    Trigger = "manual"
)

// Phase is one timed step of a rehearsal.
type Phase struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
}

// Rehearsal is one recorded attempt to recover a backup.
type Rehearsal struct {
	ID              int64     `json:"id"`
	Site            string    `json:"site"`
	Backup          string    `json:"backup"`
	BackupCreatedAt time.Time `json:"backup_created_at"`
	Trigger         Trigger   `json:"trigger"`
	Level           Level     `json:"level"`
	Outcome         Outcome   `json:"outcome"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at"`
	DurationMS      int64     `json:"duration_ms"`
	Phases          []Phase   `json:"phases,omitempty"`
	Error           string    `json:"error,omitempty"`
	Encrypted       bool      `json:"encrypted"`
	FilesRestored   int       `json:"files_restored"`
	BytesRestored   int64     `json:"bytes_restored"`
	Databases       []string  `json:"databases,omitempty"`
}

// historyPerSite bounds stored rehearsals; older ones are pruned on write.
const historyPerSite = 50

// Schema is applied by the control-plane migrations.
const Schema = `CREATE TABLE IF NOT EXISTS recovery_rehearsals (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    site TEXT NOT NULL,
    backup TEXT NOT NULL,
    backup_created_at INTEGER NOT NULL,
    trigger TEXT NOT NULL,
    level TEXT NOT NULL,
    outcome TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    finished_at INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL,
    detail BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS recovery_rehearsals_site_idx ON recovery_rehearsals(site, started_at DESC, id DESC);`

// Store persists rehearsal history in the control-plane database.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("recovery store requires a database")
	}
	return &Store{db: db}, nil
}

// detail holds the fields that are displayed but never queried.
type detail struct {
	Phases        []Phase  `json:"phases,omitempty"`
	Error         string   `json:"error,omitempty"`
	Encrypted     bool     `json:"encrypted"`
	FilesRestored int      `json:"files_restored"`
	BytesRestored int64    `json:"bytes_restored"`
	Databases     []string `json:"databases,omitempty"`
}

// Record stores a finished rehearsal and prunes the site's history to the
// newest historyPerSite entries in the same transaction.
func (s *Store) Record(ctx context.Context, r Rehearsal) error {
	if r.Site == "" || r.Backup == "" || r.Level == "" || (r.Outcome != OutcomePassed && r.Outcome != OutcomeFailed) {
		return errors.New("invalid rehearsal record")
	}
	data, err := json.Marshal(detail{Phases: r.Phases, Error: r.Error, Encrypted: r.Encrypted, FilesRestored: r.FilesRestored, BytesRestored: r.BytesRestored, Databases: r.Databases})
	if err != nil {
		return fmt.Errorf("encode rehearsal detail: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rehearsal record: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_rehearsals (site, backup, backup_created_at, trigger, level, outcome, started_at, finished_at, duration_ms, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Site, r.Backup, unixNanoOrZero(r.BackupCreatedAt), string(r.Trigger), string(r.Level), string(r.Outcome), r.StartedAt.UTC().UnixNano(), r.FinishedAt.UTC().UnixNano(), r.DurationMS, data); err != nil {
		return fmt.Errorf("insert rehearsal: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_rehearsals WHERE site = ? AND id NOT IN (SELECT id FROM recovery_rehearsals WHERE site = ? ORDER BY started_at DESC, id DESC LIMIT ?)`, r.Site, r.Site, historyPerSite); err != nil {
		return fmt.Errorf("prune rehearsal history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rehearsal record: %w", err)
	}
	return nil
}

// unixNanoOrZero stores an unknown time (a rehearsal that failed before the
// backup manifest was read) as 0; UnixNano is undefined for the zero Time.
func unixNanoOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UTC().UnixNano()
}

const rehearsalColumns = `id, site, backup, backup_created_at, trigger, level, outcome, started_at, finished_at, duration_ms, detail`

func scanRehearsal(row interface{ Scan(...any) error }) (Rehearsal, error) {
	var r Rehearsal
	var backupCreated, started, finished int64
	var trigger, level, outcome string
	var data []byte
	if err := row.Scan(&r.ID, &r.Site, &r.Backup, &backupCreated, &trigger, &level, &outcome, &started, &finished, &r.DurationMS, &data); err != nil {
		return Rehearsal{}, err
	}
	var d detail
	if err := json.Unmarshal(data, &d); err != nil {
		return Rehearsal{}, fmt.Errorf("decode rehearsal detail: %w", err)
	}
	if backupCreated != 0 {
		r.BackupCreatedAt = time.Unix(0, backupCreated).UTC()
	}
	r.StartedAt = time.Unix(0, started).UTC()
	r.FinishedAt = time.Unix(0, finished).UTC()
	r.Trigger, r.Level, r.Outcome = Trigger(trigger), Level(level), Outcome(outcome)
	r.Phases, r.Error, r.Encrypted, r.FilesRestored, r.BytesRestored, r.Databases = d.Phases, d.Error, d.Encrypted, d.FilesRestored, d.BytesRestored, d.Databases
	return r, nil
}

// Latest returns the site's most recent rehearsal of any outcome.
func (s *Store) Latest(ctx context.Context, site string) (*Rehearsal, error) {
	return s.one(ctx, `SELECT `+rehearsalColumns+` FROM recovery_rehearsals WHERE site = ? ORDER BY started_at DESC, id DESC LIMIT 1`, site)
}

// LatestPassed returns the site's most recent successful rehearsal.
func (s *Store) LatestPassed(ctx context.Context, site string) (*Rehearsal, error) {
	return s.one(ctx, `SELECT `+rehearsalColumns+` FROM recovery_rehearsals WHERE site = ? AND outcome = 'passed' ORDER BY started_at DESC, id DESC LIMIT 1`, site)
}

func (s *Store) one(ctx context.Context, query, site string) (*Rehearsal, error) {
	r, err := scanRehearsal(s.db.QueryRowContext(ctx, query, site))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read rehearsal: %w", err)
	}
	return &r, nil
}

// History returns up to limit rehearsals for the site, newest first.
func (s *Store) History(ctx context.Context, site string, limit int) ([]Rehearsal, error) {
	if limit < 1 || limit > historyPerSite {
		limit = historyPerSite
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+rehearsalColumns+` FROM recovery_rehearsals WHERE site = ? ORDER BY started_at DESC, id DESC LIMIT ?`, site, limit)
	if err != nil {
		return nil, fmt.Errorf("list rehearsals: %w", err)
	}
	defer rows.Close()
	history := make([]Rehearsal, 0, limit)
	for rows.Next() {
		r, err := scanRehearsal(rows)
		if err != nil {
			return nil, err
		}
		history = append(history, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list rehearsals: %w", err)
	}
	return history, nil
}
