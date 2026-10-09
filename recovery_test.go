package stepanel

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDatabaseRecoveryJournalSurvivesProcessKill(t *testing.T) {
	if os.Getenv("STEPANEL_DATABASE_RECOVERY_CHILD") == "1" {
		root := os.Getenv("STEPANEL_DATABASE_RECOVERY_ROOT")
		recovery := filepath.Join(root, "recovery")
		home := filepath.Join(root, "site", "public")
		writeTestFile(t, filepath.Join(home, "index.html"), "old")
		txn, err := BeginSiteTransaction(recovery, home, "test.restore", AuthorizedSite{site: "site"})
		if err != nil {
			t.Fatal(err)
		}
		if err := txn.TrackDatabase(ManagedDatabase{Name: "site_database", Kind: "cpmove"}); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(home, "index.html"), "partial")
		if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		return
	}

	root := t.TempDir()
	logPath := filepath.Join(root, "dbctl.log")
	helper := filepath.Join(root, "dbctl")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_DBCTL_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_DBCTL_LOG", logPath)
	child := exec.Command(os.Args[0], "-test.run=TestDatabaseRecoveryJournalSurvivesProcessKill", "-test.v")
	child.Env = append(os.Environ(),
		"STEPANEL_DATABASE_RECOVERY_CHILD=1",
		"STEPANEL_DATABASE_RECOVERY_ROOT="+root,
	)
	err := child.Run()
	if err == nil {
		t.Fatal("child process unexpectedly survived database recovery SIGKILL")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("child exit = %v, want SIGKILL", err)
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child exit = %v, want SIGKILL", err)
	}

	recovery := filepath.Join(root, "recovery")
	recovered, err := RecoverTransactionDatabases(Config{DBCtl: helper}, recovery, nil)
	if err != nil || len(recovered) != 1 {
		t.Fatalf("database recoveries = %#v, err=%v; want one recovery", recovered, err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil || string(logData) != "drop site_database\n" {
		t.Fatalf("database cleanup log = %q, err=%v", logData, err)
	}
	if _, err := RecoverSiteTransactions(recovery); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, filepath.Join(root, "site", "public", "index.html"), "old")
}

func TestRecoverSiteTransactionAfterProcessDeath(t *testing.T) {
	root := t.TempDir()
	recovery := filepath.Join(root, ".stepanel-recovery")
	home := filepath.Join(root, "site", "public")
	writeTestFile(t, filepath.Join(home, "index.html"), "old")
	if _, err := BeginSiteTransaction(recovery, home, "test.process-death", AuthorizedSite{site: "site"}); err != nil {
		t.Fatal(err)
	}
	// Leave the journal uncommitted, matching the state a process death or
	// power loss would leave behind.
	writeTestFile(t, filepath.Join(home, "index.html"), "partial")
	if _, err := RecoverSiteTransactions(recovery); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, filepath.Join(home, "index.html"), "old")
}

func TestRecoverSiteTransactionWithoutExistingSiteRemovesInterruptedSite(t *testing.T) {
	root := t.TempDir()
	recovery := filepath.Join(root, ".stepanel-recovery")
	home := filepath.Join(root, "site", "public")
	txn, err := BeginSiteTransaction(recovery, home, "cpmove.restore", AuthorizedSite{site: "site"})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "index.html"), "interrupted")
	recovered, err := RecoverSiteTransactions(recovery)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0] != txn.ID {
		t.Fatalf("recovered = %#v, want %q", recovered, txn.ID)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted new site still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(txn.dir, "failed-site", "index.html")); err != nil {
		t.Fatalf("failed site was not preserved: %v", err)
	}
}

func TestRecoverSiteTransactionsQuarantinesPathOutsideConfiguredRoot(t *testing.T) {
	root := t.TempDir()
	webRoot := filepath.Join(root, "web")
	recovery := filepath.Join(root, "recovery")
	home := filepath.Join(webRoot, "sites", "site", "public")
	writeTestFile(t, filepath.Join(home, "index.html"), "old")
	txn, err := BeginSiteTransaction(recovery, home, "test.restore", AuthorizedSite{site: "site"})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside", "public")
	writeTestFile(t, filepath.Join(outside, "important.txt"), "do not touch")
	txn.Home = outside
	if err := txn.persist(); err != nil {
		t.Fatal(err)
	}

	recovered, err := RecoverSiteTransactions(recovery, webRoot, filepath.Join(root, "mail"))
	if err == nil || len(recovered) != 0 || !strings.Contains(err.Error(), "outside the configured web root") {
		t.Fatalf("unsafe recovery result = %#v, error = %v", recovered, err)
	}
	assertTestFile(t, filepath.Join(outside, "important.txt"), "do not touch")
	entries, readErr := os.ReadDir(filepath.Join(recovery, "quarantine"))
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("quarantine entries=%d err=%v", len(entries), readErr)
	}
}

