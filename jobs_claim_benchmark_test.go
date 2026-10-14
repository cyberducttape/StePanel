package stepanel

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const claimBenchmarkStalledOwners = 10

// BenchmarkClaimNextQueueDepth measures the durable selection path when a
// backlog is dominated by owners whose earlier operation is already running.
// That forces the ordered candidate scan to skip blocked work before reaching
// the one eligible owner, rather than measuring the best case at the queue
// head. It is a synthetic SQLite microbenchmark, not a production capacity
// guarantee.
func BenchmarkClaimNextQueueDepth(b *testing.B) {
	for _, depth := range []int{1_000, 10_000, 100_000} {
		for _, addOrderedIndex := range []bool{false, true} {
			indexVariant := "existing_indexes"
			if addOrderedIndex {
				indexVariant = "with_ordered_claim_index"
			}
			b.Run(fmt.Sprintf("queued_%d_stalled_owners_%d/%s", depth, claimBenchmarkStalledOwners, indexVariant), func(b *testing.B) {
				db, jobs, eligibleID := newClaimBenchmarkStore(b, depth, addOrderedIndex)
				defer db.Close()
				if depth == 100_000 {
					b.Logf("claim query plan: %s", strings.Join(claimBenchmarkClaimPlan(b, db), " | "))
				}

				b.ReportMetric(float64(depth), "queued")
				b.ReportMetric(float64(claimBenchmarkStalledOwners), "stalled_owners")
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					claimed, ok, err := jobs.ClaimNext("claim-benchmark-worker", "site.backup")
					if err != nil {
						b.Fatal(err)
					}
					if !ok || claimed.ID != eligibleID {
						b.Fatalf("claim = %q, %t; want eligible job %q", claimed.ID, ok, eligibleID)
					}

					// Re-arm the sole eligible job outside the timed section. The
					// blocked backlog stays unchanged, so each sample measures the
					// same queue-depth/owner-contention scenario.
					b.StopTimer()
					if _, err := db.Exec(`UPDATE jobs SET state='queued', lease_owner=NULL, lease_expires_at=NULL WHERE id=?`, eligibleID); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
			})
		}
	}
}

func claimBenchmarkClaimPlan(b *testing.B, db *sql.DB) []string {
	b.Helper()
	now := time.Now().UTC().UnixNano()
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT id, payload, kind, owner, started_at FROM jobs AS candidate WHERE candidate.state = 'queued' AND (candidate.next_attempt_at IS NULL OR candidate.next_attempt_at <= ?) AND NOT EXISTS (SELECT 1 FROM jobs AS active WHERE active.owner = candidate.owner AND ((active.state = 'running' AND (active.lease_expires_at IS NULL OR active.lease_expires_at > ?)) OR (active.state = 'queued' AND (active.started_at < candidate.started_at OR (active.started_at = candidate.started_at AND active.id < candidate.id))))) AND kind IN (?) ORDER BY candidate.started_at, candidate.id LIMIT 1`, now, now, "site.backup")
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			b.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
	return plan
}

func newClaimBenchmarkStore(b *testing.B, queued int, addOrderedIndex bool) (*sql.DB, *Jobs, string) {
	b.Helper()
	db, err := openControlPlaneDB(filepath.Join(b.TempDir(), "control.db"))
	if err != nil {
		b.Fatal(err)
	}
	if addOrderedIndex {
		if _, err := db.Exec(`CREATE INDEX claim_bench_kind_state_started_idx ON jobs(kind,state,started_at,id)`); err != nil {
			_ = db.Close()
			b.Fatal(err)
		}
	}
	base := time.Now().UTC().Add(-time.Hour).UnixNano()
	leaseExpiry := time.Now().UTC().Add(time.Hour).UnixNano()
	tx, err := db.Begin()
	if err != nil {
		_ = db.Close()
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO jobs (id,kind,operation_key,state,owner,started_at,finished_at,payload,lease_owner,lease_expires_at,next_attempt_at,cancel_requested,updated_at) VALUES (?,?,? ,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		b.Fatal(err)
	}
	insert := func(id, owner, state, leaseOwner string, startedAt, expiry int64) {
		b.Helper()
		payload, marshalErr := jsonJobState(id, owner, state, startedAt)
		if marshalErr != nil {
			b.Fatal(marshalErr)
		}
		if _, execErr := stmt.Exec(id, "site.backup", "", state, owner, startedAt, nil, payload, nullableBenchmarkString(leaseOwner), nullableBenchmarkInt(expiry), nil, 0, base); execErr != nil {
			b.Fatal(execErr)
		}
	}
	for owner := 0; owner < claimBenchmarkStalledOwners; owner++ {
		name := fmt.Sprintf("stalled-%02d", owner)
		insert(fmt.Sprintf("active-%02d", owner), name, "running", "stalled-worker", base+int64(owner), leaseExpiry)
	}
	for i := 0; i < queued; i++ {
		owner := fmt.Sprintf("stalled-%02d", i%claimBenchmarkStalledOwners)
		insert(fmt.Sprintf("queued-%09d", i), owner, "queued", "", base+claimBenchmarkStalledOwners+int64(i), 0)
	}
	eligibleID := "eligible-after-backlog"
	insert(eligibleID, "eligible-owner", "queued", "", base+claimBenchmarkStalledOwners+int64(queued)+1, 0)
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		b.Fatal(err)
	}
	return db, newJobsWithDB(db, queued+claimBenchmarkStalledOwners+1), eligibleID
}

func jsonJobState(id, owner, state string, startedAt int64) ([]byte, error) {
	started := time.Unix(0, startedAt).UTC()
	return json.Marshal(persistedJob{Job: Job{ID: id, Kind: "site.backup", State: state, User: owner, StartedAt: started}})
}

func nullableBenchmarkString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBenchmarkInt(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}
