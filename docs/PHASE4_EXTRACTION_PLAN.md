# Phase 4 Extraction Plan (v1.0.0+)

**Status:** Planning  
**Scope:** Extract cross-cutting infrastructure domains (runtime, database, DNS, controlplane)  
**Estimated commits:** 80-120 across all domains  
**Prerequisite:** Phases 1-3 must be complete  

## Architecture Layer: Foundation

Phase 4 is the foundation layer that everything else depends on. These domains are extracted *last* because:
- They have deep coupling to app startup and lifecycle
- Multiple domains depend on them (not one-way dependencies)
- Changing them affects the entire codebase
- They require careful sequencing to avoid circular dependencies

```
Phase 4 (Foundation Layer)
├── controlplane/   (app startup, routing, config)
├── database/       (schema, pooling, replication)
├── runtime/        (process management, recovery)
└── dns/            (resolution, validation, providers)
     ↑
     All other domains depend on these four
```

## Domain Dependencies (Complex!)

```
controlplane/      (entry point, main loop)
  ├─→ runtime/     (worker lifecycle, signals)
  ├─→ database/    (connection pooling)
  ├─→ dns/         (DNS configuration)
  └─→ ALL domains  (routing, handler dispatch)

database/          (connection management)
  ├─→ runtime/     (background tasks)
  └─→ dns/         (schema validation queries)

runtime/           (process management)
  ├─→ database/    (connection pooling, migrations)
  └─→ dns/         (service health checks)

dns/               (DNS services)
  └─→ database/    (zone storage)
```

## Extraction Order (Most to Least Dependent)

### Domain 1: `internal/dns/` (LOWEST SCOPE)

**Current location:** dns.go (~69 lines), dns_desired.go (~134 lines), plus DNS validation logic scattered

**Logic to extract:**
- DNS query resolution and caching
- Zone validation (DNSSEC, SOA records)
- DNS provider integration (Route53, Cloudflare, etc.)
- Allowlist validation for zone updates
- Health checks via DNS queries

**Dependencies:**
- database (zone configuration storage)
- audit (log DNS provider operations)
- No other domain dependencies

**New interfaces:**
```go
type DNSResolver interface {
    Resolve(ctx context.Context, domain string) ([]net.IP, error)
    ValidateZone(ctx context.Context, zone string) error
    GetSOA(ctx context.Context, zone string) (*SOARecord, error)
}

type DNSProvider interface {
    CreateRecord(ctx context.Context, zone string, record *DNSRecord) error
    DeleteRecord(ctx context.Context, zone string, recordID string) error
    ListRecords(ctx context.Context, zone string) ([]DNSRecord, error)
}

type ZoneValidator interface {
    ValidateUpdate(ctx context.Context, zone string, change *ZoneChange) error
    CheckAllowlist(domain string) bool
}
```

**Files to create:**
- `internal/dns/types.go` — DNSRecord, Zone, Provider types
- `internal/dns/resolver.go` — Query resolution and caching
- `internal/dns/validator.go` — Zone validation and DNSSEC
- `internal/dns/provider.go` — Provider abstraction (Route53, Cloudflare)
- `internal/dns/allowlist.go` — Domain allowlist enforcement

**Breaking changes:** None (DNS operations are isolated)

---

### Domain 2: `internal/runtime/` (WORKER & LIFECYCLE)

**Current location:** jobs.go worker logic, signal handling in main.go, recovery.go (~449 lines), plus process management scattered

**Logic to extract:**
- Worker process lifecycle (startup, shutdown, signal handling)
- Process locking and coordination
- Recovery mode detection and execution
- Health checks and readiness probes
- Graceful shutdown coordination

**Dependencies:**
- database (worker state, recovery state)
- audit (log recovery operations)
- jobqueue (worker process pulls jobs)
- No other domain dependencies

**New interfaces:**
```go
type Worker interface {
    Start(ctx context.Context, handlers map[string]JobHandler) error
    Shutdown(ctx context.Context, timeout time.Duration) error
    Health() WorkerHealth
}

type ProcessManager interface {
    Acquire(lockPath string) (Lock, error)
    WaitForShutdown() error
    HandleSignal(sig os.Signal) error
}

type RecoveryHandler interface {
    Detect(ctx context.Context, cfg *Config) (*RecoveryState, error)
    Execute(ctx context.Context, recovery *RecoveryState) error
}
```

**Files to create:**
- `internal/runtime/types.go` — Worker, Lock, RecoveryState types
- `internal/runtime/worker.go` — Worker lifecycle management
- `internal/runtime/process.go` — Process locking and signals
- `internal/runtime/recovery.go` — Crash recovery (replaces recovery.go)
- `internal/runtime/health.go` — Health checks, readiness probes

**Breaking changes:** Moderate (worker initialization changes)

---

### Domain 3: `internal/database/` (DEEPEST COUPLING)

**Current location:** database.go (~106 lines), database_operations.go (~445 lines), plus schema migrations in main.go

**Logic to extract:**
- Database connection pooling and lifecycle
- Schema migrations and DDL management
- Replication setup and failover
- Connection validation and health checks
- Statement preparation and caching

