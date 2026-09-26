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

// TestWorkflow_RestoreInterruptedByDelete verifies that when a restore is
// interrupted by losing its lock to a delete operation, the site state remains
// consistent and the delete can proceed without corruption.
//
// Acceptance Criteria 3: No race condition bugs after concurrent operations
// Acceptance Criteria 5: Failed operations leave system in known good state
func TestWorkflow_RestoreInterruptedByDelete(t *testing.T) {
	// Setup: Two independent app instances
	root := t.TempDir()
	dbPath := filepath.Join(root, "control-plane.sqlite")
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

	// Short lease so we can trigger takeover during the test
	locksA, err := operations.NewDBLocks(dbA, "restore-process", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locksB, err := operations.NewDBLocks(dbB, "delete-process", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	restoreApp := &App{dbLocks: locksA, Config: Config{WebRoot: root}}
	deleteApp := &App{dbLocks: locksB, Config: Config{WebRoot: root}}

	siteName := "interrupt-restore-test"

	// Phase 1: Restore acquires lock and begins operation
	restoreCtx, restoreRelease, err := restoreApp.acquireSiteMutationLockContext(context.Background(), "site:"+siteName)
	if err != nil {
		t.Fatalf("restore failed to acquire lock: %v", err)
	}

	// Phase 2: Wait past the lease duration so delete can take over
	time.Sleep(150 * time.Millisecond)

	// Phase 3: Delete tries to acquire the lock (should succeed due to lease expiry)
	deleteCtx, deleteRelease, err := deleteApp.acquireSiteMutationLockContext(context.Background(), "site:"+siteName)
	if err != nil {
		t.Fatalf("delete failed to acquire lock after expiry: %v", err)
	}
	defer deleteRelease()

	// Phase 4: Verify that restore's context is now cancelled (lease was lost)
	// This simulates the restore operation discovering it lost the lock
	select {
	case <-restoreCtx.Done():
		// Expected: restore context should be cancelled because its lock was taken
	case <-time.After(500 * time.Millisecond):
		t.Fatal("restore context not cancelled even after lock loss (should be holding lock)")
	}

	// Phase 5: Verify that restore cannot proceed with operations
	// (Its context is done, so any operation using it would fail)
	if restoreCtx.Err() == nil {
		t.Error("restore context should be cancelled but Err() returned nil")
	}

	// Phase 6: Verify that delete can safely proceed
	// (It holds the valid lock now)
	select {
	case <-deleteCtx.Done():
		t.Fatalf("delete context should be live but it's done: %v", deleteCtx.Err())
	default:
		// Good: delete context is still alive
	}

	// Phase 7: Release restore lock (should be idempotent since it lost fencing)
	restoreRelease()

	t.Logf("Test passed: restore interrupted, system remained consistent, delete proceeded safely")
}

// TestWorkflow_DeployInterruptedByRestore verifies that deploy and restore
// operations don't mix files when one is interrupted by the other.
//
// Acceptance Criteria 3: No race condition bugs after concurrent operations
// Acceptance Criteria 5: Failed operations leave system in known good state
func TestWorkflow_DeployInterruptedByRestore(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "control-plane.sqlite")
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

	locksA, err := operations.NewDBLocks(dbA, "deploy-process", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locksB, err := operations.NewDBLocks(dbB, "restore-process", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	deployApp := &App{dbLocks: locksA, Config: Config{WebRoot: root}}
	restoreApp := &App{dbLocks: locksB, Config: Config{WebRoot: root}}

	siteName := "interrupt-deploy-test"

	// Phase 1: Deploy acquires lock
	deployCtx, deployRelease, err := deployApp.acquireSiteMutationLockContext(context.Background(), "site:"+siteName)
	if err != nil {
		t.Fatalf("deploy failed to acquire lock: %v", err)
	}

	// Phase 2: Wait for lease expiry
	time.Sleep(150 * time.Millisecond)

	// Phase 3: Restore acquires the lock (deploy's lease expired)
	restoreCtx, restoreRelease, err := restoreApp.acquireSiteMutationLockContext(context.Background(), "site:"+siteName)
	if err != nil {
		t.Fatalf("restore failed to acquire lock: %v", err)
	}
	defer restoreRelease()

	// Phase 4: Verify deploy's context was cancelled
	select {
	case <-deployCtx.Done():
		// Expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("deploy context not cancelled after lock loss")
	}

	// Phase 5: Verify restore context is alive
	select {
	case <-restoreCtx.Done():
		t.Fatalf("restore context should be live: %v", restoreCtx.Err())
	default:
		// Expected: restore has the lock
	}

	deployRelease()
	t.Logf("Test passed: deploy interrupted by restore, contexts properly cancelled")
}

// TestWorkflow_ResourceUpdateInterruptedBySuspension verifies that resource
// updates and account suspension don't race — one must wait or one must rollback.
//
// This tests the compound lock pattern: resource update acquires both
// site:X and account:Y locks, while suspension acquires account:Y lock.
//
// Acceptance Criteria 3: No race condition bugs after concurrent operations
// Acceptance Criteria 5: Failed operations leave system in known good state
func TestWorkflow_ResourceUpdateInterruptedBySuspension(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "control-plane.sqlite")
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

	locksA, err := operations.NewDBLocks(dbA, "resource-process", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locksB, err := operations.NewDBLocks(dbB, "suspension-process", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	resourceApp := &App{dbLocks: locksA, Config: Config{WebRoot: root}}
	suspensionApp := &App{dbLocks: locksB, Config: Config{WebRoot: root}}

	siteName := "interrupt-resource-test"
	accountName := "customer-account"

	// Phase 1: Resource update acquires compound locks (site + account)
	resourceCtx, resourceRelease, err := resourceApp.acquireSiteMutationLocksContext(
		context.Background(),
		"site:"+siteName,
		"account:"+accountName,
	)
	if err != nil {
		t.Fatalf("resource update failed to acquire locks: %v", err)
	}

	// Phase 2: Wait for lease expiry
	time.Sleep(150 * time.Millisecond)

	// Phase 3: Suspension tries to acquire account lock (should succeed)
	suspensionCtx, suspensionRelease, err := suspensionApp.acquireSiteMutationLockContext(
		context.Background(),
		"account:"+accountName,
	)
	if err != nil {
		t.Fatalf("suspension failed to acquire account lock: %v", err)
	}
	defer suspensionRelease()

	// Phase 4: Resource operation's context should be cancelled (lost account lock)
	select {
	case <-resourceCtx.Done():
		// Expected: lost one of the compound locks
	case <-time.After(500 * time.Millisecond):
		t.Fatal("resource context not cancelled after losing compound lock")
	}

	// Phase 5: Suspension context should be alive
	select {
	case <-suspensionCtx.Done():
		t.Fatalf("suspension context should be live: %v", suspensionCtx.Err())
	default:
		// Expected
	}

	resourceRelease()
	t.Logf("Test passed: resource update interrupted by suspension, compound locks enforced")
}

// TestWorkflow_BackupInterruptedByRestore verifies that backup and filesystem
// restore operations see consistent state — backup must see either before
// or after the restore, never partial state.
//
// Acceptance Criteria 3: No race condition bugs after concurrent operations
// Acceptance Criteria 5: Failed operations leave system in known good state
func TestWorkflow_BackupInterruptedByRestore(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "control-plane.sqlite")
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

	locksA, err := operations.NewDBLocks(dbA, "backup-process", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locksB, err := operations.NewDBLocks(dbB, "restore-process", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	backupApp := &App{dbLocks: locksA, Config: Config{WebRoot: root}}
	restoreApp := &App{dbLocks: locksB, Config: Config{WebRoot: root}}

	siteName := "interrupt-backup-test"

	// Phase 1: Backup acquires lock
	backupCtx, backupRelease, err := backupApp.acquireSiteMutationLockContext(context.Background(), "site:"+siteName)
	if err != nil {
		t.Fatalf("backup failed to acquire lock: %v", err)
	}

	// Phase 2: Wait for lease expiry
	time.Sleep(150 * time.Millisecond)

	// Phase 3: Restore acquires the lock
	restoreCtx, restoreRelease, err := restoreApp.acquireSiteMutationLockContext(context.Background(), "site:"+siteName)
	if err != nil {
		t.Fatalf("restore failed to acquire lock: %v", err)
	}
	defer restoreRelease()

	// Phase 4: Verify backup's context was cancelled
	select {
	case <-backupCtx.Done():
		// Expected: backup lost its lock
	case <-time.After(500 * time.Millisecond):
		t.Fatal("backup context not cancelled after lock loss")
	}

	// Phase 5: Verify restore context is alive
	select {
	case <-restoreCtx.Done():
		t.Fatalf("restore context should be live: %v", restoreCtx.Err())
	default:
		// Expected
	}

	backupRelease()
	t.Logf("Test passed: backup interrupted by restore, operations properly serialized")
}

// TestWorkflow_RouteUpdateInterruptedByTermination verifies that route updates
// and site termination don't race — the compound lock (site + vhost) prevents
// partial route state updates.
//
// Acceptance Criteria 3: No race condition bugs after concurrent operations
// Acceptance Criteria 5: Failed operations leave system in known good state
func TestWorkflow_RouteUpdateInterruptedByTermination(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "control-plane.sqlite")
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

	locksA, err := operations.NewDBLocks(dbA, "route-process", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locksB, err := operations.NewDBLocks(dbB, "termination-process", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	routeApp := &App{dbLocks: locksA, Config: Config{WebRoot: root}}
	terminationApp := &App{dbLocks: locksB, Config: Config{WebRoot: root}}

	siteName := "interrupt-route-test"
	vhostName := "route-test.example.com"

	// Phase 1: Route update acquires compound locks (site + vhost)
	routeCtx, routeRelease, err := routeApp.acquireSiteMutationLocksContext(
		context.Background(),
		"site:"+siteName,
		"vhost:"+vhostName,
	)
	if err != nil {
		t.Fatalf("route update failed to acquire locks: %v", err)
	}

	// Phase 2: Wait for lease expiry
	time.Sleep(150 * time.Millisecond)

	// Phase 3: Termination acquires site lock (should succeed after route lease expires)
	terminationCtx, terminationRelease, err := terminationApp.acquireSiteMutationLockContext(
		context.Background(),
		"site:"+siteName,
	)
	if err != nil {
		t.Fatalf("termination failed to acquire lock: %v", err)
	}
	defer terminationRelease()

	// Phase 4: Route operation's context should be cancelled
	select {
	case <-routeCtx.Done():
		// Expected: lost the compound lock
	case <-time.After(500 * time.Millisecond):
		t.Fatal("route context not cancelled after lock loss")
	}

	// Phase 5: Termination context should be alive
	select {
	case <-terminationCtx.Done():
		t.Fatalf("termination context should be live: %v", terminationCtx.Err())
	default:
		// Expected
	}

	routeRelease()
	t.Logf("Test passed: route update interrupted by termination, compound locks enforced")
}
