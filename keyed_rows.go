package stepanel

import (
	"context"
	"database/sql"
	"fmt"
)

// keyedRowTable persists a map-shaped store as one table row per key, each
// with a revision, replacing a whole-object state blob
// (docs/CONTROL_PLANE_DATABASE.md). The store keeps its in-memory map and
// lock; persist writes, in one transaction, only the keys whose values differ
// from what this process last persisted, each guarded by its revision. A
// write over a row another process changed reloads the table into the map
// and returns errControlPlaneStateRefreshed, which callers retry. Storage
// failures are returned unchanged and recorded for readiness.
type keyedRowTable[V any] struct {
	name  string
	equal func(a, b V) bool
	// clone, when set, copies a value before it is kept as the persisted
	// view, so a later in-place change to the live value (a slice element)
	// is still detected as a change.
	clone  func(V) V
	load   func(ctx context.Context, db *sql.DB) (map[string]V, map[string]int64, error)
	insert func(ctx context.Context, tx *sql.Tx, key string, value V) (bool, error)
	update func(ctx context.Context, tx *sql.Tx, key string, value V, revision int64) (bool, error)
	remove func(ctx context.Context, tx *sql.Tx, key string, revision int64) (bool, error)
}

type keyedRow[V any] struct {
	value    V
	revision int64
}

// keyedRowState is a store's binding to its table.
type keyedRowState[V any] struct {
	db        *sql.DB
	persisted map[string]keyedRow[V]
}

func (s *keyedRowState[V]) bound() bool { return s != nil && s.db != nil }

// bind makes db the store's persistence: it loads the table into values, or,
// when the table is empty, records the values already in memory (from a
// legacy pre-database JSON file). Caller holds the store lock.
func (t keyedRowTable[V]) bind(state *keyedRowState[V], db *sql.DB, values map[string]V) error {
	state.db = db
	state.persisted = map[string]keyedRow[V]{}
	loaded, revisions, err := t.loadWithTimeout(db)
	if err != nil {
		return err
	}
	if len(loaded) == 0 && len(values) > 0 {
		return t.persist(state, values)
	}
	t.replace(state, values, loaded, revisions)
	return nil
}

func (t keyedRowTable[V]) loadWithTimeout(db *sql.DB) (map[string]V, map[string]int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), controlPlaneStateReadTimeout)
	defer cancel()
	loaded, revisions, err := t.load(ctx, db)
	if err != nil {
		return nil, nil, fmt.Errorf("load %s: %w", t.name, classifyControlPlaneStorageError(err))
	}
	return loaded, revisions, nil
}

// replace sets values (in place, so callers holding the map see it) and the
// persisted view to a freshly loaded table.
func (t keyedRowTable[V]) keep(value V) V {
	if t.clone != nil {
		return t.clone(value)
	}
	return value
}

func (t keyedRowTable[V]) replace(state *keyedRowState[V], values, loaded map[string]V, revisions map[string]int64) {
	for key := range values {
		delete(values, key)
	}
	state.persisted = make(map[string]keyedRow[V], len(loaded))
	for key, value := range loaded {
		values[key] = value
		state.persisted[key] = keyedRow[V]{value: t.keep(value), revision: revisions[key]}
	}
}

// persist writes the keys whose values changed. Caller holds the store lock.
func (t keyedRowTable[V]) persist(state *keyedRowState[V], values map[string]V) error {
	type change struct {
		key      string
		value    V
		existed  bool
		remove   bool
		revision int64
	}
	var changes []change
	for key, value := range values {
		previous, existed := state.persisted[key]
		if existed && t.equal(previous.value, value) {
			continue
		}
		changes = append(changes, change{key: key, value: value, existed: existed, revision: previous.revision})
	}
	for key, previous := range state.persisted {
		if _, ok := values[key]; !ok {
			changes = append(changes, change{key: key, remove: true, revision: previous.revision})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	storageFailure := func(err error) error {
		err = classifyControlPlaneStorageError(err)
		recordControlPlaneStorageResult(err)
		return fmt.Errorf("persist %s: %w", t.name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlPlaneStateWriteTimeout)
	defer cancel()
	tx, err := state.db.BeginTx(ctx, nil)
	if err != nil {
		return storageFailure(err)
	}
	defer tx.Rollback() //nolint:errcheck // best-effort: a no-op after Commit
	conflict := ""
	for _, c := range changes {
		var applied bool
		switch {
		case c.remove:
			applied, err = t.remove(ctx, tx, c.key, c.revision)
		case c.existed:
			applied, err = t.update(ctx, tx, c.key, c.value, c.revision)
		default:
			applied, err = t.insert(ctx, tx, c.key, c.value)
		}
		if err != nil {
			return storageFailure(err)
		}
		if !applied {
			conflict = c.key
			break
		}
	}
	if conflict != "" {
		if err := tx.Rollback(); err != nil {
			return storageFailure(err)
		}
		loaded, revisions, err := t.loadWithTimeout(state.db)
		if err != nil {
			recordControlPlaneStorageResult(err)
			return fmt.Errorf("reload %s after a conflict on %s: %w", t.name, conflict, err)
		}
		t.replace(state, values, loaded, revisions)
		return fmt.Errorf("%w: %s %s was changed by another process", errControlPlaneStateRefreshed, t.name, conflict)
	}
	if err := tx.Commit(); err != nil {
		return storageFailure(err)
	}
	recordControlPlaneStorageResult(nil)
	for _, c := range changes {
		switch {
		case c.remove:
			delete(state.persisted, c.key)
		case c.existed:
			state.persisted[c.key] = keyedRow[V]{value: t.keep(c.value), revision: c.revision + 1}
		default:
			state.persisted[c.key] = keyedRow[V]{value: t.keep(c.value), revision: 1}
		}
	}
	return nil
}

// oneRowAffected reports whether an exec changed exactly one row.
func oneRowAffected(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}