func TestRecoverTransactionDatabasesCleansJournalBeforeSiteRollback(t *testing.T) {
	root := t.TempDir()
	recovery := filepath.Join(root, ".stepanel-recovery")
	home := filepath.Join(root, "site", "public")
	writeTestFile(t, filepath.Join(home, "index.html"), "old")
	txn, err := BeginSiteTransaction(recovery, home, "test.restore", AuthorizedSite{site: "site"})
	if err != nil {
		t.Fatal(err)
	}
	if err := txn.TrackDatabase(ManagedDatabase{Name: "site_archive", Kind: "cpmove"}); err != nil {
		t.Fatal(err)
	}
	if err := txn.TrackDatabase(ManagedDatabase{Name: "site_wordpress", User: "site_wordpress", Kind: "wordpress"}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "index.html"), "partial")
	logPath := filepath.Join(root, "dbctl.log")
	helper := filepath.Join(root, "dbctl")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_DBCTL_LOG\"\n"
	if err := os.WriteFile(helper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_DBCTL_LOG", logPath)
	// The broker rejects unfenced database mutations, so recovery must hold
	// the site's lease (and release it) around the drops.
	var locked []string
	released := 0
	locker := func(ctx context.Context, key string) (context.Context, func(), error) {
		locked = append(locked, key)
		return ctx, func() { released++ }, nil
	}
	recovered, err := RecoverTransactionDatabases(Config{DBCtl: helper}, recovery, locker)
	if err != nil {
		t.Fatal(err)
	}
	if len(locked) != 1 || locked[0] != txn.Site || released != 1 {
		t.Fatalf("site leases = %q (released %d), want one lease on %q", locked, released, txn.Site)
	}
	if len(recovered) != 1 || recovered[0] != txn.ID {
		t.Fatalf("database recoveries = %#v, want %q", recovered, txn.ID)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "drop site_archive\ncleanup-wordpress site_wordpress site_wordpress\n"
	if string(data) != want {
		t.Fatalf("database cleanup calls = %q, want %q", data, want)
	}
	loaded, err := loadSiteTransaction(txn.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Databases) != 0 || loaded.State != "site-backed-up" {
		t.Fatalf("database journal was not cleared before site rollback: %#v", loaded)
	}
	if _, err := RecoverSiteTransactions(recovery); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, filepath.Join(home, "index.html"), "old")
}

func TestManagedDatabaseValidationRejectsUnsafeJournalEntry(t *testing.T) {
	root := t.TempDir()
	txn, err := BeginSiteTransaction(filepath.Join(root, "recovery"), filepath.Join(root, "site", "public"), "test.restore", AuthorizedSite{site: "site"})
	if err != nil {
		t.Fatal(err)
	}
	if err := txn.TrackDatabase(ManagedDatabase{Name: "../mysql", Kind: "cpmove"}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("unsafe database journal entry accepted: %v", err)
	}
}

func TestSiteTransactionRollbackRestoresPreviousSite(t *testing.T) {
	root := t.TempDir()
	recovery := filepath.Join(root, ".stepanel-recovery")
	home := filepath.Join(root, "site", "public")
	writeTestFile(t, filepath.Join(home, "index.html"), "old")
	txn, err := BeginSiteTransaction(recovery, home, "test.restore", AuthorizedSite{site: "site"})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "index.html"), "partial")
	if err := txn.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, filepath.Join(home, "index.html"), "old")
	assertTestFile(t, filepath.Join(txn.FailedSite, "index.html"), "partial")
	loaded, err := loadSiteTransaction(txn.dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != "rolled-back" {
		t.Fatalf("state = %q, want rolled-back", loaded.State)
	}
}

