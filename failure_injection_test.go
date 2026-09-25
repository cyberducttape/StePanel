package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailureInjectionMatchesOperationAndPoint(t *testing.T) {
	t.Setenv("STEPANEL_FAIL_AT", "restore:commit")
	if err := failureInjection("restore", "commit"); err == nil {
		t.Fatal("expected injected failure")
	}
	if err := failureInjection("deploy", "commit"); err != nil {
		t.Fatalf("unrelated operation was injected: %v", err)
	}
	if err := failureInjection("restore", "init"); err != nil {
		t.Fatalf("unmatched point was injected: %v", err)
	}
}

func TestFailureInjectionAtTransactionCommitRollsBack(t *testing.T) {
	t.Setenv("STEPANEL_FAIL_AT", "restore:commit")
	root := t.TempDir()
	home := filepath.Join(root, "sites", "example", "public")
	if err := os.MkdirAll(home, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "index.html"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}

	txn, err := BeginSiteTransaction(filepath.Join(root, "recovery"), home, "wordpress.restore", AuthorizedSite{site: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "index.html"), []byte("new"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := txn.Commit(); err == nil || !strings.Contains(err.Error(), "failure injection") {
		t.Fatalf("Commit error = %v, want injected failure", err)
	}
	if err := txn.Rollback(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("rollback restored %q, want old", data)
	}
}

func TestFailureInjectionAtTransactionInitRestoresExistingSite(t *testing.T) {
	t.Setenv("STEPANEL_FAIL_AT", "restore:init")
	root := t.TempDir()
	home := filepath.Join(root, "sites", "example", "public")
	if err := os.MkdirAll(home, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "index.html"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := BeginSiteTransaction(filepath.Join(root, "recovery"), home, "archive.import", AuthorizedSite{site: "example"}); err == nil {
		t.Fatal("expected injected initialization failure")
	}
	data, err := os.ReadFile(filepath.Join(home, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("initialization failure changed existing site to %q", data)
	}
}
