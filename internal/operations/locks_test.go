package operations

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestLocksSerializeSameKey(t *testing.T) {
	var locks Locks
	releaseFirst := locks.Acquire("demo")
	started := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		release := locks.Acquire("demo")
		close(started)
		release()
		close(finished)
	}()
	select {
	case <-started:
		t.Fatal("same-key operation acquired concurrently")
	case <-time.After(20 * time.Millisecond):
	}
	releaseFirst()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("same-key operation did not acquire after release")
	}
	if locks.Len() != 0 {
		t.Fatalf("lock entries leaked: %d", locks.Len())
	}
}

func TestLocksAcquireManySkipsEmptyAndDuplicateKeys(t *testing.T) {
	var locks Locks
	release := locks.AcquireMany("", "demo", "demo", "")
	if locks.Len() != 1 {
		t.Fatalf("lock count = %d, want 1", locks.Len())
	}
	release()
	if locks.Len() != 0 {
		t.Fatalf("lock count after release = %d", locks.Len())
	}
}

func TestDBLocksRejectsInvalidInputsAndCancelledAcquire(t *testing.T) {
	db, err := sql.Open("sqlite", "file:invalid-lock-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := NewDBLocks(nil, "owner", time.Second); err == nil {
		t.Fatal("nil database was accepted")
	}
	if _, err := NewDBLocks(db, "", time.Second); err == nil {
		t.Fatal("empty owner was accepted")
	}
	locks, err := NewDBLocks(db, "owner", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locks.TryAcquire(""); err == nil {
		t.Fatal("empty resource key was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := locks.Acquire(ctx, "site-cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquire = %v", err)
	}
}

func TestLocksAcquireManyUsesStableOrder(t *testing.T) {
	var locks Locks
	first := locks.AcquireMany("site-b", "site-a")
	finished := make(chan struct{})
	go func() {
		second := locks.AcquireMany("site-a", "site-b")
		second()
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("related operation acquired before release")
	case <-time.After(20 * time.Millisecond):
	}
	first()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("related operation did not acquire after release")
	}
}
