package stepanel

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Upgrade drain. Before an upgrade stops StePanel, the installer asks the
// running panel and workers to drain: a durable flag in the control-plane
// database stops new job claims and refuses new mutating requests, and the
// installer waits for running jobs to finish instead of killing them
// mid-operation. The flag expires on its own, so an installer that dies
// cannot leave the panel refusing changes for good.
//
// Only panels from the release that introduced the drain honour the flag. An
// older running release ignores it; the installer then only waits for its
// running jobs.

const upgradeDrainTable = `CREATE TABLE IF NOT EXISTS upgrade_drain (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	started_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	reason TEXT NOT NULL
)`

// upgradeDrainGrace is how long the flag outlives the drain wait, to cover
// the upgrade itself.
const upgradeDrainGrace = 2 * time.Hour

// upgradeDrainStatus reports whether an unexpired drain is in effect.
func upgradeDrainStatus(db *sql.DB, now time.Time) (bool, string, error) {
	var expires int64
	var reason string
	err := db.QueryRow(`SELECT expires_at, reason FROM upgrade_drain WHERE id = 1`).Scan(&expires, &reason)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, "", nil
	case err != nil:
		// A database without the table predates the drain.
		if isMissingTableError(err) {
			return false, "", nil
		}
		return false, "", err
	case now.UnixNano() >= expires:
		return false, "", nil
	default:
		return true, reason, nil
	}
}

func isMissingTableError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such table")
}

// upgradeDrainGate caches the drain flag briefly, so gating every request
// does not query the database each time.
type upgradeDrainGate struct {
	db      *sql.DB
	mu      sync.Mutex
	checked time.Time
	active  bool
	reason  string
}

const upgradeDrainCacheTTL = time.Second

func (g *upgradeDrainGate) status() (bool, string) {
	if g == nil || g.db == nil {
		return false, ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if now.Sub(g.checked) < upgradeDrainCacheTTL {
		return g.active, g.reason
	}
	active, reason, err := upgradeDrainStatus(g.db, now)
	if err != nil {
		// Fail open: an unreadable flag must not stop the panel; the
		// installer still waits for running jobs before stopping it.
		log.Printf("read upgrade drain state: %v", err)
		active, reason = false, ""
	}
	g.checked, g.active, g.reason = now, active, reason
	return active, reason
}

// openUpgradeDatabase opens the live control-plane database without running
// any schema migration: the upgrade commands run from the candidate release
// before the database is snapshotted.
func openUpgradeDatabase(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("control-plane database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("control-plane database is not a regular file")
	}
	db, err := sql.Open("sqlite", "file:"+abs+"?mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open control-plane database: %w", err)
	}
	return db, nil
}

// upgradeHeadroomBytes is the free space an upgrade needs beyond twice the
// control-plane database (its snapshot and a migrated copy).
const upgradeHeadroomBytes = 512 << 20

// runUpgradePreflight checks that an upgrade can start safely: the
// control-plane database passes SQLite's integrity check and its filesystem
// has room for the installer's snapshot and the migration.
func runUpgradePreflight(path string) error {
	db, err := openUpgradeDatabase(path)
	if err != nil {
		return err
	}
	defer db.Close()
	var check string
	if err := db.QueryRow(`PRAGMA quick_check`).Scan(&check); err != nil {
		return fmt.Errorf("control-plane integrity check: %w", err)
	}
	if check != "ok" {
		return fmt.Errorf("control-plane database failed its integrity check: %s", check)
	}
	var size int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil {
			size += info.Size()
		}
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &stat); err != nil {
		return fmt.Errorf("inspect control-plane filesystem: %w", err)
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	if need := 2*size + upgradeHeadroomBytes; free < need {
		return fmt.Errorf("not enough free space for the upgrade: %d MiB free next to the control-plane database, need %d MiB", free>>20, need>>20)
	}
	fmt.Printf("upgrade preflight passed: control-plane database %d MiB, %d MiB free\n", size>>20, free>>20)
	return nil
}

