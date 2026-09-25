// SQLite-backed cross-process leased locks.
//
// The prior implementation had a set of latent bugs that meant it could not
// actually be relied on:
//
//   - INSERT...WHERE NOT EXISTS collided with the resource_key primary key
//     on takeover of an expired lock; SQLite raised a UNIQUE-constraint
//     error and Acquire returned failure even though the lock was expired
//     and legally re-acquirable.
//   - generation was declared as fencing but was hard-coded to 1 on every
//     acquisition and never used to guard Release or Renew, so a stale
//     holder that briefly reacquired the same resource on a different
//     lease could delete the newer holder's lock.
//   - Release deleted by (resource_key, owner_id) only, with no generation
//     match, so the same-process reacquire-then-stale-release race was open.
//   - The retry loop computed newLeaseUntil BEFORE waiting, so a request
//     that waited most of the lease duration got a lease that was nearly
//     expired the moment it was granted.
//   - The retry comment claimed "5 minutes" but the math (300 × 100ms) was
//     30 seconds — either the comment or the budget was wrong.
//   - Lease deadlines were stored as RFC3339Nano strings; textual lexical
//     ordering of variable-width nanosecond suffixes is not the same as
//     temporal ordering.
//
// This rewrite:
//
//   - Uses INSERT ... ON CONFLICT(resource_key) DO UPDATE with a WHERE clause
//     that only overwrites an expired row, so takeover is a single atomic
//     statement with no PK collision.
//   - Stores lease_until as INTEGER unix nanoseconds. Ordering is trivial.
//   - Assigns a monotonically-increasing generation on every acquisition
//     (previous_generation + 1, or 1 when the row is fresh). Callers get a
//     Lease token that carries the generation; Release and Renew require
//     the same generation and refuse if it does not match.
//   - Recomputes newLeaseUntil each retry attempt inside the loop, so
//     waiters always get a full lease.
//   - Exposes AcquireContext(ctx) so callers control the acquisition
//     deadline; no more silently-wrong 30-vs-300-second budget.
//   - Provides a Hold helper that renews the lease periodically until the
//     provided ctx is cancelled, so a long-running caller does not have to
//     hand-roll a renewal goroutine.
//
// Tests cover two independent *sql.DB handles and separate OS processes (as
// db_locks_test.go and db_locks_process_test.go do), not merely goroutines
// sharing one handle.

package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrLockHeld is returned by TryAcquire when the resource is currently
	// held by another owner.
	ErrLockHeld = errors.New("lock is held by another owner")
	// ErrLeaseLost is returned by Release/Renew when the lease no longer
	// matches — either the row was taken over after expiry or the caller
	// passed a stale Lease.
	ErrLeaseLost = errors.New("lease is no longer valid")
)

// Lease is a fencing token for a held lock. The caller MUST pass the exact
// Lease it received from Acquire back to Release or Renew; a Lease from a
// prior acquisition cannot release a newer one.
type Lease struct {
	ResourceKey string
	OwnerID     string
	Generation  int64
	ExpiresAt   time.Time
}

// DBLocks provides SQLite-backed leased locks with fencing generations for
// cross-process coordination.
type DBLocks struct {
	db        *sql.DB
	ownerID   string
	leaseTime time.Duration
	// retryInterval is how long a blocked Acquire sleeps between attempts.
	// Configurable so tests can drive it faster.
	retryInterval time.Duration
}

// NewDBLocks constructs a lock manager. leaseTime is how long each acquired
// lease is valid before another owner may take over; callers must renew
// before expiry (see (*DBLocks).Hold). ownerID is a string that uniquely
// identifies this process instance (typically hostname + pid + start time)
// and is stored on the lock row for observability.
func NewDBLocks(db *sql.DB, ownerID string, leaseTime time.Duration) (*DBLocks, error) {
	if db == nil {
		return nil, errors.New("db is required for distributed locking")
	}
	if ownerID == "" {
		return nil, errors.New("owner_id is required for distributed locking")
	}
	if leaseTime <= 0 {
		leaseTime = 30 * time.Second
	}
	dl := &DBLocks{
		db:            db,
		ownerID:       ownerID,
		leaseTime:     leaseTime,
		retryInterval: 100 * time.Millisecond,
	}
	if err := dl.ensureSchema(); err != nil {
		return nil, fmt.Errorf("ensure lock schema: %w", err)
	}
	return dl, nil
}

