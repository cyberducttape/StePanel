package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/operations"
	_ "modernc.org/sqlite"
)

func TestAcquireSiteMutationLocksFencesIndependentAppInstances(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control-plane.sqlite")
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	dbA, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()

	locksA, err := operations.NewDBLocks(dbA, "panel-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	locksB, err := operations.NewDBLocks(dbB, "panel-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{dbLocks: locksA}
	b := &App{dbLocks: locksB}

	releaseA, err := a.acquireSiteMutationLocks(context.Background(), "site-a", "vhost:site-a-example")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := b.acquireSiteMutationLocks(ctx, "vhost:site-a-example", "site-a"); err == nil {
		t.Fatal("independent app acquired a live compound site lock")
	}

	releaseA()
	releaseB, err := b.acquireSiteMutationLocks(context.Background(), "site-a", "vhost:site-a-example")
	if err != nil {
		t.Fatalf("lock was not released for the next owner: %v", err)
	}
	releaseB()
}

func TestAcquireSiteMutationLockContextCancelsAfterLeaseLoss(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control-plane.sqlite")
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	dbA, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()

	locksA, err := operations.NewDBLocks(dbA, "panel-a", 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locksB, err := operations.NewDBLocks(dbB, "panel-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{dbLocks: locksA}

	operationCtx, release, err := a.acquireSiteMutationLockContext(context.Background(), "site-loss")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	time.Sleep(100 * time.Millisecond)
	if _, err := locksB.TryAcquire("site-loss"); err != nil {
		t.Fatalf("takeover failed: %v", err)
	}
	select {
	case <-operationCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("operation context was not cancelled after durable lease loss")
	}
}

func TestAcquireSiteMutationLocksContextCancelsWhenAnyLeaseIsLost(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control-plane.sqlite")
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	dbA, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer dbA.Close()
	dbB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer dbB.Close()
	locksA, err := operations.NewDBLocks(dbA, "panel-a", 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locksB, err := operations.NewDBLocks(dbB, "panel-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{dbLocks: locksA}
	operationCtx, release, err := a.acquireSiteMutationLocksContext(context.Background(), "site-compound", "vhost:compound")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	time.Sleep(100 * time.Millisecond)
	if _, err := locksB.TryAcquire("vhost:compound"); err != nil {
		t.Fatalf("compound-lock takeover failed: %v", err)
	}
	select {
	case <-operationCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("compound operation context was not cancelled after one lease was lost")
	}
}
