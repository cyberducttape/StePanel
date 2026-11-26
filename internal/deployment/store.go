// Package deployment owns durable deployment-history state. It deliberately
// has no HTTP or platform-helper dependencies so it can be tested and reused
// by future release orchestration code.
package deployment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	statefile "github.com/cyberducttape/StePanel/internal/state"
	_ "modernc.org/sqlite"
)

type Record struct {
	ID         string    `json:"id"`
	Site       string    `json:"site"`
	Repository string    `json:"repository,omitempty"`
	Ref        string    `json:"ref,omitempty"`
	Commit     string    `json:"commit,omitempty"`
	Stage      string    `json:"stage"`
	State      string    `json:"state"`
	Artifact   string    `json:"artifact,omitempty"`
	Previous   string    `json:"previous_release,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

const (
	maxRecords = 1000
)

type Store struct {
	mu     sync.RWMutex
	path   string
	db     *sql.DB
	values []Record
}

// HistoryTable holds the durable deployment history, one row per record
// (control-plane migration 43 creates it). seq orders records that share a
// creation time; created_at_ns is the creation time in Unix nanoseconds, or 0
// for a record without one.
const HistoryTable = `CREATE TABLE IF NOT EXISTS deployment_history (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	id TEXT NOT NULL,
	site TEXT NOT NULL,
	repository TEXT NOT NULL DEFAULT '',
	ref TEXT NOT NULL DEFAULT '',
	commit_sha TEXT NOT NULL DEFAULT '',
	stage TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL DEFAULT '',
	artifact TEXT NOT NULL DEFAULT '',
	previous_release TEXT NOT NULL DEFAULT '',
	detail TEXT NOT NULL DEFAULT '',
	created_at_ns INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS deployment_history_newest_idx ON deployment_history(created_at_ns DESC, seq DESC)`

// writeTimeout bounds one history write; it is longer than SQLite's
// 5-second busy timeout so lock waits are not cut short.
const writeTimeout = 10 * time.Second

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func createdAtNanos(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

func insertRecord(ctx context.Context, db execer, r Record) error {
	_, err := db.ExecContext(ctx, `INSERT INTO deployment_history (id, site, repository, ref, commit_sha, stage, state, artifact, previous_release, detail, created_at_ns) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Site, r.Repository, r.Ref, r.Commit, r.Stage, r.State, r.Artifact, r.Previous, r.Detail, createdAtNanos(r.CreatedAt))
	return err
}

// pruneHistory keeps the newest maxRecords records.
func pruneHistory(ctx context.Context, db execer) error {
	_, err := db.ExecContext(ctx, `DELETE FROM deployment_history WHERE seq NOT IN (SELECT seq FROM deployment_history ORDER BY created_at_ns DESC, seq DESC LIMIT ?)`, maxRecords)
	return err
}

// MigrateBlob creates HistoryTable and moves the "deployments" state blob,
// if any, into it, then deletes the blob. It is control-plane migration 43.
func MigrateBlob(tx *sql.Tx) error {
	if _, err := tx.Exec(HistoryTable); err != nil {
		return err
	}
	var payload []byte
	err := tx.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'deployments'`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read deployment history state: %w", err)
	}
	var records []Record
	if err := json.Unmarshal(payload, &records); err != nil {
		return fmt.Errorf("decode deployment history state: %w", err)
	}
	ctx := context.Background()
	// The blob is newest first; insert oldest first so seq keeps that order
	// among records with the same creation time.
	for i := len(records) - 1; i >= 0; i-- {
		if err := insertRecord(ctx, tx, records[i]); err != nil {
			return fmt.Errorf("migrate deployment record %s: %w", records[i].ID, err)
		}
	}
	if err := pruneHistory(ctx, tx); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM state_blobs WHERE name = 'deployments'`)
	return err
}

// OpenDB opens the history in the control-plane database. When the table
// is empty, records from a legacy pre-database JSON file are imported.
func OpenDB(db *sql.DB, legacyPath string) (*Store, error) {
	s := &Store{db: db, values: []Record{}}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM deployment_history`).Scan(&count); err != nil {
		return nil, fmt.Errorf("read deployment history: %w", err)
	}
	if count > 0 || legacyPath == "" {
		return s, nil
	}
	legacy, err := Open(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort: a no-op after Commit
	for i := len(legacy.values) - 1; i >= 0; i-- {
		if err := insertRecord(ctx, tx, legacy.values[i]); err != nil {
			return nil, fmt.Errorf("import legacy deployment history: %w", err)
		}
	}
	if err := pruneHistory(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("import legacy deployment history: %w", err)
	}
	s.values = withRecords(legacy.values)
	return s, nil
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, values: []Record{}}
	d, err := statefile.ReadFileBounded(path, 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if errors.Is(err, statefile.ErrFileTooLarge) {
		return nil, errors.New("deployment state exceeds 4 MiB")
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(d, &s.values); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Add(record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.addDB(record)
	}
	next := withRecord(s.values, record)
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := statefile.WriteAtomic(s.path, append(data, '\n'), 0600); err != nil {
		return err
	}
	s.values = next
	return nil
}

// withRecords returns values sorted newest first and capped.
func withRecords(values []Record) []Record {
	next := append([]Record{}, values...)
	sort.SliceStable(next, func(i, j int) bool { return next[i].CreatedAt.After(next[j].CreatedAt) })
	if len(next) > maxRecords {
		next = next[:maxRecords]
	}
	return next
}

// withRecord returns a new newest-first history containing record. Order is
// by creation time even when a spooled record is replayed after newer ones
// were written.
func withRecord(values []Record, record Record) []Record {
	next := append([]Record{record}, values...)
	sort.SliceStable(next, func(i, j int) bool { return next[i].CreatedAt.After(next[j].CreatedAt) })
	if len(next) > maxRecords {
		next = next[:maxRecords]
	}
	return next
}

// addDB appends one row and prunes to maxRecords in one transaction.
// Appends from the panel and the job worker never conflict.
func (s *Store) addDB(record Record) error {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // best-effort: a no-op after Commit
	if err := insertRecord(ctx, tx, record); err != nil {
		return err
	}
	if err := pruneHistory(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.values = withRecord(s.values, record)
	return nil
}

// readDB returns the durable history, newest first.
func (s *Store) readDB() ([]Record, error) {
	rows, err := s.db.Query(`SELECT id, site, repository, ref, commit_sha, stage, state, artifact, previous_release, detail, created_at_ns FROM deployment_history ORDER BY created_at_ns DESC, seq DESC LIMIT ?`, maxRecords)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Record{}
	for rows.Next() {
		var r Record
		var created int64
		if err := rows.Scan(&r.ID, &r.Site, &r.Repository, &r.Ref, &r.Commit, &r.Stage, &r.State, &r.Artifact, &r.Previous, &r.Detail, &created); err != nil {
			return nil, err
		}
		if created != 0 {
			r.CreatedAt = time.Unix(0, created).UTC()
		}
		values = append(values, r)
	}
	return values, rows.Err()
}

func (s *Store) List(site string) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Records written by the job worker process are visible only in the
	// database. Fall back to the last known history if it cannot be read.
	if s.db != nil {
		if values, err := s.readDB(); err == nil {
			s.values = values
		}
	}
	out := make([]Record, 0, len(s.values))
	for _, record := range s.values {
		if site == "" || record.Site == site {
			out = append(out, record)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
