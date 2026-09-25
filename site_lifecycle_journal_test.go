package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestTerminationJournalRoundTrip covers the core journal contract:
// create → mark a step complete → close and reload → the step is still
// listed as complete. This is what makes a mid-sequence crash safe —
// the retry sees the persisted state, not a fresh journal.
func TestTerminationJournalRoundTrip(t *testing.T) {
	root := t.TempDir()
	j, err := loadOrCreateTerminationJournal(root, "site.terminate-abc123", "example", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if j.isComplete(stepBackupVerified) {
		t.Fatal("fresh journal should not have any completed steps")
	}
	j.setBackupPath("/var/backups/example-2026.tar.gz")
	if err := j.markComplete(stepBackupVerified); err != nil {
		t.Fatalf("markComplete: %v", err)
	}

	// Simulate a process restart: reload from disk.
	reloaded, err := loadOrCreateTerminationJournal(root, "site.terminate-abc123", "example", "admin")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !reloaded.isComplete(stepBackupVerified) {
		t.Fatal("reloaded journal lost the completed step")
	}
	if reloaded.BackupPath != "/var/backups/example-2026.tar.gz" {
		t.Errorf("BackupPath = %q, want the value we set before crash", reloaded.BackupPath)
	}
}

// TestTerminationJournalSurvivesProcessKill exercises the failure boundary
// with a real child process. The child starts the next destructive step and
// is killed before it can mark that step complete; the parent then performs
// the retry and persists the completion record. This is stronger evidence
// than reloading a journal in the same process because it also exercises the
// process-exit path and a fresh test binary.
func TestTerminationJournalSurvivesProcessKill(t *testing.T) {
	if os.Getenv("STEPANEL_TERMINATION_JOURNAL_CHILD") == "1" {
		root := os.Getenv("STEPANEL_TERMINATION_JOURNAL_ROOT")
		journal, err := loadOrCreateTerminationJournal(root, "site.terminate-kill", "example", "admin")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "destructive-step-started"), []byte("1\n"), 0600); err != nil {
			t.Fatal(err)
		}
		// The process dies after the destructive step starts but before the
		// journal can claim that it committed.
		if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		_ = journal
		return
	}

	root := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=TestTerminationJournalSurvivesProcessKill", "-test.v")
	child.Env = append(os.Environ(),
		"STEPANEL_TERMINATION_JOURNAL_CHILD=1",
		"STEPANEL_TERMINATION_JOURNAL_ROOT="+root,
	)
	err := child.Run()
	if err == nil {
		t.Fatal("child process unexpectedly survived the simulated crash")
	}
	exitErr, ok := err.(*exec.ExitError)
	status, statusOK := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !statusOK || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child exit = %v, want SIGKILL", err)
	}
	if _, err := os.Stat(filepath.Join(root, "destructive-step-started")); err != nil {
		t.Fatalf("child did not reach destructive-step boundary: %v", err)
	}

	recovered, err := loadOrCreateTerminationJournal(root, "site.terminate-kill", "example", "admin")
	if err != nil {
		t.Fatalf("reload after process kill: %v", err)
	}
	if recovered.isComplete(stepRoutesRemoved) {
		t.Fatal("step interrupted before commit was incorrectly marked complete")
	}
	if err := recovered.markComplete(stepRoutesRemoved); err != nil {
		t.Fatalf("persist recovered step: %v", err)
	}
	reloaded, err := loadOrCreateTerminationJournal(root, "site.terminate-kill", "example", "admin")
	if err != nil {
		t.Fatalf("reload recovered journal: %v", err)
	}
	if !reloaded.isComplete(stepRoutesRemoved) {
		t.Fatal("recovered step completion was not durable")
	}
}

// TestTerminationJournalCleanup — after every step is done, cleanup
// removes the file. That is what turns the audit log into the durable
// record of the termination. Cleanup on an already-removed journal is
// a no-op (idempotent so a retry after cleanup does not fail).
func TestTerminationJournalCleanup(t *testing.T) {
	root := t.TempDir()
	j, err := loadOrCreateTerminationJournal(root, "site.terminate-xyz789", "example", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.markComplete(stepBackupVerified); err != nil {
		t.Fatal(err)
	}
	path := j.path
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("journal not on disk after mark: %v", err)
	}
	if err := j.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("journal still present after cleanup: %v", err)
	}
	// Second cleanup is a no-op.
	if err := j.cleanup(); err != nil {
		t.Errorf("second cleanup should be idempotent, got %v", err)
	}
}

// TestTerminationJournalRejectsMismatchedRetry — a retry that reuses the
// jobID for a DIFFERENT site (should never happen, but defense in
// depth) is refused rather than silently continuing. If this ever
// triggers, the journal is intact and the operator can investigate
// without further destruction.
func TestTerminationJournalRejectsMismatchedRetry(t *testing.T) {
	root := t.TempDir()
	j, err := loadOrCreateTerminationJournal(root, "job-1", "site-a", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.markComplete(stepBackupVerified); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateTerminationJournal(root, "job-1", "site-b", "admin"); err == nil {
		t.Fatal("journal reload with different site should have failed")
	}
}

// TestTerminationJournalPathRefusesTraversal — the jobID is
// interpolated into a filesystem path. A malformed value that could
// escape RecoveryRoot must be refused before we ever touch disk.
func TestTerminationJournalPathRefusesTraversal(t *testing.T) {
	root := t.TempDir()
	for _, bad := range []string{
		"",
		"..",
		"../etc",
		"foo/bar",
		"job\x00null",
		".hidden",
		"contains spaces",
		strings.Repeat("x", 129),
	} {
		if _, err := terminationJournalPath(root, bad); err == nil {
			t.Errorf("terminationJournalPath(%q) should have failed", bad)
		}
	}
}

// TestTerminationJournalCorruptFile — a manually-corrupted journal
// (bad JSON) is refused with a clear error, not silently overwritten.
// That preserves whatever damage-inspection value the file has.
func TestTerminationJournalCorruptFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "site-termination-jobcorrupt.json")
	if err := os.WriteFile(path, []byte("not valid json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadOrCreateTerminationJournal(root, "jobcorrupt", "example", "admin")
	if err == nil {
		t.Fatal("corrupt journal should have failed to load")
	}
	if !errors.Is(err, err) {
		// Just ensure we got an error path; the specific type is not
		// contractual.
		t.Skip("error wrapping check not enforced here")
	}
}

// TestTerminationStepOrderIsComplete guards against a rename or removal
// of a step constant leaving the sequence with a gap that a caller
// would silently skip.
func TestTerminationStepOrderIsComplete(t *testing.T) {
	want := []string{
		stepBackupVerified,
		stepDatabasesRemoved,
		stepRoutesRemoved,
		stepProxiesRemoved,
		stepTasksRemoved,
		stepServicesRemoved,
		stepSiteStateRemoved,
		stepOwnershipRemoved,
	}
	if len(terminationStepOrder) != len(want) {
		t.Fatalf("terminationStepOrder length = %d, want %d", len(terminationStepOrder), len(want))
	}
	for i := range want {
		if terminationStepOrder[i] != want[i] {
			t.Errorf("terminationStepOrder[%d] = %q, want %q", i, terminationStepOrder[i], want[i])
		}
	}
}
