// Package session owns durable, revocable session-registry state.
package session

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	statefile "github.com/itchyitchy123/StePanel/internal/state"
)

type Entry struct {
	Username string `json:"username"`
	Expiry   int64  `json:"expiry"`
}

type Registry struct {
	mu      sync.RWMutex
	path    string
	db      *sql.DB
	Entries map[string]Entry
	err     error
}

// OpenDB loads the session registry from the shared control-plane database.
func OpenDB(db *sql.DB, legacyPath ...string) (*Registry, error) {
	registry := &Registry{db: db, Entries: make(map[string]Entry)}
	rows, err := db.Query(`SELECT id, username, expiry FROM sessions`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var entry Entry
		if err := rows.Scan(&id, &entry.Username, &entry.Expiry); err != nil {
			_ = rows.Close()
			return nil, err
		}
		registry.Entries[id] = entry
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(registry.Entries) == 0 && len(legacyPath) > 0 && legacyPath[0] != "" {
		legacy, err := Open(legacyPath[0])
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil {
			for id, entry := range legacy.Entries {
				registry.Entries[id] = entry
			}
		}
	}
	now := time.Now().Unix()
	for id, entry := range registry.Entries {
		if id == "" || entry.Expiry <= now {
			delete(registry.Entries, id)
		}
	}
	if err := registry.persistFullSyncLocked(); err != nil {
		return nil, err
	}
	return registry, nil
}

// New creates an empty registry. It is useful when the caller needs to build
// state before the first persistence operation, while Open is preferred for
// normal startup.
func New(path string) *Registry {
	return &Registry{path: path, Entries: make(map[string]Entry)}
}

func Open(path string) (*Registry, error) {
	registry := &Registry{path: path, Entries: make(map[string]Entry)}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > 1<<20 {
			return nil, errors.New("session state exceeds 1 MiB")
		}
		if err := json.Unmarshal(data, &registry.Entries); err != nil {
			var legacy map[string]int64
			if legacyErr := json.Unmarshal(data, &legacy); legacyErr != nil {
				return nil, err
			}
			for id, expiry := range legacy {
				registry.Entries[id] = Entry{Expiry: expiry}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	now := time.Now().Unix()
	for id, entry := range registry.Entries {
		if id == "" || entry.Expiry <= now {
			delete(registry.Entries, id)
		}
	}
	if err := registry.persistFullSyncLocked(); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *Registry) Add(id, username string, expiry int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().Unix()
	for candidate, entry := range r.Entries {
		if entry.Expiry <= now {
			delete(r.Entries, candidate)
		}
	}
	evictedID := ""
	if len(r.Entries) >= 10000 {
		oldestID := ""
		var oldest int64
		for candidate, entry := range r.Entries {
			if oldestID == "" || entry.Expiry < oldest {
				oldestID, oldest = candidate, entry.Expiry
			}
		}
		if oldestID != "" {
			delete(r.Entries, oldestID)
			evictedID = oldestID
		}
	}
	entry := Entry{Username: username, Expiry: expiry}
	r.Entries[id] = entry
	if err := r.persistAddLocked(id, entry, now, evictedID); err != nil {
		delete(r.Entries, id)
		r.err = err
		return err
	}
	r.err = nil
	return nil
}

func (r *Registry) Valid(id, username string, expiry int64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.Entries[id]
	return ok && entry.Expiry == expiry && entry.Username == username && expiry > time.Now().Unix()
}

func (r *Registry) RevokeUser(username string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	previous := make(map[string]Entry, len(r.Entries))
	for id, entry := range r.Entries {
		previous[id] = entry
	}
	for id, entry := range r.Entries {
		if entry.Username == username {
			delete(r.Entries, id)
		}
	}
	if err := r.persistDeleteByUsernameLocked(username); err != nil {
		r.Entries = previous
		r.err = err
		return err
	}
	r.err = nil
	return nil
}

func (r *Registry) Revoke(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	previous, existed := r.Entries[id]
	delete(r.Entries, id)
	if err := r.persistDeleteLocked(id); err != nil {
		if existed {
			r.Entries[id] = previous
		}
		r.err = err
		return err
	}
	r.err = nil
	return nil
}

func (r *Registry) PersistenceError() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.err
}

// persistFullSyncLocked rewrites the entire sessions table to match
// r.Entries. It is only used once, at startup after Open/OpenDB have loaded
// and garbage-collected expired entries: every subsequent mutation goes
// through the targeted persistAddLocked/persistDeleteLocked/
// persistDeleteByUsernameLocked paths instead, so a login or revocation on a
// large, active registry no longer deletes and reinserts every other
// session's row.
func (r *Registry) persistFullSyncLocked() error {
	if r.db != nil {
		tx, err := r.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sessions`); err != nil {
			_ = tx.Rollback()
			return err
		}
		for id, entry := range r.Entries {
			if _, err := tx.Exec(`INSERT INTO sessions (id, username, expiry, updated_at) VALUES (?, ?, ?, unixepoch())`, id, entry.Username, entry.Expiry); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		return tx.Commit()
	}
	return r.persistFileLocked()
}

// persistAddLocked applies exactly the mutations Add just made to r.Entries:
// the expiry-driven garbage collection cutoff, an optional evicted entry
// (when the registry was at capacity), and the new/updated entry itself.
func (r *Registry) persistAddLocked(id string, entry Entry, expiredCutoff int64, evictedID string) error {
	if r.db != nil {
		tx, err := r.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sessions WHERE expiry <= ?`, expiredCutoff); err != nil {
			_ = tx.Rollback()
			return err
		}
		if evictedID != "" {
			if _, err := tx.Exec(`DELETE FROM sessions WHERE id = ?`, evictedID); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO sessions (id, username, expiry, updated_at) VALUES (?, ?, ?, unixepoch())
			ON CONFLICT(id) DO UPDATE SET username = excluded.username, expiry = excluded.expiry, updated_at = excluded.updated_at`,
			id, entry.Username, entry.Expiry); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	return r.persistFileLocked()
}

func (r *Registry) persistDeleteLocked(id string) error {
	if r.db != nil {
		_, err := r.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
		return err
	}
	return r.persistFileLocked()
}

func (r *Registry) persistDeleteByUsernameLocked(username string) error {
	if r.db != nil {
		_, err := r.db.Exec(`DELETE FROM sessions WHERE username = ?`, username)
		return err
	}
	return r.persistFileLocked()
}

func (r *Registry) persistFileLocked() error {
	if r.path == "" {
		return nil
	}
	data, err := json.Marshal(r.Entries)
	if err != nil {
		return err
	}
	return statefile.WriteAtomic(r.path, append(data, '\n'), 0600)
}
