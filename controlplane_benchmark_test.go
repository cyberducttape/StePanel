package stepanel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These benchmarks exercise the SQLite access patterns used by the control
// plane. They are synthetic workload measurements, not HTTP or filesystem
// backup throughput measurements.
func BenchmarkControlPlaneSiteMutations(b *testing.B) {
	for _, siteCount := range []int{10, 100, 500} {
		b.Run(strconv.Itoa(siteCount)+"_sites_10_mutators", func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "control.db")
			db, err := openControlPlaneDB(path)
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			tx, err := db.Begin()
			if err != nil {
				b.Fatal(err)
			}
			for i := 0; i < siteCount; i++ {
				if _, err := tx.Exec(`INSERT INTO tenant_sites(site, username, updated_at) VALUES (?, ?, ?)`, fmt.Sprintf("site-%04d", i), fmt.Sprintf("user-%04d", i%50), time.Now().Unix()); err != nil {
					b.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			var sequence atomic.Uint64
			benchmarkFixedWorkers(b, 10, func() error {
				i := sequence.Add(1) % uint64(siteCount)
				_, err := db.Exec(`UPDATE tenant_sites SET updated_at = ? WHERE site = ?`, time.Now().UnixNano(), fmt.Sprintf("site-%04d", i))
				return err
			})
		})
	}
}

// Two independent one-connection pools model the panel and external worker
// contending for the same WAL database. The cross-process CAS tests cover the
// process boundary; this benchmark quantifies the shared-file serialization.
func BenchmarkControlPlanePanelWorkerContention(b *testing.B) {
	path := filepath.Join(b.TempDir(), "control.db")
	panelDB, err := openControlPlaneDB(path)
	if err != nil {
		b.Fatal(err)
	}
	defer panelDB.Close()
	workerDB, err := openControlPlaneDB(path)
	if err != nil {
		b.Fatal(err)
	}
	defer workerDB.Close()
	if _, err := panelDB.Exec(`INSERT INTO state_blobs(name, payload, updated_at, revision) VALUES ('load', '{}', 0, 0)`); err != nil {
		b.Fatal(err)
	}
	var sequence atomic.Uint64
	benchmarkFixedWorkers(b, 10, func() error {
		db := panelDB
		if sequence.Add(1)%2 == 0 {
			db = workerDB
		}
		_, err := db.Exec(`UPDATE state_blobs SET updated_at = ?, revision = revision + 1 WHERE name = 'load'`, time.Now().UnixNano())
		return err
	})
}

// Set STEPANEL_BENCH_DB to the same path in two concurrent `go test` processes
// to measure actual panel/worker process contention on one SQLite file.
func BenchmarkControlPlaneProcessContention(b *testing.B) {
	path := os.Getenv("STEPANEL_BENCH_DB")
	if path == "" {
		path = filepath.Join(b.TempDir(), "control.db")
	}
	db, err := openControlPlaneDB(path)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	var busyTimeout int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		b.Fatal(err)
	}
	b.Logf("SQLite busy_timeout=%dms", busyTimeout)
	row := fmt.Sprintf("process-load-%d", os.Getpid())
	if _, err := db.Exec(`INSERT OR IGNORE INTO state_blobs(name, payload, updated_at, revision) VALUES (?, '{}', 0, 0)`, row); err != nil {
		b.Fatal(err)
	}
	if barrier := os.Getenv("STEPANEL_BENCH_BARRIER"); barrier != "" {
		if err := os.WriteFile(filepath.Join(barrier, fmt.Sprintf("ready-%d", os.Getpid())), []byte("ready"), 0600); err != nil {
			b.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			entries, err := os.ReadDir(barrier)
			if err != nil {
				b.Fatal(err)
			}
			if len(entries) >= 2 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		entries, err := os.ReadDir(barrier)
		if err != nil || len(entries) < 2 {
			b.Fatal("timed out waiting for peer benchmark process")
		}
	}
	var before int64
	if err := db.QueryRow(`SELECT revision FROM state_blobs WHERE name = ?`, row).Scan(&before); err != nil {
		b.Fatal(err)
	}
	benchmarkFixedWorkers(b, 1, func() error {
		started := time.Now()
		_, err := db.Exec(`UPDATE state_blobs SET updated_at = ?, revision = revision + 1 WHERE name = ?`, time.Now().UnixNano(), row)
		if err != nil {
			return fmt.Errorf("write failed after %s: %w", time.Since(started), err)
		}
		return err
	})
	var after int64
	if err := db.QueryRow(`SELECT revision FROM state_blobs WHERE name = ?`, row).Scan(&after); err != nil {
		b.Fatal(err)
	}
	if delta := after - before; delta != int64(b.N) {
		b.Fatalf("persisted process benchmark updates = %d, want %d", delta, b.N)
	}
}

// A complete batch represents the requested queue depth: 25 backups and 25
// restores are durably enqueued, claimed by a worker, and completed.
func BenchmarkControlPlaneFiftyBackupRestoreJobs(b *testing.B) {
	db, err := openControlPlaneDB(filepath.Join(b.TempDir(), "control.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	jobs := newJobsWithDB(db, 100)
	b.ResetTimer()
	for batch := 0; batch < b.N; batch++ {
		for i := 0; i < 50; i++ {
			kind := "site.backup"
			if i >= 25 {
				kind = "backup.restore"
			}
			if _, err := jobs.Enqueue(kind, fmt.Sprintf("tenant-%d", i), "", []byte(`{"site":"demo"}`), 3); err != nil {
				b.Fatal(err)
			}
		}
		for i := 0; i < 50; i++ {
			job, ok, err := jobs.ClaimNext("benchmark-worker", "site.backup", "backup.restore")
			if err != nil || !ok {
				b.Fatalf("claim %d: ok=%v err=%v", i, ok, err)
			}
			if err := jobs.finishClaim(job.ID, "benchmark-worker", func(item *Job) {
				item.State = "completed"
				item.Output = []byte(`{"ok":true}`)
			}); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.ReportMetric(float64(b.N*50), "jobs/batch")
}

func BenchmarkControlPlaneAuditHeavyMutations(b *testing.B) {
	db, err := openControlPlaneDB(filepath.Join(b.TempDir(), "control.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	outbox, err := newAuditOutboxStore(db)
	if err != nil {
		b.Fatal(err)
	}
	var sequence atomic.Uint64
	benchmarkFixedWorkers(b, 10, func() error {
		i := sequence.Add(1)
		return outbox.enqueue(context.Background(), "", "benchmark-admin", "site.updated", fmt.Sprintf("site-%d", i%500), `{"field":"updated"}`)
	})
}

func benchmarkFixedWorkers(b *testing.B, workers int, operation func() error) {
	b.Helper()
	b.ResetTimer()
	var next atomic.Int64
	var failed atomic.Bool
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := next.Add(1)
				if i > int64(b.N) {
					return
				}
				if err := operation(); err != nil {
					failed.Store(true)
					b.Errorf("operation: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if failed.Load() {
		b.FailNow()
	}
}