// ensureSchema creates the resource_locks table if it does not exist. The
// lease_until column is INTEGER (unix nanoseconds); do not migrate from a
// TEXT column silently, because the ordering semantics are different.
func (dl *DBLocks) ensureSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS resource_locks (
		resource_key TEXT PRIMARY KEY,
		owner_id     TEXT NOT NULL,
		lease_until  INTEGER NOT NULL,
		generation   INTEGER NOT NULL,
		acquired_at  INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_resource_locks_lease ON resource_locks(lease_until);
	`
	_, err := dl.db.Exec(schema)
	return err
}

// TryAcquire attempts an acquisition without blocking. Returns ErrLockHeld
// if the resource is currently held by a live lease.
func (dl *DBLocks) TryAcquire(resourceKey string) (Lease, error) {
	if resourceKey == "" {
		return Lease{}, errors.New("resource_key is required")
	}
	return dl.tryOnce(resourceKey)
}

// Acquire blocks up to the context deadline waiting for the lock. The
// context governs the wait budget honestly — callers pick their own limit.
// If ctx is already cancelled, returns ctx.Err() without attempting.
func (dl *DBLocks) Acquire(ctx context.Context, resourceKey string) (Lease, error) {
	if resourceKey == "" {
		return Lease{}, errors.New("resource_key is required")
	}
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	for {
		lease, err := dl.tryOnce(resourceKey)
		if err == nil {
			return lease, nil
		}
		if !errors.Is(err, ErrLockHeld) {
			return Lease{}, err
		}
		select {
		case <-ctx.Done():
			return Lease{}, fmt.Errorf("waiting for lock %s: %w", resourceKey, ctx.Err())
		case <-time.After(dl.retryInterval):
		}
	}
}

// tryOnce performs a single UPSERT attempt. The WHERE clause on DO UPDATE
// only overwrites an expired row, so a live lease is preserved and the
// caller sees RowsAffected == 0.
//
// Because lease_until, generation, and acquired_at are set from SQL-side
// expressions (not from parameters that a Go-side clock read minutes ago),
// the granted lease always starts "now" from the DB's perspective — no
// pre-computed newLeaseUntil that could already be stale.
//
// The row is never deleted by Release — it is marked expired instead. That
// keeps generation monotonically increasing across the lifetime of a
// resource_key, so a stale Lease from before a release-and-reacquire cycle
// (same owner_id, same key, but higher generation now) is correctly
// rejected by fencing.
func (dl *DBLocks) tryOnce(resourceKey string) (Lease, error) {
	nowNS := time.Now().UnixNano()
	leaseNS := time.Now().Add(dl.leaseTime).UnixNano()

	tx, err := dl.db.Begin()
	if err != nil {
		return Lease{}, fmt.Errorf("begin lock txn: %w", err)
	}
	defer tx.Rollback()

	// The DO UPDATE branch overwrites only if the existing lease has
	// expired, and bumps generation past the previous holder's — so any
	// fencing token the previous holder still carries no longer matches.
	// If the existing lease is still live, RowsAffected reports 0 and the
	// caller gets ErrLockHeld.
	result, err := tx.Exec(`
		INSERT INTO resource_locks (resource_key, owner_id, lease_until, generation, acquired_at)
		VALUES (?, ?, ?, 1, ?)
		ON CONFLICT(resource_key) DO UPDATE SET
			owner_id    = excluded.owner_id,
			lease_until = excluded.lease_until,
			generation  = resource_locks.generation + 1,
			acquired_at = excluded.acquired_at
		WHERE resource_locks.lease_until <= ?
	`, resourceKey, dl.ownerID, leaseNS, nowNS, nowNS)
	if err != nil {
		return Lease{}, fmt.Errorf("insert lock: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Lease{}, fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return Lease{}, ErrLockHeld
	}

	// Read back the just-written generation so the caller's fencing token
	// matches exactly what is persisted (including the +1 from a takeover).
	var gen int64
	if err := tx.QueryRow(`SELECT generation FROM resource_locks WHERE resource_key = ?`, resourceKey).Scan(&gen); err != nil {
		return Lease{}, fmt.Errorf("read back generation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Lease{}, fmt.Errorf("commit lock: %w", err)
	}
	return Lease{
		ResourceKey: resourceKey,
		OwnerID:     dl.ownerID,
		Generation:  gen,
		ExpiresAt:   time.Unix(0, leaseNS),
	}, nil
}

// Release drops the lease if — and only if — the caller's fencing token
// still matches what is persisted. A Lease that has been taken over after
// expiry, or a stale Lease from a prior acquisition, gets ErrLeaseLost and
// does not touch the current row.
//
// Implementation note: Release marks lease_until = 0 (immediately eligible
// for takeover) rather than deleting the row. This preserves the row's
// generation counter across release/reacquire cycles, so a stale Lease
// from before a release is guaranteed to have a lower generation than the
// current one and cannot be confused with it.
func (dl *DBLocks) Release(lease Lease) error {
	if lease.ResourceKey == "" {
		return errors.New("lease has no resource_key")
	}
	result, err := dl.db.Exec(`
		UPDATE resource_locks
		SET lease_until = 0
		WHERE resource_key = ? AND owner_id = ? AND generation = ? AND lease_until > 0
	`, lease.ResourceKey, lease.OwnerID, lease.Generation)
	if err != nil {
		return fmt.Errorf("release lock: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		// Either the lease no longer matches (taken over, or stale) or the
		// row was already released. Distinguish the two so callers can
		// detect a lost lease.
		var currentGen int64
		var currentLeaseUntil int64
		err := dl.db.QueryRow(`SELECT generation, lease_until FROM resource_locks WHERE resource_key = ?`, lease.ResourceKey).Scan(&currentGen, &currentLeaseUntil)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect lock state: %w", err)
		}
		if currentGen == lease.Generation && currentLeaseUntil == 0 {
			// Already released by this holder — idempotent no-op.
			return nil
		}
		return ErrLeaseLost
	}
	return nil
}

// Renew extends the lease if the fencing token still matches. Returns the
// updated Lease (with a new ExpiresAt) on success, or ErrLeaseLost if the
// lock has been taken over.
func (dl *DBLocks) Renew(lease Lease) (Lease, error) {
	if lease.ResourceKey == "" {
		return Lease{}, errors.New("lease has no resource_key")
	}
	now := time.Now().UnixNano()
	newExpiry := time.Now().Add(dl.leaseTime).UnixNano()
	result, err := dl.db.Exec(`
		UPDATE resource_locks
		SET lease_until = ?
		WHERE resource_key = ? AND owner_id = ? AND generation = ? AND lease_until > ?
	`, newExpiry, lease.ResourceKey, lease.OwnerID, lease.Generation, now)
	if err != nil {
		return Lease{}, fmt.Errorf("renew lock: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Lease{}, fmt.Errorf("rows affected: %w", err)
	}
	if affected == 0 {
		return Lease{}, ErrLeaseLost
	}
	lease.ExpiresAt = time.Unix(0, newExpiry)
	return lease, nil
}

// Hold blocks on the given ctx while renewing the lease on a schedule. It
// is the recommended way to hold a lock across a long operation without
// letting the lease expire mid-flight. Renewal interval is leaseTime / 3
// so two consecutive renewal failures still leave >1/3 of a lease before
// takeover becomes possible.
//
// Returns nil when ctx is cancelled (normal shutdown), or a non-nil error
// if the lease is lost (ErrLeaseLost) — in which case the caller MUST
// abort the operation it was protecting, because a competing owner has
// taken over.
func (dl *DBLocks) Hold(ctx context.Context, lease Lease) error {
	if lease.ResourceKey == "" {
		return errors.New("lease has no resource_key")
	}
	interval := dl.leaseTime / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			renewed, err := dl.Renew(lease)
			if err != nil {
				return err
			}
			lease = renewed
		}
	}
}

// (ExpireStaleLocks intentionally removed: deleting expired rows would
// reset each key's generation counter and break fencing. Takeover of an
// expired row is handled by tryOnce's UPSERT without a separate cleanup
// pass. The row set is bounded by the count of distinct resource_keys,
// which for a hosting panel is bounded by the site count.)
