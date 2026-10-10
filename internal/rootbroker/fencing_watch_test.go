package rootbroker

import (
	"context"
	"database/sql"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/operations"
	_ "modernc.org/sqlite"
)

// fencedWordPressBroker returns a broker whose appctl runs for runFor and
// records completion, and a lease table holding site:demo for owner-a.
func fencedWordPressBroker(t *testing.T, runFor string) (*Broker, *sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "fencing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE resource_locks (resource_key TEXT PRIMARY KEY, owner_id TEXT NOT NULL, lease_until INTEGER NOT NULL, generation INTEGER NOT NULL, acquired_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO resource_locks VALUES ('site:demo', 'owner-a', ?, 1, ?)`, time.Now().Add(time.Minute).UnixNano(), time.Now().UnixNano()); err != nil {
		t.Fatal(err)
	}
	completed := filepath.Join(root, "completed")
	helper := filepath.Join(root, "appctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nsleep "+runFor+"\n: > "+completed+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	broker, err := newBrokerWithFencingDB(root, t.TempDir(), log.New(io.Discard, "", 0), &fakeHost{}, db)
	if err != nil {
		t.Fatal(err)
	}
	broker.appctlPath = helper
	broker.leaseWatchInterval = 20 * time.Millisecond
	broker.leaseWatchMargin = 0
	return broker, db, completed
}

var demoLease = []operations.FencingToken{{ResourceKey: "site:demo", OwnerID: "owner-a", Generation: 1}}

// A privileged operation must not outlive the lease that admitted it: when
// another owner takes the resource mid-operation, the helper is killed.
func TestBrokerCancelsOperationWhenLeaseIsSuperseded(t *testing.T) {
	broker, db, completed := fencedWordPressBroker(t, "5")
	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = db.Exec(`UPDATE resource_locks SET owner_id = 'owner-b', generation = 2 WHERE resource_key = 'site:demo'`)
	}()
	started := time.Now()
	response, err := broker.Execute(context.Background(), &Request{RequestType: "wordpress", Fencing: demoLease, WordPress: &WordPressRequest{Action: "core-update", Site: "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || !strings.Contains(response.Error, "fencing lease was lost") {
		t.Fatalf("response = %#v, want lease-loss cancellation", response)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("operation ran %s after its lease was superseded", elapsed)
	}
	if _, err := os.Stat(completed); err == nil {
		t.Fatal("helper completed after its lease was superseded")
	}
}

// An owner that stops renewing loses the lease at expiry; the operation is
// cancelled before another owner could acquire it when a margin is set.
func TestBrokerCancelsOperationBeforeLeaseExpires(t *testing.T) {
	broker, db, completed := fencedWordPressBroker(t, "5")
	if _, err := db.Exec(`UPDATE resource_locks SET lease_until = ?`, time.Now().Add(400*time.Millisecond).UnixNano()); err != nil {
		t.Fatal(err)
	}
	broker.leaseWatchMargin = 300 * time.Millisecond
	response, err := broker.Execute(context.Background(), &Request{RequestType: "wordpress", Fencing: demoLease, WordPress: &WordPressRequest{Action: "core-update", Site: "demo"}})
	if err != nil || response.OK || !strings.Contains(response.Error, "fencing lease was lost") {
		t.Fatalf("response = %#v, %v; want cancellation inside the safety margin", response, err)
	}
	var leaseUntil int64
	if err := db.QueryRow(`SELECT lease_until FROM resource_locks`).Scan(&leaseUntil); err != nil {
		t.Fatal(err)
	}
	if time.Now().UnixNano() >= leaseUntil {
		t.Fatal("operation was only cancelled after the lease had already expired")
	}
	if _, err := os.Stat(completed); err == nil {
		t.Fatal("helper completed although its owner stopped renewing")
	}
}

// A lease that stays valid never interrupts the operation.
func TestBrokerKeepsOperationWithLiveLease(t *testing.T) {
	broker, _, completed := fencedWordPressBroker(t, "0.3")
	response, err := broker.Execute(context.Background(), &Request{RequestType: "wordpress", Fencing: demoLease, WordPress: &WordPressRequest{Action: "core-update", Site: "demo"}})
	if err != nil || !response.OK {
		t.Fatalf("response = %#v, %v", response, err)
	}
	if _, err := os.Stat(completed); err != nil {
		t.Fatal("helper did not complete under a live lease")
	}
}

func TestBrokerCancelsOperationWhenFencingDatabaseIsUnavailable(t *testing.T) {
	broker, db, completed := fencedWordPressBroker(t, "5")
	broker.fencingDBErrorGrace = 100 * time.Millisecond
	go func() {
		time.Sleep(60 * time.Millisecond)
		_ = db.Close()
	}()
	response, err := broker.Execute(context.Background(), &Request{RequestType: "wordpress", Fencing: demoLease, WordPress: &WordPressRequest{Action: "core-update", Site: "demo"}})
	if err != nil || response.OK || !strings.Contains(response.Error, "fencing database unavailable") {
		t.Fatalf("response = %#v, %v; want fail-closed fencing cancellation", response, err)
	}
	if _, err := os.Stat(completed); err == nil {
		t.Fatal("helper completed while the fencing database was unavailable")
	}
}

// Read-only database actions back administrator views and hold no lease;
// every mutating database action must still be fenced.
func TestDatabaseFencingDistinguishesReads(t *testing.T) {
	for action, fenced := range map[string]bool{
		"inventory": false, "dump": false, "diagnostics": false, "sessions": false, "settings": false,
		"terminate": true, "reconcile": true, "drop": true, "drop-managed": true, "provision": true, "restore": true, "cleanup-wordpress": true,
	} {
		if got := requestRequiresFencing(&Request{RequestType: "db", DB: &DBRequest{Action: action}}); got != fenced {
			t.Errorf("db %s requires fencing = %v, want %v", action, got, fenced)
		}
	}
}
