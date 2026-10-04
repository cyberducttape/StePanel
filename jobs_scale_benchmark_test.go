package stepanel

import (
	"database/sql"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cyberducttape/StePanel/internal/session"
)

// seedJobScale models a busy single host: a long completed history spread
// over many owners, a queue backlog, and a few running jobs.
func seedJobScale(tb testing.TB, completed, queued, running, owners int) (*Jobs, *sql.DB) {
	tb.Helper()
	db, err := openControlPlaneDB(filepath.Join(tb.TempDir(), "control.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	jobs := newJobsWithDB(db, running+queued+1)
	base := time.Now().UTC().Add(-30 * 24 * time.Hour)
	add := func(i int, state string) {
		started := base.Add(time.Duration(i) * time.Second)
		item := &Job{ID: fmt.Sprintf("job-%06d", i), Kind: "import", User: fmt.Sprintf("owner-%04d", i%owners), OperationKey: fmt.Sprintf("op-%06d", i), State: state, StartedAt: started}
		if state == "completed" {
			finished := started.Add(time.Minute)
			item.FinishedAt = &finished
		}
		if state == "running" {
			expires := time.Now().UTC().Add(time.Hour)
			item.LeaseOwner = "bench"
			item.LeaseExpires = &expires
		}
		jobs.items[item.ID] = item
	}
	i := 0
	for ; i < completed; i++ {
		add(i, "completed")
	}
	for r := 0; r < running; r, i = r+1, i+1 {
		add(i, "running")
	}
	for q := 0; q < queued; q, i = q+1, i+1 {
		add(i, "queued")
	}
	jobs.mu.Lock()
	err = jobs.persistLocked()
	jobs.mu.Unlock()
	if err != nil {
		tb.Fatal(err)
	}
	return jobs, db
}

// BenchmarkJobsListAtScale measures the Job Center listing (newest 100 plus
// all active jobs) against a 20k-row history.
func BenchmarkJobsListAtScale(b *testing.B) {
	jobs, _ := seedJobScale(b, 20000, 400, 8, 500)
	b.ResetTimer()
	for b.Loop() {
		if items := jobs.List(100); len(items) < 100 {
			b.Fatalf("listed %d jobs", len(items))
		}
	}
}

// BenchmarkJobsClaimNextAtScale measures worker claim latency with a queue
// backlog. Each claimed job is returned to the queue outside the timer.
func BenchmarkJobsClaimNextAtScale(b *testing.B) {
	jobs, db := seedJobScale(b, 20000, 400, 8, 500)
	b.ResetTimer()
	for b.Loop() {
		item, ok, err := jobs.ClaimNext("bench-worker")
		if err != nil || !ok {
			b.Fatalf("claim: ok=%v err=%v", ok, err)
		}
		b.StopTimer()
		if _, err := db.Exec(`UPDATE jobs SET state = 'queued', lease_owner = NULL, lease_expires_at = NULL WHERE id = ?`, item.ID); err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
	}
}

func TestDurableJobsListReturnsNewestAndAllActiveInOrder(t *testing.T) {
	// Active jobs are the oldest rows here, so they appear only because the
	// listing always includes queued and running work.
	jobs, _ := seedJobScale(t, 0, 0, 0, 1)
	base := time.Now().UTC().Add(-time.Hour)
	jobs.mu.Lock()
	for i, state := range []string{"queued", "running", "completed", "completed", "completed", "completed"} {
		started := base.Add(time.Duration(i) * time.Minute)
		item := &Job{ID: fmt.Sprintf("job-%d", i), Kind: "import", User: fmt.Sprintf("owner-%d", i), State: state, StartedAt: started}
		if state == "completed" {
			finished := started.Add(time.Second)
			item.FinishedAt = &finished
		}
		jobs.items[item.ID] = item
	}
	err := jobs.persistLocked()
	jobs.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, item := range jobs.List(2) {
		ids = append(ids, item.ID)
	}
	if got, want := fmt.Sprint(ids), "[job-5 job-4 job-1 job-0]"; got != want {
		t.Fatalf("listed %s, want %s", got, want)
	}
}

// BenchmarkSessionValidationUnderJobLoad measures the latency of the
// per-request session check while the Job Center polls and workers claim
// jobs on the same single-connection control-plane database. It reports
// p50/p99 alongside ns/op; see docs/CAPACITY.md for the target.
func BenchmarkSessionValidationUnderJobLoad(b *testing.B) {
	for _, load := range []struct {
		name            string
		pollers, claims int
		// Zero intervals run the background work back to back (saturation).
		pollEvery, claimEvery time.Duration
	}{
		{name: "idle"},
		// Capacity target profile: 50 open Job Center views polling each
		// second and two workers claiming ten jobs per second each.
		{name: "target_500_sites_50_viewers_2_workers", pollers: 50, claims: 2, pollEvery: time.Second, claimEvery: 100 * time.Millisecond},
		{name: "saturated_4_pollers_2_workers", pollers: 4, claims: 2},
	} {
		b.Run(load.name, func(b *testing.B) {
			jobs, db := seedJobScale(b, 20000, 400, 8, 500)
			registry, err := session.OpenDB(db)
			if err != nil {
				b.Fatal(err)
			}
			expiry := time.Now().Add(time.Hour).Unix()
			if err := registry.Add("session-1", "admin", expiry); err != nil {
				b.Fatal(err)
			}
			stop := make(chan struct{})
			var background sync.WaitGroup
			pace := func(interval time.Duration) bool {
				if interval == 0 {
					select {
					case <-stop:
						return false
					default:
						return true
					}
				}
				select {
				case <-stop:
					return false
				case <-time.After(interval):
					return true
				}
			}
			for i := 0; i < load.pollers; i++ {
				background.Add(1)
				go func() {
					defer background.Done()
					for pace(load.pollEvery) {
						jobs.List(100)
					}
				}()
			}
			for i := 0; i < load.claims; i++ {
				background.Add(1)
				go func(worker int) {
					defer background.Done()
					owner := fmt.Sprintf("bench-worker-%d", worker)
					for pace(load.claimEvery) {
						item, ok, err := jobs.ClaimNext(owner)
						if err != nil || !ok {
							continue
						}
						_, _ = db.Exec(`UPDATE jobs SET state = 'queued', lease_owner = NULL, lease_expires_at = NULL WHERE id = ?`, item.ID)
					}
				}(i)
			}
			// Sample open-loop: checks arrive at random times, as requests do.
			// A back-to-back loop would hide queueing (coordinated omission),
			// because only the few checks that collide with background work
			// wait, and they are drowned out by the many that do not.
			latencies := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for b.Loop() {
				b.StopTimer()
				time.Sleep(time.Duration(rand.Int64N(int64(2 * time.Millisecond))))
				b.StartTimer()
				start := time.Now()
				if !registry.Valid("session-1", "admin", expiry) {
					b.Fatal("session rejected")
				}
				latencies = append(latencies, time.Since(start))
			}
			b.StopTimer()
			close(stop)
			background.Wait()
			sort.Slice(latencies, func(i, k int) bool { return latencies[i] < latencies[k] })
			percentile := func(p float64) float64 {
				return float64(latencies[int(p*float64(len(latencies)-1))].Microseconds())
			}
			b.ReportMetric(percentile(0.50), "p50_µs")
			b.ReportMetric(percentile(0.90), "p90_µs")
			b.ReportMetric(percentile(0.99), "p99_µs")
		})
	}
}
