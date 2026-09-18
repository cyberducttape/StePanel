package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	recovered, err := RecoverTransactionDatabases(Config{DBCtl: helper}, recovery)
	if err != nil {
		t.Fatal(err)
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
