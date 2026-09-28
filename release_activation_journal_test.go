package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReleaseActivationRecoversAfterProcessKill(t *testing.T) {
	if os.Getenv("STEPANEL_RELEASE_ACTIVATION_CHILD") == "1" {
		root := os.Getenv("STEPANEL_RELEASE_ACTIVATION_ROOT")
		cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
		siteRoot := filepath.Join(cfg.WebRoot, "sites", "example")
		if err := os.MkdirAll(filepath.Join(siteRoot, "public"), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(siteRoot, "public", "index.html"), []byte("old"), 0640); err != nil {
			t.Fatal(err)
		}
		manager, err := newSiteManagerForConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		staged, err := manager.CreateReleaseStaging(context.Background(), "example", ".stepanel-release-crash-")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staged, "index.html"), []byte("new"), 0640); err != nil {
			t.Fatal(err)
		}
		journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", staged)
		if err != nil {
			t.Fatal(err)
		}
		if err := journal.persist(); err != nil {
			t.Fatal(err)
		}
		if _, err := manager.ActivateStagedReplacing(context.Background(), "example", staged); err != nil {
			t.Fatal(err)
		}
		// The process dies after publication but before markActivated. Recovery
		// must use the prepared journal plus the manager-owned previous tree to
		// restore the old release rather than leaving the new tree live.
		if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		return
	}

	root := t.TempDir()
	child := exec.Command(os.Args[0], "-test.run=TestReleaseActivationRecoversAfterProcessKill", "-test.v")
	child.Env = append(os.Environ(),
		"STEPANEL_RELEASE_ACTIVATION_CHILD=1",
		"STEPANEL_RELEASE_ACTIVATION_ROOT="+root,
	)
	output, err := child.CombinedOutput()
	if err == nil {
		t.Fatal("child process unexpectedly survived the simulated crash")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("child exit = %v, want SIGKILL", err)
	}
	status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child exit = %v, want SIGKILL; output: %s", err, output)
	}

	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	if _, err := recoverReleaseActivationJournals(cfg); err != nil {
		t.Fatalf("recover release activation: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.WebRoot, "sites", "example", "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("active release after recovery = %q, want old", data)
	}
}

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
	stage := filepath.Join(siteRoot, ".stepanel-release-next")
	for _, path := range []string{previous, stage} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(previous, "index.html"), []byte("old"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "index.html"), []byte("new"), 0640); err != nil {
		t.Fatal(err)
	}

	journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", stage)
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
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("interrupted release stage still exists: %v", err)
	}
}

func TestRecoverPreparedReleaseDoesNotRollbackAnUnstartedSwap(t *testing.T) {
	root := t.TempDir()
	cfg := Config{WebRoot: filepath.Join(root, "www"), RecoveryRoot: filepath.Join(root, "recovery")}
	siteRoot := filepath.Join(cfg.WebRoot, "sites", "example")
	public := filepath.Join(siteRoot, "public")
	previous := filepath.Join(siteRoot, ".stepanel-previous-old")
	stage := filepath.Join(siteRoot, ".stepanel-release-next")
	for _, path := range []string{public, previous, stage} {
		if err := os.MkdirAll(path, 0750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(public, "index.html"), []byte("current"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previous, "index.html"), []byte("older"), 0640); err != nil {
		t.Fatal(err)
	}

	journal, err := newReleaseActivationJournal(cfg.RecoveryRoot, "example", stage)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.persist(); err != nil {
		t.Fatal(err)
	}
	if _, err := recoverReleaseActivationJournals(cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(public, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "current" {
		t.Fatalf("unstarted activation changed active release to %q", data)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("staged release was not discarded: %v", err)
	}
}
