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
	d, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
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
	previous := append([]Record(nil), s.values...)
	s.values = append([]Record{record}, s.values...)
	if len(s.values) > 1000 {
		s.values = s.values[:1000]
	}
	data, err := json.MarshalIndent(s.values, "", "  ")
	if err != nil {
		return err
	}
	if s.db != nil {
		err = s.persistDBPayload(data)
	} else {
		err = statefile.WriteAtomic(s.path, append(data, '\n'), 0600)
	}
	if err != nil {
		s.values = previous
		return err
	}
	return nil
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Record, 0, len(s.values))
	for _, record := range s.values {
		if site == "" || record.Site == site {
			out = append(out, record)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
