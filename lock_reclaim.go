package stepanel

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
)

// Durable operation locks are leases: a process that dies holding one leaves
// it in resource_locks until the lease runs out (controlPlaneLeaseTime). After
// a crash or power loss that would refuse every mutation of the affected
// sites for that long after StePanel is back. Each lock owner therefore
// records its PID and process start identity, and a starting process releases
// the leases of owners on this host that provably no longer run.

const lockOwnerPrefix = "stepanel/"

// lockOwnerID names this process as a lock owner:
// stepanel/PID/BOOT_ID:START_TICKS/HOSTNAME.
func lockOwnerID(hostname string) (string, error) {
	identity, err := linuxProcessStartIdentity(os.Getpid())
	if err != nil {
		return "", fmt.Errorf("identify lock owner process: %w", err)
	}
	return fmt.Sprintf("%s%d/%s/%s", lockOwnerPrefix, os.Getpid(), identity, hostname), nil
}

// lockOwnerDead reports whether the process that owns a lease provably no
// longer runs. Anything it cannot prove keeps the lease until it expires.
func lockOwnerDead(owner, hostname string, startIdentity func(int) (string, error)) bool {
	if strings.HasPrefix(owner, lockOwnerPrefix) {
		parts := strings.SplitN(strings.TrimPrefix(owner, lockOwnerPrefix), "/", 3)
		if len(parts) != 3 || parts[2] != hostname {
			return false
		}
		pid, err := strconv.Atoi(parts[0])
		if err != nil || pid <= 0 {
			return false
		}
		actual, err := startIdentity(pid)
		switch {
		case errors.Is(err, errCapacityOwnerProcessMissing):
			return true
		case err != nil:
			return false
		default:
			// A different boot or start time means the PID was reused.
			return actual != parts[1]
		}
	}
	// Owners written before identities were recorded:
	// stepanel-HOSTNAME-PID-NANOS. Only a missing PID proves death.
	legacy := "stepanel-" + hostname + "-"
	if !strings.HasPrefix(owner, legacy) {
		return false
	}
	fields := strings.Split(strings.TrimPrefix(owner, legacy), "-")
	if len(fields) != 2 {
		return false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return false
	}
	_, err = startIdentity(pid)
	return errors.Is(err, errCapacityOwnerProcessMissing)
}

// reclaimDeadOwnerLocks releases live leases whose owners on this host no
// longer run. Release keeps the row and its generation, so a fencing token
// the dead owner issued can never match a later lease.
func reclaimDeadOwnerLocks(db *sql.DB, hostname string, now time.Time, startIdentity func(int) (string, error)) (int, error) {
	rows, err := db.Query(`SELECT resource_key, owner_id, generation FROM resource_locks WHERE lease_until > ?`, now.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("list held operation locks: %w", err)
	}
	type held struct {
		key, owner string
		generation int64
	}
	var dead []held
	for rows.Next() {
		var item held
		if err := rows.Scan(&item.key, &item.owner, &item.generation); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("read held operation lock: %w", err)
		}
		if lockOwnerDead(item.owner, hostname, startIdentity) {
			dead = append(dead, item)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("list held operation locks: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("list held operation locks: %w", err)
	}
	released := 0
	for _, item := range dead {
		result, err := db.Exec(`UPDATE resource_locks SET lease_until = 0
			WHERE resource_key = ? AND owner_id = ? AND generation = ? AND lease_until > 0`, item.key, item.owner, item.generation)
		if err != nil {
			return released, fmt.Errorf("release lock %s of exited owner: %w", item.key, err)
		}
		if affected, err := result.RowsAffected(); err == nil && affected == 1 {
			released++
			log.Printf("released operation lock %s held by exited process %s", item.key, item.owner)
		}
	}
	return released, nil
}
