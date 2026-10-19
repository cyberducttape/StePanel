// Package deployment owns durable deployment-history state. It deliberately
// has no HTTP or platform-helper dependencies so it can be tested and reused
// by future release orchestration code.
package deployment

import (
	"database/sql"
	"encoding/json"
	"errors"
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
	maxRecords      = 1000
	dbWriteAttempts = 16
)

type Store struct {
	mu     sync.RWMutex
	path   string
	db     *sql.DB
	values []Record
}

func OpenDB(db *sql.DB, legacyPath string) (*Store, error) {
	s := &Store{db: db, values: []Record{}}
	var payload []byte
	err := db.QueryRow(`SELECT payload FROM state_blobs WHERE name = 'deployments'`).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		if legacyPath != "" {
			legacy, legacyErr := Open(legacyPath)
			if legacyErr != nil && !errors.Is(legacyErr, os.ErrNotExist) {
				return nil, legacyErr
			}
			if legacyErr == nil {
				s.values = legacy.values
				if err := s.persistDB(); err != nil {
					return nil, err
				}
			}
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &s.values); err != nil {
		return nil, err
	}
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

// addDB appends to the durable history with a revision compare-and-swap. The
// panel and the job worker are separate processes sharing this row; writing
// a process's cached history wholesale would erase records the other process
// added since it last read the row.
func (s *Store) addDB(record Record) error {
	for attempt := 0; attempt < dbWriteAttempts; attempt++ {
		current, revision, err := s.readDB()
		if err != nil {
			return err
		}
		next := withRecord(current, record)
		data, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		var result sql.Result
		if revision < 0 {
			result, err = s.db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('deployments', ?, unixepoch(), 1) ON CONFLICT(name) DO NOTHING`, data)
		} else {
			result, err = s.db.Exec(`UPDATE state_blobs SET payload = ?, updated_at = unixepoch(), revision = revision + 1 WHERE name = 'deployments' AND revision = ?`, data, revision)
		}
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 1 {
			s.values = next
			return nil
		}
	}
	return errors.New("deployment history changed concurrently; retry limit reached")
}

// readDB returns the durable history and its revision, or revision -1 when
// no history row exists yet.
func (s *Store) readDB() ([]Record, int64, error) {
	var payload []byte
	var revision int64
	err := s.db.QueryRow(`SELECT payload, revision FROM state_blobs WHERE name = 'deployments'`).Scan(&payload, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return []Record{}, -1, nil
	}
	if err != nil {
		return nil, 0, err
	}
	values := []Record{}
	if err := json.Unmarshal(payload, &values); err != nil {
		return nil, 0, err
	}
	return values, revision, nil
}

func (s *Store) persistDB() error {
	data, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return err
	}
	return s.persistDBPayload(data)
}

func (s *Store) persistDBPayload(data []byte) error {
	_, err := s.db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('deployments', ?, unixepoch(), 1) ON CONFLICT(name) DO UPDATE SET payload=excluded.payload, updated_at=excluded.updated_at, revision=state_blobs.revision+1`, data)
	return err
}

func (s *Store) List(site string) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Records written by the job worker process are visible only in the
	// database. Fall back to the last known history if it cannot be read.
	if s.db != nil {
		if values, _, err := s.readDB(); err == nil {
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
