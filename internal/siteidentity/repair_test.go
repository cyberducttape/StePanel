package siteidentity

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

type repairHostStub struct {
	preflightErr error
	commitErr    error
	commits      int
	reconcile    map[string]int
	verify       map[string]int
	verifyErr    map[string]error
}

func (h *repairHostStub) Preflight(context.Context, []RepairAssignment) error { return h.preflightErr }
func (h *repairHostStub) BeforeCommit(context.Context, RepairJournal) error {
	h.commits++
	return h.commitErr
}
func (h *repairHostStub) Reconcile(_ context.Context, assignment RepairAssignment) error {
	h.reconcile[assignment.Site]++
	return nil
}
func (h *repairHostStub) Verify(_ context.Context, assignment RepairAssignment) error {
	h.verify[assignment.Site]++
	if err := h.verifyErr[assignment.Site]; err != nil {
		delete(h.verifyErr, assignment.Site)
		return err
	}
	return nil
}
func (*repairHostStub) Finalize(context.Context, []RepairAssignment) error { return nil }

func collisionFixture(t *testing.T) (*sql.DB, string, []string) {
	t.Helper()
	db := testIdentityDB(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sites"), 0o750); err != nil {
		t.Fatal(err)
	}
	sites := []string{"shared-site-prefix00018346", "shared-site-prefix0001d455"}
	for _, site := range sites {
		if err := os.Mkdir(filepath.Join(root, "sites", site), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO site_identities(site, username, created_at) VALUES (?, ?, 1)`, sites[0], UnixUser(sites[0])); err != nil {
		t.Fatal(err)
	}
	return db, root, sites
}

func newRepairHostStub() *repairHostStub {
	return &repairHostStub{reconcile: map[string]int{}, verify: map[string]int{}, verifyErr: map[string]error{}}
}

func repairJournalPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private-recovery")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "repair.json")
}

func TestExecuteCollisionRepairResumesAfterHostVerificationInterruption(t *testing.T) {
	db, root, sites := collisionFixture(t)
	journal := repairJournalPath(t)
	host := newRepairHostStub()
	host.verifyErr[sites[0]] = errors.New("injected interruption after host mutation")
	first, err := ExecuteCollisionRepair(context.Background(), db, root, journal, host)
	if err == nil || first.State != "mappings-committed" {
		t.Fatalf("interrupted repair = state %q, err %v; want committed mappings and resumable error", first.State, err)
	}
	if first.Assignments[0].Completed || first.Assignments[1].Completed {
		t.Fatalf("checkpoint after interrupted verification = %#v", first.Assignments)
	}

	completed, err := ExecuteCollisionRepair(context.Background(), db, root, journal, host)
	if err != nil {
		t.Fatalf("resume collision repair: %v", err)
	}
	if completed.State != "completed" || !completed.Assignments[0].Completed || !completed.Assignments[1].Completed {
		t.Fatalf("resumed journal = %#v", completed)
	}
	if host.reconcile[sites[0]] != 2 || host.reconcile[sites[1]] != 1 {
		t.Fatalf("reconcile attempts = %v; interrupted host action must be safely repeated", host.reconcile)
	}
	if err := MigrateExisting(db, root); err != nil {
		t.Fatalf("startup migration after repair: %v", err)
	}
}

func TestExecuteCollisionRepairFailsBeforeMappingWhenPreflightFails(t *testing.T) {
	db, root, sites := collisionFixture(t)
	journal := repairJournalPath(t)
	host := newRepairHostStub()
	host.preflightErr = errors.New("panel service is active")
	if _, err := ExecuteCollisionRepair(context.Background(), db, root, journal, host); err == nil {
		t.Fatal("active services passed repair preflight")
	}
	var username string
	if err := db.QueryRow(`SELECT username FROM site_identities WHERE site = ?`, sites[0]).Scan(&username); err != nil {
		t.Fatal(err)
	}
	if username != UnixUser(sites[0]) {
		t.Fatalf("preflight failure changed identity to %q", username)
	}
	if host.reconcile[sites[0]] != 0 || host.commits != 0 {
		t.Fatalf("host mutation ran before preflight: reconcile %v, commits %d", host.reconcile, host.commits)
	}
	if _, err := os.Lstat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused preflight persisted a stale plan: %v", err)
	}
}

func TestExecuteCollisionRepairLeavesMappingsWhenPreCommitStepFails(t *testing.T) {
	db, root, sites := collisionFixture(t)
	journal := repairJournalPath(t)
	host := newRepairHostStub()
	host.commitErr = errors.New("snapshot failed")
	if _, err := ExecuteCollisionRepair(context.Background(), db, root, journal, host); err == nil {
		t.Fatal("failed pre-commit snapshot did not stop the repair")
	}
	var username string
	if err := db.QueryRow(`SELECT username FROM site_identities WHERE site = ?`, sites[0]).Scan(&username); err != nil {
		t.Fatal(err)
	}
	if username != UnixUser(sites[0]) {
		t.Fatalf("pre-commit failure changed identity to %q", username)
	}
	host.commitErr = nil
	completed, err := ExecuteCollisionRepair(context.Background(), db, root, journal, host)
	if err != nil || completed.State != "completed" {
		t.Fatalf("resume after pre-commit failure = %q, %v", completed.State, err)
	}
}

func TestExecuteCollisionRepairResumesWhenCommitOutlivedJournalUpdate(t *testing.T) {
	db, root, sites := collisionFixture(t)
	journalPath := repairJournalPath(t)
	plan, err := PlanCollisionRepair(db, root)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the mapping transaction committed but before
	// the journal recorded it: the journal still says planned.
	if err := writeRepairJournal(journalPath, plan); err != nil {
		t.Fatal(err)
	}
	if err := applyRepairMappings(context.Background(), db, plan.Assignments); err != nil {
		t.Fatal(err)
	}
	host := newRepairHostStub()
	completed, err := ExecuteCollisionRepair(context.Background(), db, root, journalPath, host)
	if err != nil || completed.State != "completed" {
		t.Fatalf("resume after committed mappings = %q, %v", completed.State, err)
	}
	for _, site := range sites {
		if host.reconcile[site] != 1 || host.verify[site] != 1 {
			t.Fatalf("site %q was not reconciled after resume: reconcile %v verify %v", site, host.reconcile, host.verify)
		}
	}
}

func TestExecuteCollisionRepairCompletesPersistedOwnerConflict(t *testing.T) {
	db := testIdentityDB(t)
	root := t.TempDir()
	site := "shared-site-prefix00018346"
	if err := os.MkdirAll(filepath.Join(root, "sites", site), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_identities(site, username, created_at) VALUES (?, ?, 1)`, "deleted-owner", UnixUser(site)); err != nil {
		t.Fatal(err)
	}
	if err := MigrateExisting(db, root); err == nil {
		t.Fatal("persisted owner conflict did not block startup before repair")
	}
	completed, err := ExecuteCollisionRepair(context.Background(), db, root, repairJournalPath(t), newRepairHostStub())
	if err != nil || completed.State != "completed" {
		t.Fatalf("persisted owner repair = %q, %v", completed.State, err)
	}
	if err := MigrateExisting(db, root); err != nil {
		t.Fatalf("startup migration after persisted owner repair: %v", err)
	}
	var owner string
	if err := db.QueryRow(`SELECT site FROM site_identities WHERE username = ?`, UnixUser(site)).Scan(&owner); err != nil || owner != "deleted-owner" {
		t.Fatalf("repair released the tombstoned legacy name: owner %q, %v", owner, err)
	}
}

func TestPlanCollisionRepairIsIdempotentAfterAllMappingsAreReassigned(t *testing.T) {
	db, root, sites := collisionFixture(t)
	if _, err := ExecuteCollisionRepair(context.Background(), db, root, repairJournalPath(t), newRepairHostStub()); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanCollisionRepair(db, root); err == nil {
		t.Fatal("completed legacy collision still produced a repair plan")
	}
	for _, site := range sites {
		var username string
		if err := db.QueryRow(`SELECT username FROM site_identities WHERE site = ?`, site).Scan(&username); err != nil {
			t.Fatal(err)
		}
		if username == UnixUser(site) {
			t.Fatalf("site %q still uses the legacy identity", site)
		}
	}
}

func TestPlanCollisionRepairFindsPersistedOwnerConflictForOneLiveSite(t *testing.T) {
	db := testIdentityDB(t)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sites"), 0o750); err != nil {
		t.Fatal(err)
	}
	site := "shared-site-prefix00018346"
	if err := os.Mkdir(filepath.Join(root, "sites", site), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO site_identities(site, username, created_at) VALUES (?, ?, 1)`, "deleted-owner", UnixUser(site)); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanCollisionRepair(db, root)
	if err != nil {
		t.Fatalf("plan persisted owner conflict: %v", err)
	}
	if len(plan.Assignments) != 1 || plan.Assignments[0].Site != site || plan.Assignments[0].LegacyUser != UnixUser(site) || plan.Assignments[0].PreviousUser != "" {
		t.Fatalf("persisted owner repair plan = %#v", plan.Assignments)
	}
}