func liveRunningJobs(db *sql.DB, now time.Time) (int, error) {
	var running int
	err := db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE state = 'running' AND lease_expires_at > ?`, now.UnixNano()).Scan(&running)
	return running, err
}

// beginUpgradeDrain sets the drain flag and waits up to wait for running
// jobs to finish. If they do not, it clears the flag and fails unless force
// is set, so a long operation is not cut off by an upgrade.
func beginUpgradeDrain(db *sql.DB, wait time.Duration, force bool, poll time.Duration) error {
	if _, err := db.Exec(upgradeDrainTable); err != nil {
		return fmt.Errorf("prepare upgrade drain: %w", err)
	}
	now := time.Now()
	reason := "an upgrade is in progress"
	if _, err := db.Exec(`INSERT INTO upgrade_drain (id, started_at, expires_at, reason) VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET started_at = excluded.started_at, expires_at = excluded.expires_at, reason = excluded.reason`,
		now.UnixNano(), now.Add(wait+upgradeDrainGrace).UnixNano(), reason); err != nil {
		return fmt.Errorf("set upgrade drain: %w", err)
	}
	deadline := now.Add(wait)
	for {
		running, err := liveRunningJobs(db, time.Now())
		if err != nil {
			return fmt.Errorf("count running jobs: %w", err)
		}
		if running == 0 {
			fmt.Println("upgrade drain: no jobs are running; new claims and mutations are paused")
			return nil
		}
		if time.Now().After(deadline) {
			if force {
				fmt.Printf("upgrade drain: %d job(s) still running after %s; continuing because the drain was forced (they resume through crash recovery)\n", running, wait)
				return nil
			}
			if err := endUpgradeDrain(db); err != nil {
				return fmt.Errorf("%d job(s) still running after %s, and the drain could not be cleared: %w", running, wait, err)
			}
			return fmt.Errorf("%d job(s) still running after %s; the upgrade was not started (wait, or set STEPANEL_UPGRADE_DRAIN_FORCE=1)", running, wait)
		}
		time.Sleep(poll)
	}
}

func endUpgradeDrain(db *sql.DB) error {
	if _, err := db.Exec(`DELETE FROM upgrade_drain WHERE id = 1`); err != nil && !isMissingTableError(err) {
		return fmt.Errorf("clear upgrade drain: %w", err)
	}
	return nil
}

// runUpgradeDrainCommand implements "stepanel upgrade-drain begin DB
// [SECONDS]" and "stepanel upgrade-drain end DB".
func runUpgradeDrainCommand(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: stepanel upgrade-drain begin CONTROL_PLANE_DB [WAIT_SECONDS] | end CONTROL_PLANE_DB")
	}
	db, err := openUpgradeDatabase(args[1])
	if err != nil {
		return err
	}
	defer db.Close()
	switch args[0] {
	case "begin":
		wait := 15 * time.Minute
		if len(args) == 3 {
			seconds, err := strconv.Atoi(args[2])
			if err != nil || seconds < 0 || seconds > 86400 {
				return errors.New("upgrade drain wait must be 0-86400 seconds")
			}
			wait = time.Duration(seconds) * time.Second
		} else if len(args) > 3 {
			return errors.New("usage: stepanel upgrade-drain begin CONTROL_PLANE_DB [WAIT_SECONDS]")
		}
		return beginUpgradeDrain(db, wait, os.Getenv("STEPANEL_UPGRADE_DRAIN_FORCE") == "1", 2*time.Second)
	case "end":
		if len(args) != 2 {
			return errors.New("usage: stepanel upgrade-drain end CONTROL_PLANE_DB")
		}
		return endUpgradeDrain(db)
	default:
		return errors.New("usage: stepanel upgrade-drain begin CONTROL_PLANE_DB [WAIT_SECONDS] | end CONTROL_PLANE_DB")
	}
}
