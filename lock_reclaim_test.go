package stepanel

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/operations"
)

func TestLockOwnerDead(t *testing.T) {
	identities := map[int]string{100: "boot:500"}
	lookup := func(pid int) (string, error) {
		if identity, ok := identities[pid]; ok {
			return identity, nil
		}
		if pid == 300 {
			return "", fmt.Errorf("permission denied")
		}
		return "", fmt.Errorf("%w: pid %d", errCapacityOwnerProcessMissing, pid)
	}
	for _, tc := range []struct {
		owner string
		dead  bool
	}{
		{"stepanel/100/boot:500/node", false},            // running
		{"stepanel/100/boot:499/node", true},             // PID reused
		{"stepanel/100/oldboot:500/node", true},          // earlier boot
		{"stepanel/200/boot:1/node", true},               // gone
		{"stepanel/300/boot:1/node", false},              // unreadable: keep
		{"stepanel/200/boot:1/other-node", false},        // another host
		{"stepanel/x/boot:1/node", false},                // malformed
		{"stepanel-node-200-1793951548000000000", true},  // legacy, gone
		{"stepanel-node-100-1793951548000000000", false}, // legacy, PID in use
		{"stepanel-other-200-1793951548000000000", false},
		{"installer-schema-check", false},
	} {
		if got := lockOwnerDead(tc.owner, "node", lookup); got != tc.dead {
			t.Errorf("lockOwnerDead(%q) = %v, want %v", tc.owner, got, tc.dead)
		}
	}
}

func TestReclaimDeadOwnerLocksReleasesOnlyExitedOwners(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/locks.db?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dead, err := operations.NewDBLocks(db, "stepanel/200/boot:1/node", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	live, err := operations.NewDBLocks(db, "stepanel/100/boot:500/node", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	deadLease, err := dead.TryAcquire("site-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := live.TryAcquire("site-b"); err != nil {
		t.Fatal(err)
	}
	lookup := func(pid int) (string, error) {
		if pid == 100 {
			return "boot:500", nil
		}
		return "", fmt.Errorf("%w: pid %d", errCapacityOwnerProcessMissing, pid)
	}
	released, err := reclaimDeadOwnerLocks(db, "node", time.Now(), lookup)
	if err != nil || released != 1 {
		t.Fatalf("reclaim = %d, %v; want 1 released", released, err)
	}
	next, err := live.TryAcquire("site-a")
	if err != nil {
		t.Fatalf("site-a should be free after its owner exited: %v", err)
	}
	if next.Generation <= deadLease.Generation {
		t.Fatalf("generation did not advance past the exited owner's lease: %d <= %d", next.Generation, deadLease.Generation)
	}
	if err := operations.VerifyFencingToken(db, deadLease.Token()); err == nil {
		t.Fatal("the exited owner's fencing token is still accepted")
	}
	if _, err := dead.TryAcquire("site-b"); err == nil {
		t.Fatal("a running owner's lease was released")
	}
}