**Dependencies:**
- audit (log database operations)
- dns (possibly for DNS failover)
- No other domain dependencies

**New interfaces:**
```go
type DatabaseManager interface {
    OpenConnection(ctx context.Context, cfg *DatabaseConfig) (Connection, error)
    GetPool() *sql.DB
    MigrateSchema(ctx context.Context) error
    ValidateConnectivity(ctx context.Context) error
}

type SchemaManager interface {
    GetVersion(ctx context.Context) (int, error)
    Migrate(ctx context.Context, target int) error
    Status(ctx context.Context) (*MigrationStatus, error)
}

type ReplicationManager interface {
    ConfigureReplica(ctx context.Context, replica *ReplicaConfig) error
    CheckHealth(ctx context.Context) ReplicaHealth
    Failover(ctx context.Context) error
}
```

**Files to create:**
- `internal/database/types.go` — Connection, Pool, Schema types
- `internal/database/manager.go` — Connection lifecycle
- `internal/database/schema.go` — Schema migrations (replaces schema logic in main.go)
- `internal/database/replication.go` — HA and failover
- `internal/database/health.go` — Connection validation

**Breaking changes:** Moderate (connection initialization refactored)

---

### Domain 4: `internal/controlplane/` (ENTRY POINT)

**Current location:** main.go (startup logic ~300 lines), controlplane.go (~528 lines), routing and middleware

**Logic to extract:**
- Application startup sequence
- HTTP server initialization and configuration
- Route registration and middleware
- Graceful shutdown coordination
- Configuration loading and validation
- TLS/certificate initialization

**Dependencies:**
- ALL other domains (routes depend on every handler)
- audit (initialized at startup)
- jobqueue (workers started at startup)
- sites, migration, deployment (handlers registered)
- runtime (lifecycle coordination)
- database (connections initialized)
- dns (configuration loaded)

**New interfaces:**
```go
type ControlPlane interface {
    Start(ctx context.Context, cfg *Config) error
    Shutdown(ctx context.Context, timeout time.Duration) error
    RegisterRoute(path string, handler http.Handler) error
    RegisterMiddleware(middleware func(http.Handler) http.Handler) error
}

type Bootstrapper interface {
    LoadConfig() (*Config, error)
    ValidateConfig(cfg *Config) []error
    InitializeDependencies(cfg *Config) (*Dependencies, error)
}

type RequestRouter interface {
    Route(r *http.Request) (http.Handler, error)
    ListRoutes() []RouteInfo
}
```

**Files to create:**
- `internal/controlplane/types.go` — Config, Dependencies types
- `internal/controlplane/app.go` — Main application lifecycle
- `internal/controlplane/router.go` — HTTP routing and middleware
- `internal/controlplane/bootstrap.go` — Startup initialization
- `internal/controlplane/shutdown.go` — Graceful shutdown

**Breaking changes:** Major (main.go refactored significantly)

---

## Extraction Strategy (Per Domain)

1. **Create the package** (`internal/{domain}/`)
2. **Define interfaces** (for all privileged operations)
3. **Extract types** (move structs, config, enums)
4. **Extract functions** (move logic, update imports)
5. **Update root package** (keep only routing/handlers)
6. **Stub main.go** (initialize domains, coordinate lifecycle)
7. **Update tests** (rewrite with interfaces for mocking)
8. **Verify** (compile, run tests, smoke tests)
9. **Commit** (one logical commit per file extracted)

---

## Impact per Domain

### dns/
- **Files changed:** dns.go, dns_desired.go, plus DNS validation scattered
- **Import statements:** ~15 (DNS queries localized)
- **Risk:** Low (isolated from core)
- **Tests:** dns_desired_test.go updates

### runtime/
- **Files changed:** jobs.go (worker logic), main.go (signals), recovery.go
- **Import statements:** ~20 (worker coordination)
- **Risk:** Medium (worker lifecycle touches many paths)
- **Tests:** recovery_test.go, drills in run-recovery-drills.sh

### database/
- **Files changed:** database.go, database_operations.go, main.go (initialization)
- **Import statements:** ~25 (connection pooling used everywhere)
- **Risk:** Medium-high (connection layer is critical)
- **Tests:** database_test.go, database_operations tests

### controlplane/
- **Files changed:** main.go (~60% refactored), controlplane.go, helpers.go
- **Import statements:** ~150+ (routes depend on every domain)
- **Risk:** High (app entry point, touches everything)
- **Tests:** main_test.go, controlplane_test.go, all handler tests

---

## Dependency Resolution Strategy

**Critical constraint:** controlplane/ imports everything, so extract it *last*.

**Extraction sequence:**
1. Extract dns/ (no dependencies on other Phase 4 domains)
2. Extract runtime/ (depends only on database)
3. Extract database/ (depends only on dns, runtime)
4. Extract controlplane/ (depends on all three above + all Phase 2/3 domains)

**Why this order?**
- Each extraction removes complexity from what controlplane/ must coordinate
- Circular dependencies avoided by extracting most-dependent (controlplane) last
- Each domain can be tested independently before controlplane ties them together

