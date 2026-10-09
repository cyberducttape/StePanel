package stepanel

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/operations"
	"github.com/cyberducttape/StePanel/internal/rootbroker"
	_ "modernc.org/sqlite"
)

func expireTestLease(t *testing.T, db *sql.DB, key string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE resource_locks SET lease_until = 0 WHERE resource_key = ?`, key); err != nil {
		t.Fatalf("expire test lease %s: %v", key, err)
	}
}

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
	expireTestLease(t, dbA, "site-loss")
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
	expireTestLease(t, dbA, "vhost:compound")
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

func TestSiteMutationLockLossTerminatesInFlightHelper(t *testing.T) {
	root := t.TempDir()
	scriptPath := filepath.Join(root, "long-helper.sh")
	started := filepath.Join(root, "started")
	completed := filepath.Join(root, "completed")
	script := "#!/bin/sh\nset -eu\n: > " + started + "\nsleep 10\n: > " + completed + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

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
	locksA, err := operations.NewDBLocks(dbA, "helper-owner", 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	locksB, err := operations.NewDBLocks(dbB, "helper-takeover", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	owner := &App{Config: Config{SiteCtl: scriptPath, WebRoot: root}, dbLocks: locksA}

	ctx, release, err := owner.acquireSiteMutationLockContext(context.Background(), "site:helper-loss")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan error, 1)
	go func() { done <- siteHelperContext(ctx, owner.Config, "seal", "helper-loss") }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("long-running helper did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	expireTestLease(t, dbA, "site:helper-loss")
	takeoverLease, err := locksB.TryAcquire("site:helper-loss")
	if err != nil {
		t.Fatal("takeover failed: ", err)
	}
	defer locksB.Release(takeoverLease)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("in-flight helper reported success after lease loss")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight helper was not terminated after lease loss")
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(completed); err == nil {
		t.Fatal("helper completed after its lease was lost")
	}
}

// Startup recovery seals and deletes sites through the broker, which rejects
// mutating requests without a fencing token. The recovery context must carry
// the site's lease token, and the lease must be released afterwards.
func TestRecoveredSiteLockCarriesFencingToken(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control-plane.sqlite")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	locks, err := operations.NewDBLocks(db, "panel-recovery", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{dbLocks: locks}
	ctx, unlock, err := lockRecoveredSite(app.acquireSiteMutationLockContext, "recovered-site")
	if err != nil {
		t.Fatal(err)
	}
	if tokens := operations.FencingTokens(ctx); len(tokens) != 1 {
		t.Fatalf("recovery context fencing tokens = %v, want the site lease", tokens)
	}
	unlock()
	if _, err := locks.TryAcquire("recovered-site"); err != nil {
		t.Fatalf("recovery did not release the site lease: %v", err)
	}
	if ctx, unlock, err := lockRecoveredSite(nil, "unlocked-site"); err != nil || len(operations.FencingTokens(ctx)) != 0 {
		t.Fatalf("nil locker = (%v, %v), want an unlocked context", operations.FencingTokens(ctx), err)
	} else {
		unlock()
	}
}

// A live owner renews every third of the lease, so its lease never has less
// than about two thirds left. The broker's watchdog must never mistake that
// for a lost lease, with room for a missed renewal tick.
func TestLeaseTimeLeavesWatchdogHeadroom(t *testing.T) {
	healthyMinimum := controlPlaneLeaseTime * 2 / 3
	if need := 2 * (rootbroker.LeaseWatchMargin + rootbroker.LeaseWatchInterval); healthyMinimum < need {
		t.Fatalf("lease time %s leaves %s for a live owner; the fencing watchdog needs at least %s", controlPlaneLeaseTime, healthyMinimum, need)
	}
}

// The startup database reconcile mutates through the root broker, which
// rejects requests without a fencing token; it must run under its own lease.
func TestStartupDatabaseReconcileHoldsLease(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "dbctl.log")
	helper := filepath.Join(root, "dbctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \""+logPath+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "control-plane.sqlite")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	locks, err := operations.NewDBLocks(db, "panel-startup", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{dbLocks: locks}
	var tokens []operations.FencingToken
	locker := func(ctx context.Context, key string) (context.Context, func(), error) {
		leased, release, err := app.acquireSiteMutationLockContext(ctx, key)
		if err == nil {
			tokens = operations.FencingTokens(leased)
		}
		return leased, release, err
	}
	if err := reconcileDatabaseOperations(Config{DBCtl: helper}, locker); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].ResourceKey != databaseReconcileLease {
		t.Fatalf("reconcile fencing tokens = %#v, want one %q lease", tokens, databaseReconcileLease)
	}
	if data, err := os.ReadFile(logPath); err != nil || string(data) != "reconcile\n" {
		t.Fatalf("database helper calls = %q, %v", data, err)
	}
	if _, err := locks.TryAcquire(databaseReconcileLease); err != nil {
		t.Fatalf("reconcile did not release its lease: %v", err)
	}
}
