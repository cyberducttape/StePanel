package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverReleaseActivationJournalRollsBackActivatedSwap(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	siteRoot := filepath.Join(cfg.WebRoot, "sites", "example")
	public := filepath.Join(siteRoot, "public")
	previous := filepath.Join(siteRoot, ".stepanel-previous-1")
	if err := os.MkdirAll(public, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(previous, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(public, "index.html"), []byte("new"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "index.html"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}

	journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", filepath.Join(siteRoot, ".stepanel-release-next"))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.persist(); err != nil {
		t.Fatal(err)
	}
	if err := journal.markActivated(previous); err != nil {
		t.Fatal(err)
	}

	ids, err := recoverReleaseActivationJournals(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != journal.ID {
		t.Fatalf("recovered IDs = %v, want %q", ids, journal.ID)
	}
	data, err := os.ReadFile(filepath.Join(public, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("active release = %q, want old", data)
	}
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Fatalf("previous release still exists: %v", err)
	}
	if _, err := os.Stat(journal.path); !os.IsNotExist(err) {
		t.Fatalf("journal still exists: %v", err)
	}
}

func TestRecoverReleaseActivationJournalRollsBackPreparedSwapAfterFirstRename(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	siteRoot := filepath.Join(cfg.WebRoot, "sites", "example")
	previous := filepath.Join(siteRoot, ".stepanel-previous-2")
	if err := os.MkdirAll(previous, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "index.html"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}

	journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", filepath.Join(siteRoot, ".stepanel-release-next"))
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.persist(); err != nil {
		t.Fatal(err)
	}
	ids, err := recoverReleaseActivationJournals(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != journal.ID {
		t.Fatalf("recovered IDs = %v, want %q", ids, journal.ID)
	}
	data, err := os.ReadFile(filepath.Join(siteRoot, "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("restored release = %q, want old", data)
	}
}
