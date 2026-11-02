package stepanel

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// BenchmarkJobsStartupLoad measures what a panel or worker restart pays to
// load the durable queue: every queued, running, and dead-letter job is read
// and decoded into memory. Payloads are about 2 KiB, representative of site
// operation requests.
func BenchmarkJobsStartupLoad(b *testing.B) {
	payload, err := json.Marshal(map[string]string{"site": "example", "body": strings.Repeat("x", 2000)})
	if err != nil {
		b.Fatal(err)
	}
	for _, depth := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("queued_%d", depth), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "control.db")
			db, err := openControlPlaneDB(path)
			if err != nil {
				b.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				b.Fatal(err)
			}
			stmt, err := tx.Prepare(`INSERT INTO jobs (id,kind,operation_key,state,owner,started_at,payload,cancel_requested,updated_at) VALUES (?,?,?,?,?,?,?,0,unixepoch())`)
			if err != nil {
				b.Fatal(err)
			}
			base := time.Now().Add(-time.Hour).UnixNano()
			encoder := newJobsWithDB(db)
			for i := 0; i < depth; i++ {
				id := fmt.Sprintf("site.backup-%08d", i)
				owner := fmt.Sprintf("site-%05d", i%5000)
				// Rows are written exactly as Enqueue writes them.
				record, _, _, _, err := encoder.encodeDurableItem(&Job{ID: id, Kind: "site.backup", State: "queued", User: owner, StartedAt: time.Unix(0, base+int64(i)).UTC(), MaxAttempts: 3, Payload: payload})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := stmt.Exec(id, "site.backup", "", "queued", owner, base+int64(i), record); err != nil {
					b.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			db.Close()

			var peak uint64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				db, err := openControlPlaneDB(path)
				if err != nil {
					b.Fatal(err)
				}
				runtime.GC()
				var before runtime.MemStats
				runtime.ReadMemStats(&before)
				jobs, err := openDurableJobsDB(db, "")
				if err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				runtime.GC()
				var after runtime.MemStats
				runtime.ReadMemStats(&after)
				if after.HeapAlloc > before.HeapAlloc && after.HeapAlloc-before.HeapAlloc > peak {
					peak = after.HeapAlloc - before.HeapAlloc
				}
				if len(jobs.items) != depth {
					b.Fatalf("loaded %d jobs, want %d", len(jobs.items), depth)
				}
				runtime.KeepAlive(jobs)
				db.Close()
				b.StartTimer()
			}
			b.ReportMetric(float64(peak)/(1<<20), "retained_MiB")
		})
	}
}

// Current rows (base64 payload) and rows written before payload encoding
// (raw JSON payload) both load.
func TestDurableJobRecordDecodesCurrentAndLegacyPayloads(t *testing.T) {
	encoder := newJobsWithDB(nil)
	current, _, _, _, err := encoder.encodeDurableItem(&Job{ID: "site.backup-1", Kind: "site.backup", State: "queued", User: "shop", Payload: []byte(`{"site":"shop"}`)})
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"id":"site.backup-2","kind":"site.backup","state":"queued","user":"shop","payload":{"site":"shop"}}`)
	empty := []byte(`{"id":"site.backup-3","kind":"site.backup","state":"queued","user":"shop"}`)
	for name, tc := range map[string]struct {
		data    []byte
		payload string
	}{"current": {current, `{"site":"shop"}`}, "legacy": {legacy, `{"site":"shop"}`}, "no payload": {empty, ""}} {
		item, err := decodeDurableJobRecord(tc.data)
		if err != nil || item.ID == "" || string(item.Payload) != tc.payload {
			t.Errorf("%s: decoded %+v (payload %q), %v", name, item, item.Payload, err)
		}
	}
	if _, err := decodeDurableJobRecord([]byte(`{"id":`)); err == nil {
		t.Error("truncated record decoded")
	}
}