func TestRecoverSiteTransactionsRollsBackInterruptedSite(t *testing.T) {
	root := t.TempDir()
	recovery := filepath.Join(root, ".stepanel-recovery")
	home := filepath.Join(root, "site", "public")
	writeTestFile(t, filepath.Join(home, "index.html"), "old")
	txn, err := BeginSiteTransaction(recovery, home, "test.restore", AuthorizedSite{site: "site"})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "index.html"), "partial")
	recovered, err := RecoverSiteTransactions(recovery)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0] != txn.ID {
		t.Fatalf("recovered = %#v, want %q", recovered, txn.ID)
	}
	assertTestFile(t, filepath.Join(home, "index.html"), "old")
}

func TestQuarantinedRecoveryJournalIsReportedAsCorruption(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(broken, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "transaction.json"), []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	var reported []string
	previous := recoveryCorruptionObserver
	recoveryCorruptionObserver = func(dir string, cause error) { reported = append(reported, filepath.Base(dir)) }
	t.Cleanup(func() { recoveryCorruptionObserver = previous })
	// Recovery reports the malformed entry as an error after quarantining it.
	if _, err := RecoverSiteTransactions(root, t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("malformed journal was not reported")
	}
	if len(reported) != 1 || reported[0] != "broken" {
		t.Fatalf("reported %v, want the quarantined journal", reported)
	}
}

func TestRecoverSiteTransactionsQuarantinesMalformedEntryAndContinues(t *testing.T) {
	root := t.TempDir()
	recovery := filepath.Join(root, ".stepanel-recovery")
	if err := os.MkdirAll(filepath.Join(recovery, "bad"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recovery, "bad", "transaction.json"), []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "site", "public")
	writeTestFile(t, filepath.Join(home, "index.html"), "old")
	txn, err := BeginSiteTransaction(recovery, home, "test.restore", AuthorizedSite{site: "site"})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "index.html"), "partial")
	recovered, err := RecoverSiteTransactions(recovery)
	if err == nil || len(recovered) != 1 || recovered[0] != txn.ID {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	assertTestFile(t, filepath.Join(home, "index.html"), "old")
	entries, readErr := os.ReadDir(filepath.Join(recovery, "quarantine"))
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("quarantine entries=%d err=%v", len(entries), readErr)
	}
}

func TestSiteTransactionRollbackReconcilesRestoredBackup(t *testing.T) {
	root := t.TempDir()
	recovery := filepath.Join(root, ".stepanel-recovery")
	home := filepath.Join(root, "site", "public")
	writeTestFile(t, filepath.Join(home, "index.html"), "old")
	txn, err := BeginSiteTransaction(recovery, home, "test.restore", AuthorizedSite{site: "site"})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "index.html"), "partial")
	txn.State = "rolling-back"
	if err := txn.persist(); err != nil {
		t.Fatal(err)
	}
	failed := filepath.Join(txn.dir, "failed-site")
	if err := os.Rename(home, failed); err != nil {
		t.Fatal(err)
	}
	txn.FailedSite = failed
	if err := txn.persist(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(txn.Backup, home); err != nil {
		t.Fatal(err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, filepath.Join(home, "index.html"), "old")
	assertTestFile(t, filepath.Join(failed, "index.html"), "partial")
}

func TestCommittedSiteTransactionRetainsBackupUntilCleanup(t *testing.T) {
	root := t.TempDir()
	recovery := filepath.Join(root, ".stepanel-recovery")
	home := filepath.Join(root, "site", "public")
	writeTestFile(t, filepath.Join(home, "index.html"), "old")
	txn, err := BeginSiteTransaction(recovery, home, "test.restore", AuthorizedSite{site: "site"})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, "index.html"), "new")
	if err := txn.Commit(); err != nil {
		t.Fatal(err)
	}
	assertTestFile(t, filepath.Join(txn.Backup, "index.html"), "old")
	if err := CleanupSiteTransactions(recovery, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(txn.dir); err != nil {
		t.Fatalf("fresh recovery transaction was removed: %v", err)
	}
	if err := CleanupSiteTransactions(recovery, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(txn.dir); !os.IsNotExist(err) {
		t.Fatalf("expired transaction still exists: %v", err)
	}
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0640); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
