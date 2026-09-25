package main

import (
	"context"
	"database/sql"
	"os"
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

func TestAdversarialMutationLockScenariosSerializeAcrossConnections(t *testing.T) {
	scenarios := []struct {
		name   string
		first  []string
		second []string
	}{
		{name: "restore-delete", first: []string{"site:restore-delete"}, second: []string{"site:restore-delete"}},
		{name: "deploy-restore", first: []string{"site:deploy-restore"}, second: []string{"site:deploy-restore"}},
		{name: "route-termination", first: []string{"site:route-termination", "vhost:route-termination"}, second: []string{"site:route-termination"}},
		{name: "resource-suspension", first: []string{"site:resource-suspension", "account:customer"}, second: []string{"account:customer"}},
		{name: "backup-filesystem-restore", first: []string{"site:backup-restore"}, second: []string{"site:backup-restore"}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
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
			locksA, err := operations.NewDBLocks(dbA, "scenario-a", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			locksB, err := operations.NewDBLocks(dbB, "scenario-b", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			firstApp := &App{dbLocks: locksA}
			secondApp := &App{dbLocks: locksB}
			release, err := firstApp.acquireSiteMutationLocks(context.Background(), scenario.first...)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if _, err := secondApp.acquireSiteMutationLocks(ctx, scenario.second...); err == nil {
				release()
				t.Fatal("conflicting mutation acquired locks concurrently")
			}
			release()
			reacquired, err := secondApp.acquireSiteMutationLocks(context.Background(), scenario.second...)
			if err != nil {
				t.Fatalf("lock was not available after first operation released: %v", err)
			}
			reacquired()
		})
	}
}

func TestSiteMutationLockSerializesRealHelperBoundaryAcrossApps(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "helper-critical-section")
	overlap := filepath.Join(root, "helper-overlap")
	helper := filepath.Join(root, "site-helper.sh")
	script := `#!/bin/sh
set -eu
mkdir -p "$STEPANEL_HELPER_MARKER"
if ! mkdir "$STEPANEL_HELPER_MARKER.lock" 2>/dev/null; then
  : > "$STEPANEL_HELPER_OVERLAP"
  exit 97
fi
trap 'rmdir "$STEPANEL_HELPER_MARKER.lock"' EXIT
: > "$STEPANEL_HELPER_MARKER.started"
sleep 0.2
`
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STEPANEL_HELPER_MARKER", marker)
	t.Setenv("STEPANEL_HELPER_OVERLAP", overlap)

	dsn := "file:" + filepath.Join(root, "control-plane.sqlite") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
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
	locksA, err := operations.NewDBLocks(dbA, "helper-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	locksB, err := operations.NewDBLocks(dbB, "helper-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Config: Config{SiteCtl: helper}, dbLocks: locksA}
	b := &App{Config: Config{SiteCtl: helper}, dbLocks: locksB}

	firstDone := make(chan error, 1)
	go func() {
		ctx, release, err := a.acquireSiteMutationLockContext(context.Background(), "site:helper-boundary")
		if err != nil {
			firstDone <- err
			return
		}
		defer release()
		firstDone <- siteHelperContext(ctx, a.Config, "seal", "helper-boundary")
	}()

	started := marker + ".started"
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first helper did not reach its critical section")
		}
		time.Sleep(5 * time.Millisecond)
	}

	secondDone := make(chan error, 1)
	go func() {
		ctx, release, err := b.acquireSiteMutationLockContext(context.Background(), "site:helper-boundary")
		if err != nil {
			secondDone <- err
			return
		}
		defer release()
		secondDone <- siteHelperContext(ctx, b.Config, "seal", "helper-boundary")
	}()
	if err := <-firstDone; err != nil {
		t.Fatalf("first helper mutation failed: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second helper mutation failed: %v", err)
	}
	if _, err := os.Stat(overlap); err == nil {
		t.Fatal("helper critical sections overlapped despite independent durable locks")
	}
}
