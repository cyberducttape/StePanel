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
		"php_profiles":        reflect.TypeOf(PHPProfile{}),
		"composer_operations": reflect.TypeOf(ComposerOperation{}),
		"backup_schedules":    reflect.TypeOf(BackupSchedule{}),
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

func TestPHPProfilesAndComposerOperationsUseTheirTables(t *testing.T) {
	db := openTestControlPlane(t)
	verified := time.Date(2026, 11, 26, 13, 0, 0, 1, time.UTC)
	legacy := map[string]PHPProfile{"shop": {Site: "shop", Version: "8.3", MemoryLimit: "256M", MaxExecutionTime: 60, UploadMaxFilesize: "64M", PostMaxSize: "64M", MaxInputVars: 3000, OPcache: true, ErrorReporting: "E_ALL", State: "applied", VerifiedAt: verified, Verification: []string{"php -v ok", "opcache on"}}}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('php', ?, unixepoch(), 1)`, payload); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migratePHPProfilesBlob(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	php := &PHPProfileStore{values: map[string]PHPProfile{}}
	if err := bindPHPProfiles(php, db); err != nil {
		t.Fatal(err)
	}
	got := php.values["shop"]
	want := legacy["shop"]
	if !got.VerifiedAt.Equal(verified) || !reflect.DeepEqual(got.Verification, want.Verification) || !got.OPcache || got.DisplayErrors || got.MaxInputVars != 3000 || got.MemoryLimit != "256M" {
		t.Fatalf("migrated PHP profile = %#v", got)
	}
	got.VerifiedAt, want.VerifiedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated PHP profile = %#v, want %#v", got, want)
	}
	// An in-place edit of the verification list is detected and persisted.
	profile := php.values["shop"]
	profile.Verification[1] = "opcache off"
	if err := php.saveLocked("shop", profile); err != nil {
		t.Fatal(err)
	}
	reopened := &PHPProfileStore{values: map[string]PHPProfile{}}
	if err := bindPHPProfiles(reopened, db); err != nil {
		t.Fatal(err)
	}
	if got := reopened.values["shop"].Verification; len(got) != 2 || got[1] != "opcache off" {
		t.Fatalf("verification after an in-place edit = %v", got)
	}

	composer := &ComposerStore{latest: map[string]ComposerOperation{}}
	if err := bindComposerOperations(composer, db); err != nil {
		t.Fatal(err)
	}
	composer.latest["shop"] = ComposerOperation{Site: "shop", Command: "install", Completed: verified, DurationMS: 4200, Commit: "abc123"}
	if err := composer.persistLocked(); err != nil {
		t.Fatal(err)
	}
	delete(composer.latest, "shop")
	if err := composer.persistLocked(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM composer_operations`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("composer_operations after delete: %d, %v", n, err)
	}
}

func TestBackupSchedulesMigrateWithOptionalTimes(t *testing.T) {
	db := openTestControlPlane(t)
	next := time.Date(2026, 11, 27, 1, 0, 0, 0, time.UTC)
	last := time.Date(2026, 11, 26, 1, 0, 0, 3, time.UTC)
	legacy := map[string]BackupSchedule{
		"shop": {Site: "shop", IntervalMinutes: 60, KeepLast: 7, IncludeDatabases: true, Enabled: true, NextRun: next, LastRun: &last, LastSuccess: &last, LastDurationMS: 4200, RetentionCheckedAt: &last, RetentionUnattributed: []string{"stray.tar"}, RetentionUnattributedCount: 1},
		"blog": {Site: "blog", IntervalMinutes: 1440, KeepLast: 3, NextRun: next},
	}
	payload, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO state_blobs (name, payload, updated_at, revision) VALUES ('backup-schedules', ?, unixepoch(), 1)`, payload); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateBackupSchedulesBlob(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	store := &backupSchedules{items: map[string]BackupSchedule{}}
	if err := bindBackupSchedules(store, db); err != nil {
		t.Fatal(err)
	}
	shop := store.items["shop"]
	if shop.LastRun == nil || !shop.LastRun.Equal(last) || shop.LastSuccess == nil || !shop.NextRun.Equal(next) || len(shop.RetentionUnattributed) != 1 || !shop.IncludeDatabases {
		t.Fatalf("migrated shop schedule = %#v", shop)
	}
	if blog := store.items["blog"]; blog.LastRun != nil || blog.LastSuccess != nil || blog.RetentionCheckedAt != nil || blog.Enabled {
		t.Fatalf("migrated blog schedule (no optional times) = %#v", blog)
	}
	// Changing a pointed-to time in place is still detected.
	*store.items["shop"].LastRun = last.Add(time.Hour)
	if err := store.persistLocked(); err != nil {
		t.Fatal(err)
	}
	reopened := &backupSchedules{items: map[string]BackupSchedule{}}
	if err := bindBackupSchedules(reopened, db); err != nil {
		t.Fatal(err)
	}
	if got := reopened.items["shop"].LastRun; got == nil || !got.Equal(last.Add(time.Hour)) {
		t.Fatalf("last run after an in-place change = %v", got)
	}
}
