package operations

import (
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestDBLocksAcrossOSProcesses proves the lock boundary with separate test
// processes, not merely separate goroutines or database handles. This is the
// failure mode the panel/worker deployment must actually prevent.
func TestDBLocksAcrossOSProcesses(t *testing.T) {
	if mode := os.Getenv("STEPANEL_DB_LOCK_PROCESS_CHILD"); mode != "" {
		dbPath := os.Getenv("STEPANEL_DB_LOCK_PROCESS_DB")
		marker := os.Getenv("STEPANEL_DB_LOCK_PROCESS_MARKER")
		db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		locks, err := NewDBLocks(db, "child-"+mode, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := locks.TryAcquire("site:process-boundary")
		switch mode {
		case "hold":
			if err != nil {
				t.Fatalf("holder could not acquire lease: %v", err)
			}
			if err := os.WriteFile(marker, []byte("ready\n"), 0600); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(os.Getenv("STEPANEL_DB_LOCK_PROCESS_RELEASE")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("holder timed out waiting for release signal")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := locks.Release(lease); err != nil {
				t.Fatal(err)
			}
		case "blocked":
			if !errors.Is(err, ErrLockHeld) {
				t.Fatalf("contending process error = %v, want ErrLockHeld", err)
			}
			if err := os.WriteFile(marker, []byte("blocked\n"), 0600); err != nil {
				t.Fatal(err)
			}
		case "acquired":
			if err != nil {
				t.Fatalf("process could not acquire released lease: %v", err)
			}
			if err := locks.Release(lease); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, []byte("acquired\n"), 0600); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unknown child mode %q", mode)
		}
		return
	}

	root := t.TempDir()
	dbPath := filepath.Join(root, "locks.sqlite")
	releasePath := filepath.Join(root, "release")
	startChild := func(mode string) (*exec.Cmd, string) {
		marker := filepath.Join(root, mode+".marker")
		cmd := exec.Command(os.Args[0], "-test.run=^TestDBLocksAcrossOSProcesses$", "-test.v")
		cmd.Env = append(os.Environ(),
			"STEPANEL_DB_LOCK_PROCESS_CHILD="+mode,
			"STEPANEL_DB_LOCK_PROCESS_DB="+dbPath,
			"STEPANEL_DB_LOCK_PROCESS_MARKER="+marker,
			"STEPANEL_DB_LOCK_PROCESS_RELEASE="+releasePath,
		)
		return cmd, marker
	}
	waitForMarker := func(path string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(path); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for process marker %s", path)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	holder, ready := startChild("hold")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	waitForMarker(ready)

	blocked, blockedMarker := startChild("blocked")
	if output, err := blocked.CombinedOutput(); err != nil {
		t.Fatalf("contending process failed: %v\n%s", err, output)
	}
	waitForMarker(blockedMarker)
	blockedContents, err := os.ReadFile(blockedMarker)
	if err != nil || string(blockedContents) != "blocked\n" {
		t.Fatalf("contending process marker = %q, err=%v", blockedContents, err)
	}

	if err := os.WriteFile(releasePath, []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := holder.Wait(); err != nil {
		t.Fatalf("holder process failed: %v", err)
	}

	acquired, acquiredMarker := startChild("acquired")
	if output, err := acquired.CombinedOutput(); err != nil {
		t.Fatalf("post-release process failed: %v\n%s", err, output)
	}
	waitForMarker(acquiredMarker)
	acquiredContents, err := os.ReadFile(acquiredMarker)
	if err != nil || string(acquiredContents) != "acquired\n" {
		t.Fatalf("post-release process marker = %q, err=%v", acquiredContents, err)
	}
}
