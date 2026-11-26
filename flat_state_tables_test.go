package stepanel

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Each generated-statement table holds exactly its struct's fields, so a new
// field cannot be written to a missing column or silently dropped.
func TestReflectRowTablesMatchTheirStructs(t *testing.T) {
	db := openTestControlPlane(t)
	for table, typ := range map[string]reflect.Type{
		"dns_desired_records": reflect.TypeOf(DNSDesiredRecord{}),
		"site_workers":        reflect.TypeOf(Worker{}),
		"redis_allocations":   reflect.TypeOf(RedisAllocation{}),
	} {
		columns, err := reflectColumnsOf(typ)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"revision", "store_key"}
		for _, c := range columns {
			want = append(want, c.name)
		}
		sort.Strings(want)
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			got = append(got, name)
		}
		rows.Close()
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s columns = %v, %s fields need %v; add a migration for the new field", table, got, typ.Name(), want)
		}
	}
}

func TestFlatStoresMigrateFromTheirBlobs(t *testing.T) {
	db := openTestControlPlane(t)
	updated := time.Date(2026, 11, 26, 12, 0, 0, 9, time.UTC)
	dns := map[string]DNSDesiredRecord{"zone/www/A": {Key: "zone/www/A", DomainID: "zone", Type: "A", Name: "www", Target: "203.0.113.5", TTL: 300, Action: "upsert", Actor: "admin", State: "applied", UpdatedAt: updated}}
	workers := map[string]Worker{"shop/queue": {Site: "shop", Name: "queue", Type: "laravel", Processes: 2, MemoryMB: 256, Retries: 3, Root: "/var/www/sites/shop", State: "applied", Deleted: true}}
	redis := map[string]RedisAllocation{"shop": {Site: "shop", Database: 4, Namespace: "shop:", MemoryMB: 64, Eviction: "allkeys-lru"}}
	for name, value := range map[string]any{"dns-desired": dns, "workers": workers, "redis": redis} {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES (?, ?, unixepoch(), 1)`, name, payload); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, migrate := range []func(*sql.Tx) error{migrateDNSDesiredBlob, migrateWorkersBlob, migrateRedisAllocationsBlob} {
		if err := migrate(tx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dnsStore := &DNSDesiredStore{values: map[string]DNSDesiredRecord{}}
	workerStore := &WorkerStore{values: map[string]Worker{}}
	redisStore := &RedisAllocationStore{values: map[string]RedisAllocation{}}
	if err := bindDNSDesired(dnsStore, db); err != nil {
		t.Fatal(err)
	}
	if err := bindWorkers(workerStore, db); err != nil {
		t.Fatal(err)
	}
	if err := bindRedisAllocations(redisStore, db); err != nil {
		t.Fatal(err)
	}
	if got := dnsStore.values["zone/www/A"]; !got.UpdatedAt.Equal(updated) || got.TTL != 300 || got.Target != "203.0.113.5" {
		t.Fatalf("migrated DNS record = %#v", got)
	}
	if got := workerStore.values["shop/queue"]; got != workers["shop/queue"] {
		t.Fatalf("migrated worker = %#v", got)
	}
	if got := redisStore.values["shop"]; got != redis["shop"] {
		t.Fatalf("migrated Redis allocation = %#v", got)
	}
	var blobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM state_blobs WHERE name IN ('dns-desired', 'workers', 'redis')`).Scan(&blobs); err != nil || blobs != 0 {
		t.Fatalf("blobs after migration: %d, %v", blobs, err)
	}
}

// A migration writes only the columns its table has, so a field added in a
// later release cannot break upgrading an older database.
func TestBlobMigrationWritesOnlyExistingColumns(t *testing.T) {
	db := openTestControlPlane(t)
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('narrow', '{"shop":{"site":"shop","database":4,"namespace":"shop:","memory_mb":64,"eviction":"noeviction"}}', unixepoch(), 1)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	// An older table without the eviction column.
	narrow := `CREATE TABLE narrow_redis (store_key TEXT PRIMARY KEY, "site" TEXT NOT NULL, "database" INTEGER NOT NULL, "namespace" TEXT NOT NULL, "memory_mb" INTEGER NOT NULL DEFAULT 0, revision INTEGER NOT NULL DEFAULT 1)`
	if err := migrateBlobToReflectTable[RedisAllocation](tx, "narrow", "narrow_redis", narrow); err != nil {
		t.Fatalf("migration into an older table: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var database int
	if err := db.QueryRow(`SELECT "database" FROM narrow_redis WHERE store_key = 'shop'`).Scan(&database); err != nil || database != 4 {
		t.Fatalf("migrated row = %d, %v", database, err)
	}
}

func TestGeneratedRowTablesRefuseStaleWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control-plane.db")
	panelDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer panelDB.Close()
	workerDB, err := openControlPlaneDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer workerDB.Close()
	panel := &WorkerStore{values: map[string]Worker{}}
	worker := &WorkerStore{values: map[string]Worker{}}
	if err := bindWorkers(panel, panelDB); err != nil {
		t.Fatal(err)
	}
	if err := bindWorkers(worker, workerDB); err != nil {
		t.Fatal(err)
	}
	panel.values["shop/queue"] = Worker{Site: "shop", Name: "queue", Type: "laravel", Processes: 1}
	if err := panel.persistLocked(); err != nil {
		t.Fatal(err)
	}
	worker.values["shop/queue"] = Worker{Site: "shop", Name: "queue", Type: "laravel", Processes: 9}
	if err := worker.persistLocked(); !errors.Is(err, errControlPlaneStateRefreshed) {
		t.Fatalf("a stale worker write = %v", err)
	}
	if got := worker.values["shop/queue"]; got.Processes != 1 {
		t.Fatalf("the refreshed worker = %#v", got)
	}
	// Same-process update and delete through the generated statements.
	panel.values["shop/queue"] = Worker{Site: "shop", Name: "queue", Type: "laravel", Processes: 3, Deleted: true}
	if err := panel.persistLocked(); err != nil {
		t.Fatal(err)
	}
	delete(panel.values, "shop/queue")
	if err := panel.persistLocked(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := panelDB.QueryRow(`SELECT COUNT(*) FROM site_workers`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("site_workers after delete: %d, %v", n, err)
	}
}