---

## Circular Dependency Prevention

**Problem:** controlplane/ imports all domains, all domains need configs (from controlplane).

**Solution:** Config dependency injection via constructor parameters:

```go
// Each domain receives only its config section
type DNSConfig struct {
    Providers map[string]ProviderConfig
    Allowlist []string
}

func dns.New(cfg *DNSConfig) (*DNSResolver, error) { ... }

// controlplane/ loads full config, passes sections to domains
func (cp *ControlPlane) Bootstrap() {
    cfg := cp.loadConfig()
    dnsResolver := dns.New(cfg.DNS)
    dbManager := database.New(cfg.Database)
    rtManager := runtime.New(cfg.Runtime)
    // ... then initialize controlplane with all of them
}
```

This breaks circular dependencies: domains only import config types, not the full Config.

---

## Testing Strategy

### Unit Testing
- Mock each interface (DNSResolver, Worker, DatabaseManager, etc.)
- Test error conditions and recovery paths
- Isolation via dependency injection

### Integration Testing
- Multiple domains together (database + runtime)
- Failure injection (DNS provider down, database connection lost)
- Recovery execution (crash recovery, graceful shutdown)

### Smoke Tests
- Full app startup sequence
- Handler dispatch (all routes callable)
- Graceful shutdown (all subsystems shut down)

---

## Rollback Plan

If extraction breaks something:

1. **Revert commits** (each domain extraction is isolated)
2. **Identify blocker** (circular dependency, missing initialization?)
3. **Fix in situ** (keep domain logic in root, defer extraction)
4. **Document constraint** (ARCHITECTURE_ROADMAP.md notes limitation)

**Recovery time:** 5-10 minutes per rollback (commits are small)

---

## Time Estimates

- dns/ alone: 4-5 commits, 4-6 hours
- dns/ + runtime/: 10-12 commits, 12-16 hours
- dns/ + runtime/ + database/: 18-22 commits, 20-28 hours
- All 4 domains: 80-120 commits, 5-8 days of careful work

---

## Success Criteria

Phase 4 is complete when:

- ✅ All four domains extracted into `internal/{domain}/`
- ✅ No root-package file exceeds 200 lines (excluding templates)
- ✅ All cross-domain dependencies clear (dependency graph acyclic)
- ✅ All handler tests pass (integration tests via mocks)
- ✅ Smoke tests pass (full startup/shutdown cycle)
- ✅ No circular imports (go build succeeds)
- ✅ Backward compatibility maintained (API layer unchanged)

---

## Post-Phase 4 State

After Phase 4 completion, StePanel codebase is fully reorganized:

```
github.com/itchyitchy123/StePanel/
├── internal/
│   ├── audit/           (Phase 2: tamper-evident logging)
│   ├── identity/        (Phase 2: auth, accounts, tokens)
│   ├── jobqueue/        (Phase 2: durable jobs, workers)
│   ├── backup/          (Phase 2: backup jobs, storage, verify)
│   ├── migration/       (Phase 3: cpmove, wordpress, archive)
│   ├── sites/           (Phase 3: site lifecycle, isolation)
│   ├── deployment/      (Phase 3: containers, builders, pipelines)
│   ├── dns/             (Phase 4: resolution, validation, providers)
│   ├── runtime/         (Phase 4: workers, recovery, signals)
│   ├── database/        (Phase 4: pooling, schema, replication)
│   ├── controlplane/    (Phase 4: app, routing, bootstrap)
│   ├── metadata/        (existing: backup metadata index)
│   └── operations/      (existing: distributed locks)
├── main.go              (10-20 lines: entry point)
├── auth_test.go, etc.   (test files, mostly unchanged)
└── docs/
    ├── CLAUDE.md                    (governance standards)
    ├── ARCHITECTURE_ROADMAP.md      (this structure)
    ├── PRODUCTION_READINESS.md      (deployment guide)
    ├── PHASE1_GOVERNANCE.md         (completed)
    ├── PHASE2_EXTRACTION_PLAN.md    (completed or in progress)
    ├── PHASE3_EXTRACTION_PLAN.md    (planned)
    └── PHASE4_EXTRACTION_PLAN.md    (this file)
```

**Result:**
- Main package: <100 lines (entry point only)
- Each domain: 200-500 lines (clear responsibility)
- Test coverage: ~80% via interface mocks
- Onboarding time: <1 hour per domain (vs. full codebase understanding now)
- Refactoring safety: Can change domain internals without breaking others

---

## Related Documents

- [PHASE1_GOVERNANCE.md](PHASE1_GOVERNANCE.md) — Governance standards (CLAUDE.md)
- [PHASE2_EXTRACTION_PLAN.md](PHASE2_EXTRACTION_PLAN.md) — Audit extraction
- [PHASE3_EXTRACTION_PLAN.md](PHASE3_EXTRACTION_PLAN.md) — Sites operations
- [CLAUDE.md](../CLAUDE.md) — Code quality standards
- [ARCHITECTURE_ROADMAP.md](ARCHITECTURE_ROADMAP.md) — Current architecture state
- [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md) — Release criteria
