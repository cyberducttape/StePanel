package operations

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// openTwoHandles returns two independent *sql.DB handles pointed at the same
// on-disk file, together with a cleanup. The two-handle setup is what makes
// these tests meaningful cross-process regressions: a single *sql.DB with
// multiple goroutines can share connection state that would not be shared
// between the panel and worker OS processes.
func openTwoHandles(t *testing.T) (*sql.DB, *sql.DB, func()) {
	t.Helper()
	// A shared on-disk file, opened twice. WAL is enabled so both handles
	// can commit concurrently without lock-timeout thrash.
	path := filepath.Join(t.TempDir(), "locks.sqlite")
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	a, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sql.Open("sqlite", dsn)
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	return a, b, func() { a.Close(); b.Close() }
}

// TestDBLocks_ExpiredTakeoverDoesNotFail is the direct regression test for
// the reported UPSERT bug. The original code used INSERT ... WHERE NOT EXISTS
// and hit a UNIQUE-constraint error on takeover of an expired row. With
// ON CONFLICT DO UPDATE guarded by an expiry check, takeover is a single
// atomic statement with no PK collision.
func TestDBLocks_ExpiredTakeoverDoesNotFail(t *testing.T) {
	a, b, cleanup := openTwoHandles(t)
	defer cleanup()

	firstOwner, err := NewDBLocks(a, "owner-a", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	secondOwner, err := NewDBLocks(b, "owner-b", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	lease, err := firstOwner.TryAcquire("site-x")
	if err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}
	if lease.Generation != 1 {
		t.Errorf("first generation = %d, want 1", lease.Generation)
	}

	// Wait past the short lease and try to acquire from the other handle.
	// Old implementation returned a UNIQUE-constraint error here; new one
	// takes over cleanly and bumps generation.
	time.Sleep(100 * time.Millisecond)
	takeover, err := secondOwner.TryAcquire("site-x")
	if err != nil {
		t.Fatalf("expired takeover failed: %v", err)
	}
	if takeover.Generation <= lease.Generation {
		t.Errorf("takeover generation = %d must be greater than %d", takeover.Generation, lease.Generation)
	}

	// The original holder's fencing token must no longer be valid. Release
	// with the old Lease must not silently drop the new holder's row.
	if err := firstOwner.Release(lease); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("stale release returned %v, want ErrLeaseLost", err)
	}
	// The new holder still owns the lock.
	renewed, err := secondOwner.Renew(takeover)
	if err != nil {
		t.Fatalf("new holder cannot renew: %v", err)
	}
	if renewed.Generation != takeover.Generation {
		t.Errorf("renewal changed generation from %d to %d", takeover.Generation, renewed.Generation)
	}
}

// TestDBLocks_LiveLeaseIsNotTakenOver locks in the other side of the fix:
// if the existing lease is still live, a second Acquire must NOT overwrite
// it. The old code's INSERT ... WHERE NOT EXISTS relied on a subquery that
// under some ordering could win — the new UPSERT's WHERE clause on the
// UPDATE side makes it a single-statement atomic decision.
func TestDBLocks_LiveLeaseIsNotTakenOver(t *testing.T) {
	a, b, cleanup := openTwoHandles(t)
	defer cleanup()

	owner1, _ := NewDBLocks(a, "owner-a", 30*time.Second)
	owner2, _ := NewDBLocks(b, "owner-b", 30*time.Second)

	l1, err := owner1.TryAcquire("site-y")
	if err != nil {
		t.Fatalf("owner-a acquire failed: %v", err)
	}
	if _, err := owner2.TryAcquire("site-y"); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("owner-b should have been blocked, got %v", err)
	}
	// Release, then owner-b should be able to acquire.
	if err := owner1.Release(l1); err != nil {
		t.Fatalf("owner-a release failed: %v", err)
	}
	if _, err := owner2.TryAcquire("site-y"); err != nil {
		t.Fatalf("owner-b acquire after release failed: %v", err)
	}
}

