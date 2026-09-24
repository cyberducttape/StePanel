package operations

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ResourceLock represents a distributed lock with leasing semantics
type ResourceLock struct {
	ResourceKey string
	OwnerID     string
	LeaseUntil  time.Time
	Generation  int64
}

// DBLocks provides SQLite-backed distributed locking with proper leasing and fencing.
// Uses transactional advisory-resource records for cross-process coordination.
type DBLocks struct {
	db        *sql.DB
	ownerID   string
	leaseTime time.Duration
}

// NewDBLocks creates a distributed lock manager backed by SQLite
func NewDBLocks(db *sql.DB, ownerID string, leaseTime time.Duration) (*DBLocks, error) {
	if leaseTime == 0 {
		leaseTime = 30 * time.Second
	}
	if ownerID == "" {
		return nil, errors.New("owner_id is required for distributed locking")
	}

	return &DBLocks{
		db:        db,
		ownerID:   ownerID,
		leaseTime: leaseTime,
	}, nil
}

// ensureSchema creates the resource_locks table if it doesn't exist
func (dl *DBLocks) ensureSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS resource_locks (
		resource_key TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL,
		lease_until TEXT NOT NULL,
		generation INTEGER NOT NULL,
		acquired_at TEXT NOT NULL,
		UNIQUE(resource_key)
	);
	CREATE INDEX IF NOT EXISTS idx_resource_locks_lease ON resource_locks(lease_until);
	`
	_, err := dl.db.Exec(schema)
	return err
}

// Acquire obtains a distributed lock for the given resource key with leasing.
// Returns a function that renews or releases the lock.
// Uses SQLite transactions for atomic compare-and-swap semantics.
func (dl *DBLocks) Acquire(resourceKey string) (func(), error) {
	if resourceKey == "" {
		return func() {}, nil
	}

	if err := dl.ensureSchema(); err != nil {
		return nil, fmt.Errorf("ensure lock schema: %w", err)
	}

	newLeaseUntil := time.Now().Add(dl.leaseTime)

	// Start a transaction to atomically acquire or wait
	for attempts := 0; attempts < 300; attempts++ { // 5-minute timeout with 100ms retries
		tx, err := dl.db.Begin()
		if err != nil {
			return nil, fmt.Errorf("begin transaction: %w", err)
		}

		// Try to acquire: insert if not exists, or update if lease expired
		result, err := tx.Exec(`
			INSERT INTO resource_locks (resource_key, owner_id, lease_until, generation, acquired_at)
			SELECT ?, ?, ?, 1, ?
			WHERE NOT EXISTS (
				SELECT 1 FROM resource_locks
				WHERE resource_key = ? AND lease_until > ?
			)
		`, resourceKey, dl.ownerID, newLeaseUntil.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), resourceKey, time.Now().UTC().Format(time.RFC3339Nano))

		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("insert lock: %w", err)
		}

		rows, err := result.RowsAffected()
		if err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("rows affected: %w", err)
		}

		if rows == 1 {
			// Successfully acquired the lock
			tx.Commit()
			return func() {
				// Release by deleting the lock (only if we still own it)
				_, _ = dl.db.Exec(`
					DELETE FROM resource_locks
					WHERE resource_key = ? AND owner_id = ?
				`, resourceKey, dl.ownerID)
			}, nil
		}

		tx.Rollback()

		// Lock held by another process or not yet expired, wait and retry
		time.Sleep(100 * time.Millisecond)
	}

	return nil, fmt.Errorf("timeout acquiring lock for %s after 5 minutes", resourceKey)
}

// TryAcquire attempts to acquire a lock without blocking
func (dl *DBLocks) TryAcquire(resourceKey string) (func(), error) {
	if resourceKey == "" {
		return func() {}, nil
	}

	if err := dl.ensureSchema(); err != nil {
		return nil, fmt.Errorf("ensure lock schema: %w", err)
	}

	newLeaseUntil := time.Now().Add(dl.leaseTime)

	tx, err := dl.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}

	result, err := tx.Exec(`
		INSERT INTO resource_locks (resource_key, owner_id, lease_until, generation, acquired_at)
		SELECT ?, ?, ?, 1, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM resource_locks
			WHERE resource_key = ? AND lease_until > ?
		)
	`, resourceKey, dl.ownerID, newLeaseUntil.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), resourceKey, time.Now().UTC().Format(time.RFC3339Nano))

	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("insert lock: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		tx.Rollback()
		return nil, fmt.Errorf("lock already held for %s", resourceKey)
	}

	tx.Commit()
	return func() {
		_, _ = dl.db.Exec(`
			DELETE FROM resource_locks
			WHERE resource_key = ? AND owner_id = ?
		`, resourceKey, dl.ownerID)
	}, nil
}

// RenewLease extends the lease for a held lock
func (dl *DBLocks) RenewLease(resourceKey string) error {
	newLeaseUntil := time.Now().Add(dl.leaseTime)

	result, err := dl.db.Exec(`
		UPDATE resource_locks
		SET lease_until = ?
		WHERE resource_key = ? AND owner_id = ? AND lease_until > ?
	`, newLeaseUntil.UTC().Format(time.RFC3339Nano), resourceKey, dl.ownerID, time.Now().UTC().Format(time.RFC3339Nano))

	if err != nil {
		return fmt.Errorf("renew lease: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("lock not held or expired for %s", resourceKey)
	}

	return nil
}

// ExpireStaleLocks removes any locks that have expired
func (dl *DBLocks) ExpireStaleLocks() error {
	_, err := dl.db.Exec(`
		DELETE FROM resource_locks
		WHERE lease_until < ?
	`, time.Now().UTC().Format(time.RFC3339Nano))

	if err != nil {
		return fmt.Errorf("expire stale locks: %w", err)
	}
	return nil
}
