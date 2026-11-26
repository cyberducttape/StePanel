package stepanel

import "database/sql"

// Flat map-shaped stores kept one row per key with generated statements
// (reflect_rows.go). Each table is created by literal SQL in its migration.

const dnsDesiredTable = `CREATE TABLE IF NOT EXISTS dns_desired_records (
	store_key TEXT PRIMARY KEY,
	"key" TEXT NOT NULL,
	"domain_id" TEXT NOT NULL,
	"record_id" TEXT NOT NULL DEFAULT '',
	"type" TEXT NOT NULL,
	"name" TEXT NOT NULL,
	"target" TEXT NOT NULL,
	"ttl" INTEGER NOT NULL DEFAULT 0,
	"priority" INTEGER NOT NULL DEFAULT 0,
	"service" TEXT NOT NULL DEFAULT '',
	"protocol" TEXT NOT NULL DEFAULT '',
	"port" INTEGER NOT NULL DEFAULT 0,
	"weight" INTEGER NOT NULL DEFAULT 0,
	"action" TEXT NOT NULL,
	"actor" TEXT NOT NULL DEFAULT '',
	"state" TEXT NOT NULL,
	"last_error" TEXT NOT NULL DEFAULT '',
	"updated_at" TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1
)`

const workersTable = `CREATE TABLE IF NOT EXISTS site_workers (
	store_key TEXT PRIMARY KEY,
	"site" TEXT NOT NULL,
	"name" TEXT NOT NULL,
	"type" TEXT NOT NULL,
	"processes" INTEGER NOT NULL DEFAULT 0,
	"memory_mb" INTEGER NOT NULL DEFAULT 0,
	"retries" INTEGER NOT NULL DEFAULT 0,
	"root" TEXT NOT NULL DEFAULT '',
	"state" TEXT NOT NULL DEFAULT '',
	"last_error" TEXT NOT NULL DEFAULT '',
	"deleted" INTEGER NOT NULL DEFAULT 0 CHECK ("deleted" IN (0, 1)),
	revision INTEGER NOT NULL DEFAULT 1
)`

const redisAllocationsTable = `CREATE TABLE IF NOT EXISTS redis_allocations (
	store_key TEXT PRIMARY KEY,
	"site" TEXT NOT NULL,
	"database" INTEGER NOT NULL,
	"namespace" TEXT NOT NULL,
	"memory_mb" INTEGER NOT NULL DEFAULT 0,
	"eviction" TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1
)`

var (
	dnsDesiredRows      = newReflectRowTable[DNSDesiredRecord]("DNS desired records", "dns_desired_records")
	workerRows          = newReflectRowTable[Worker]("site workers", "site_workers")
	redisAllocationRows = newReflectRowTable[RedisAllocation]("Redis allocations", "redis_allocations")
)

func migrateDNSDesiredBlob(tx *sql.Tx) error {
	return migrateBlobToReflectTable[DNSDesiredRecord](tx, "dns-desired", "dns_desired_records", dnsDesiredTable)
}

func migrateWorkersBlob(tx *sql.Tx) error {
	return migrateBlobToReflectTable[Worker](tx, "workers", "site_workers", workersTable)
}

func migrateRedisAllocationsBlob(tx *sql.Tx) error {
	return migrateBlobToReflectTable[RedisAllocation](tx, "redis", "redis_allocations", redisAllocationsTable)
}

func bindDNSDesired(s *DNSDesiredStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = &keyedRowState[DNSDesiredRecord]{}
	}
	return dnsDesiredRows.bind(s.rows, db, s.values)
}

func bindWorkers(s *WorkerStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = &keyedRowState[Worker]{}
	}
	return workerRows.bind(s.rows, db, s.values)
}

func bindRedisAllocations(s *RedisAllocationStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = &keyedRowState[RedisAllocation]{}
	}
	return redisAllocationRows.bind(s.rows, db, s.values)
}

const phpProfilesTable = `CREATE TABLE IF NOT EXISTS php_profiles (
	store_key TEXT PRIMARY KEY,
	"site" TEXT NOT NULL,
	"version" TEXT NOT NULL,
	"memory_limit" TEXT NOT NULL DEFAULT '',
	"max_execution_time" INTEGER NOT NULL DEFAULT 0,
	"upload_max_filesize" TEXT NOT NULL DEFAULT '',
	"post_max_size" TEXT NOT NULL DEFAULT '',
	"max_input_vars" INTEGER NOT NULL DEFAULT 0,
	"opcache" INTEGER NOT NULL DEFAULT 0 CHECK ("opcache" IN (0, 1)),
	"display_errors" INTEGER NOT NULL DEFAULT 0 CHECK ("display_errors" IN (0, 1)),
	"error_reporting" TEXT NOT NULL DEFAULT '',
	"preview_token" TEXT NOT NULL DEFAULT '',
	"state" TEXT NOT NULL DEFAULT '',
	"last_error" TEXT NOT NULL DEFAULT '',
	"verified_at" TEXT NOT NULL DEFAULT '',
	"verification" TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1
)`

const composerOperationsTable = `CREATE TABLE IF NOT EXISTS composer_operations (
	store_key TEXT PRIMARY KEY,
	"site" TEXT NOT NULL,
	"command" TEXT NOT NULL,
	"completed" TEXT NOT NULL DEFAULT '',
	"duration_ms" INTEGER NOT NULL DEFAULT 0,
	"commit" TEXT NOT NULL DEFAULT '',
	revision INTEGER NOT NULL DEFAULT 1
)`

var (
	phpProfileRows        = newReflectRowTable[PHPProfile]("PHP profiles", "php_profiles")
	composerOperationRows = newReflectRowTable[ComposerOperation]("Composer operations", "composer_operations")
)

func migratePHPProfilesBlob(tx *sql.Tx) error {
	return migrateBlobToReflectTable[PHPProfile](tx, "php", "php_profiles", phpProfilesTable)
}

func migrateComposerOperationsBlob(tx *sql.Tx) error {
	return migrateBlobToReflectTable[ComposerOperation](tx, "composer", "composer_operations", composerOperationsTable)
}

func bindPHPProfiles(s *PHPProfileStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = &keyedRowState[PHPProfile]{}
	}
	return phpProfileRows.bind(s.rows, db, s.values)
}

func bindComposerOperations(s *ComposerStore, db *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = &keyedRowState[ComposerOperation]{}
	}
	return composerOperationRows.bind(s.rows, db, s.latest)
}