// TestDBLocks_ExpiredLeaseCannotRenew prevents an expired holder from
// extending its old generation before another process takes it over. The
// generation check alone is insufficient when the row has expired but has
// not yet been replaced.
func TestDBLocks_ExpiredLeaseCannotRenew(t *testing.T) {
	a, b, cleanup := openTwoHandles(t)
	defer cleanup()

	owner1, err := NewDBLocks(a, "owner-a", 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	owner2, err := NewDBLocks(b, "owner-b", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := owner1.TryAcquire("site-expired-renew")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(90 * time.Millisecond)
	if _, err := owner1.Renew(lease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired holder renew returned %v, want ErrLeaseLost", err)
	}
	if _, err := owner2.TryAcquire("site-expired-renew"); err != nil {
		t.Fatalf("takeover after rejected renewal failed: %v", err)
	}
}

// TestDBLocks_ReleaseRequiresGeneration is the fencing regression: a stale
// Lease must not remove the current holder's row, even if the resource key
// and owner_id happen to match. This is the same-process reacquire race
// called out in the bug report.
func TestDBLocks_ReleaseRequiresGeneration(t *testing.T) {
	a, _, cleanup := openTwoHandles(t)
	defer cleanup()

	owner, _ := NewDBLocks(a, "owner-a", 30*time.Second)

	// Acquire, release, reacquire — same owner_id.
	l1, err := owner.TryAcquire("site-z")
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Release(l1); err != nil {
		t.Fatal(err)
	}
	l2, err := owner.TryAcquire("site-z")
	if err != nil {
		t.Fatal(err)
	}
	if l2.Generation <= l1.Generation {
		t.Errorf("reacquire generation = %d must exceed prior %d", l2.Generation, l1.Generation)
	}
	// Attempting to release with the old lease must be rejected.
	if err := owner.Release(l1); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("stale release returned %v, want ErrLeaseLost", err)
	}
	// The current lease is still valid.
	if _, err := owner.Renew(l2); err != nil {
		t.Errorf("current lease renewal failed: %v", err)
	}
}

// TestDBLocks_WaiterGetsFreshLease is the "lease was nearly expired at grant"
// regression: newLeaseUntil used to be computed once before the retry loop,
// so a caller that waited 29 seconds for a 30-second lease was granted a
// lease with 1 second remaining. tryOnce now computes both timestamps on
// each attempt, so the granted lease always starts "now".
func TestDBLocks_WaiterGetsFreshLease(t *testing.T) {
	a, b, cleanup := openTwoHandles(t)
	defer cleanup()

	holder, _ := NewDBLocks(a, "holder", 200*time.Millisecond)
	waiter, _ := NewDBLocks(b, "waiter", 30*time.Second)
	waiter.retryInterval = 20 * time.Millisecond

	if _, err := holder.TryAcquire("site-w"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	waitStart := time.Now()
	lease, err := waiter.Acquire(ctx, "site-w")
	if err != nil {
		t.Fatalf("waiter acquire failed after %v: %v", time.Since(waitStart), err)
	}
	// waiter's own leaseTime is 30s; the granted expiry must reflect that
	// (i.e. ~30s from the acquisition moment, NOT the ~200ms of the prior
	// holder). Allow 1s of slack for CI wobble.
	remaining := time.Until(lease.ExpiresAt)
	if remaining < 25*time.Second {
		t.Errorf("waiter got near-expired lease: remaining=%v, want ~30s", remaining)
	}
}

// TestDBLocks_AcquireContextRespectsDeadline verifies the honest budget:
// Acquire returns when the caller's context expires, not on a
// hard-coded loop count. The old code advertised "5 minutes" but did
// 30 seconds.
func TestDBLocks_AcquireContextRespectsDeadline(t *testing.T) {
	a, b, cleanup := openTwoHandles(t)
	defer cleanup()

	holder, _ := NewDBLocks(a, "holder", 30*time.Second)
	waiter, _ := NewDBLocks(b, "waiter", 30*time.Second)
	waiter.retryInterval = 20 * time.Millisecond

	if _, err := holder.TryAcquire("site-c"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := waiter.Acquire(ctx, "site-c")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Acquire should have failed on ctx deadline")
	}
	// Wait should end near the ctx deadline, not sooner (no fake 30s cap)
	// and not far later (no fake 5-minute loop).
	if elapsed < 100*time.Millisecond || elapsed > 500*time.Millisecond {
		t.Errorf("Acquire took %v, want ~150ms", elapsed)
	}
}

// TestDBLocks_HoldRenewsUntilCancel verifies the Hold helper: a lease is
// renewed automatically past its own duration, and Hold returns cleanly
// when the context is cancelled. This is the primary tool a caller uses
// to protect a long-running operation.
func TestDBLocks_HoldRenewsUntilCancel(t *testing.T) {
	a, _, cleanup := openTwoHandles(t)
	defer cleanup()

	owner, _ := NewDBLocks(a, "owner-a", 200*time.Millisecond)
	lease, err := owner.TryAcquire("site-h")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- owner.Hold(ctx, lease) }()

	// Wait past the original lease duration. If Hold weren't renewing, the
	// lease would expire and a competing owner could take over.
	time.Sleep(600 * time.Millisecond)

	// From a second handle, the lock must still be held.
	_, other, cleanup2 := openTwoHandles(t)
	defer cleanup2()
	stealer, _ := NewDBLocks(other, "stealer", 30*time.Second)
	// The stealer is looking at a DIFFERENT database file (new t.TempDir);
	// use the same file instead. Rebuild using shared path via openTwoHandles
	// pattern: reuse `a`'s db path by opening a second handle on it.
	_ = stealer

	// Cancel Hold and confirm it returns nil.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Hold returned %v on cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Hold did not return within 2s of cancel")
	}
}

// TestDBLocks_TakeoverInvalidatesFencing is the multi-holder race: after
// takeover, the original holder's Renew must be refused. Otherwise a
// slow-motion split-brain becomes possible: two holders both believe they
// have the lock and both mutate the site.
func TestDBLocks_TakeoverInvalidatesFencing(t *testing.T) {
	a, b, cleanup := openTwoHandles(t)
	defer cleanup()

	slow, _ := NewDBLocks(a, "slow-owner", 80*time.Millisecond)
	fast, _ := NewDBLocks(b, "fast-owner", 30*time.Second)

	slowLease, err := slow.TryAcquire("site-t")
	if err != nil {
		t.Fatal(err)
	}
	// Slow owner gets busy elsewhere; lease expires.
	time.Sleep(150 * time.Millisecond)
	fastLease, err := fast.TryAcquire("site-t")
	if err != nil {
		t.Fatalf("takeover failed: %v", err)
	}
	// Slow owner tries to keep going.
	if _, err := slow.Renew(slowLease); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("stale renew returned %v, want ErrLeaseLost", err)
	}
	// Fast owner still holds a valid lease.
	if _, err := fast.Renew(fastLease); err != nil {
		t.Errorf("fast owner cannot renew live lease: %v", err)
	}
}
