// Package operations: distributed locking for multi-process safety
//
// DistributedLocks provides cross-process synchronization for site mutations.
// This is essential when panel and worker processes run simultaneously.

package operations

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DistributedLocks manages file-based locks for inter-process synchronization.
// Each operation key has a corresponding lock file. Lock acquisition creates
// an exclusive lock file; lock release removes it.
//
// Lock files use os.O_EXCL to ensure atomic exclusive creation across processes.
// Lock expiry is enforced via file mtime comparison to handle process crashes.
type DistributedLocks struct {
	lockDir string
	timeout time.Duration
}

// NewDistributedLocks creates a distributed lock manager for the given directory.
// lockDir will be created if it doesn't exist. timeout is the maximum duration
// a lock can be held before being forcibly released (handles crashed processes).
func NewDistributedLocks(lockDir string, timeout time.Duration) (*DistributedLocks, error) {
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}
	return &DistributedLocks{
		lockDir: lockDir,
		timeout: timeout,
	}, nil
}

// Acquire obtains an exclusive lock for the given key.
// Returns a function that releases the lock when called.
// Blocks if another process holds the lock (respects timeout).
//
// Lock files are named .lockdir/key.lock and contain the PID of lock holder.
func (dl *DistributedLocks) Acquire(key string) (func(), error) {
	if key == "" {
		return func() {}, nil
	}

	lockFile := filepath.Join(dl.lockDir, key+".lock")
	pid := os.Getpid()
	pidStr := fmt.Sprintf("%d", pid)

	// Try to acquire lock with exponential backoff
	deadline := time.Now().Add(5 * time.Minute) // Max wait time

	for {
		// Check for stale locks (crashed process)
		if info, err := os.Stat(lockFile); err == nil {
			age := time.Since(info.ModTime())
			if age > dl.timeout {
				// Lock is stale, remove it
				_ = os.Remove(lockFile)
			} else {
				// Lock is held by another process
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("timeout acquiring lock for %s", key)
				}
				// Wait before retrying
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}

		// Try to create lock file exclusively
		file, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			// Lock acquired! Write PID and close
			fmt.Fprint(file, pidStr)
			file.Close()

			// Return unlock function
			return func() {
				_ = os.Remove(lockFile)
			}, nil
		}

		// os.IsExist means another process has the lock
		if errors.Is(err, os.ErrExist) {
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("timeout acquiring lock for %s", key)
			}
			// Wait before retrying
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Some other error
		return nil, fmt.Errorf("acquire lock %s: %w", key, err)
	}
}

// TryAcquire attempts to acquire a lock without blocking.
// Returns the unlock function if successful, or an error if the lock is held.
func (dl *DistributedLocks) TryAcquire(key string) (func(), error) {
	if key == "" {
		return func() {}, nil
	}

	lockFile := filepath.Join(dl.lockDir, key+".lock")

	// Check for stale locks
	if info, err := os.Stat(lockFile); err == nil {
		age := time.Since(info.ModTime())
		if age > dl.timeout {
			_ = os.Remove(lockFile)
		} else {
			return nil, fmt.Errorf("lock held for %s", key)
		}
	}

	// Try to create lock file
	file, err := os.OpenFile(lockFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("lock held for %s", key)
		}
		return nil, fmt.Errorf("acquire lock %s: %w", key, err)
	}

	pid := os.Getpid()
	fmt.Fprintf(file, "%d", pid)
	file.Close()

	return func() {
		_ = os.Remove(lockFile)
	}, nil
}

// ReleaseStale removes any locks older than the configured timeout.
// Called periodically to clean up locks from crashed processes.
func (dl *DistributedLocks) ReleaseStale() error {
	entries, err := os.ReadDir(dl.lockDir)
	if err != nil {
		return fmt.Errorf("read lock directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".lock" {
			lockPath := filepath.Join(dl.lockDir, entry.Name())
			info, err := os.Stat(lockPath)
			if err == nil {
				if time.Since(info.ModTime()) > dl.timeout {
					_ = os.Remove(lockPath)
				}
			}
		}
	}
	return nil
}

// IMPORTANT: Code that uses Locks (process-local) must transition to
// DistributedLocks (file-based) for multi-process safety.
//
// Migration path:
// 1. Keep local Locks for in-process goroutine coordination
// 2. Add DistributedLocks for panel/worker coordination
// 3. Call Acquire on DistributedLocks before calling local Locks.Acquire
//
// Example:
//   distributedUnlock, err := a.distributedLocks.Acquire(site)
//   if err != nil {
//       http.Error(w, "could not acquire lock", 503)
//       return
//   }
//   defer distributedUnlock()
//
//   localUnlock := a.siteOperations.Acquire(site)  // Still need this for goroutines
//   defer localUnlock()
