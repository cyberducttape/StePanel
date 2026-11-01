package stepanel

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"
)

// referenceClaimSelection is the original ordered-scan claim query: the
// specification the owner-head selection must match exactly.
const referenceClaimSelection = `SELECT candidate.id FROM jobs AS candidate WHERE candidate.state = 'queued' AND (candidate.next_attempt_at IS NULL OR candidate.next_attempt_at <= ?) AND NOT EXISTS (SELECT 1 FROM jobs AS active WHERE active.owner = candidate.owner AND ((active.state = 'running' AND (active.lease_expires_at IS NULL OR active.lease_expires_at > ?)) OR (active.state = 'queued' AND (active.started_at < candidate.started_at OR (active.started_at = candidate.started_at AND active.id < candidate.id)))))`

func referenceClaim(t *testing.T, db *sql.DB, now int64, kinds []string) string {
	t.Helper()
	query, args := referenceClaimSelection, []any{now, now}
	if len(kinds) > 0 {
		query += " AND candidate.kind IN ("
		for i, kind := range kinds {
			if i > 0 {
				query += ","
			}
			query += "?"
			args = append(args, kind)
		}
		query += ")"
	}
	var id string
	err := db.QueryRow(query+" ORDER BY candidate.started_at, candidate.id LIMIT 1", args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// The owner-head claim (bounded prefix, then owner walk) must select exactly
// the job the original ordered scan selects, across owner ordering, ties,
// retry backoff, live and expired leases, kind filters and long blocked
// prefixes.
func TestClaimSelectionMatchesOrderedScanReference(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rng := rand.New(rand.NewSource(20261101))
	now := time.Now().UTC().UnixNano()
	kindsPool := []string{"site.backup", "certificate.issue", "site.create"}
	filters := [][]string{nil, {"site.backup"}, {"certificate.issue", "site.create"}}
	for round := 0; round < 300; round++ {
		if _, err := db.Exec(`DELETE FROM jobs`); err != nil {
			t.Fatal(err)
		}
		owners := 1 + rng.Intn(40)
		jobs := rng.Intn(400)
		if round%10 == 0 {
			jobs += claimPrefixLength * 3 // force the owner-walk fallback
		}
		for i := 0; i < jobs; i++ {
			owner := fmt.Sprintf("owner-%02d", rng.Intn(owners))
			state := "queued"
			switch r := rng.Intn(10); {
			case r == 0:
				state = "running"
			case r == 1:
				state = "completed"
			}
			var lease, nextAttempt any
			if state == "running" {
				if rng.Intn(3) == 0 {
					lease = now - int64(time.Minute) // expired lease
				} else if rng.Intn(5) != 0 {
					lease = now + int64(time.Minute)
				}
			}
			if state == "queued" && rng.Intn(6) == 0 {
				nextAttempt = now + int64(rng.Intn(2)*2-1)*int64(time.Minute)
			}
			started := now - int64(time.Hour) + int64(rng.Intn(200))*int64(time.Second) // frequent ties
			if _, err := db.Exec(`INSERT INTO jobs (id,kind,operation_key,state,owner,started_at,payload,lease_expires_at,next_attempt_at,cancel_requested,updated_at) VALUES (?,?,?,?,?,?,?,?,?,0,?)`,
				fmt.Sprintf("job-%04d-%03d", round, i), kindsPool[rng.Intn(len(kindsPool))], fmt.Sprintf("k-%d-%d", round, i), state, owner, started, []byte(`{}`), lease, nextAttempt, now); err != nil {
				t.Fatal(err)
			}
		}
		for _, kinds := range filters {
			want := referenceClaim(t, db, now, kinds)
			got, _, _, _, _, err := selectClaimCandidate(db, now, kinds)
			if errors.Is(err, sql.ErrNoRows) {
				got, err = "", nil
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("round %d kinds %v: selected %q, reference %q", round, kinds, got, want)
			}
		}
	}
}

// When every job in the claim-order prefix is blocked, the owner walk must
// find the eligible job behind them.
func TestClaimSelectionFallsBackPastABlockedPrefix(t *testing.T) {
	db, err := openControlPlaneDB(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().UnixNano()
	insert := func(id, owner, state string, started int64, lease any) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO jobs (id,kind,operation_key,state,owner,started_at,payload,lease_expires_at,cancel_requested,updated_at) VALUES (?,?,?,?,?,?,?,?,0,?)`, id, "site.backup", id, state, owner, started, []byte(`{}`), lease, now); err != nil {
			t.Fatal(err)
		}
	}
	insert("running", "busy", "running", now-int64(time.Hour), now+int64(time.Hour))
	for i := 0; i < claimPrefixLength*2; i++ {
		insert(fmt.Sprintf("blocked-%03d", i), "busy", "queued", now-int64(time.Hour)+int64(i+1), nil)
	}
	insert("eligible", "idle", "queued", now, nil)
	var prefixID string
	if err := db.QueryRow(claimPrefixQuery+" ORDER BY candidate.started_at, candidate.id LIMIT 1", now, now).Scan(&prefixID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("prefix query found %q, %v; the fixture must block the whole prefix", prefixID, err)
	}
	id, _, _, _, _, err := selectClaimCandidate(db, now, []string{"site.backup"})
	if err != nil || id != "eligible" {
		t.Fatalf("selected %q, %v; want the eligible job behind the blocked prefix", id, err)
	}
}
