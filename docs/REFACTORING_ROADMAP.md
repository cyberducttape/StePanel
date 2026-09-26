# StePanel Refactoring Roadmap

**Goal:** Transform StePanel from a ~150-file root package into a well-organized, testable, maintainable codebase with clear domain boundaries.

**Current State:** 150 root files, ~2.5MB. Proven domains (sites, jobs, audit) demonstrate value of clear boundaries.

**Strategy:** Incremental extraction as features are modified. Never disruptive rewrites.  
**Timeline:** 6-9 weeks (phases 1-4)  
**Status:** Phase 0 ✅ Complete | Phase 1-4 (Planned)

---

## Phase 0: Strategic Foundation (2026-09-26) ✅

**Status:** Complete | **Timeline:** 1 day

### What Phase 0 Accomplished

Validated the refactoring need and created the strategic plan:

1. **Gate 1 Verification** (One Lifecycle Authority)
   - Audited all 8 CRITICAL files + 3 HIGH priority files
   - Verified 11/11 files architecturally compliant
   - Confirmed staging vs. canonical site distinction is properly enforced
   - **Finding:** Architecture is sound; refactoring is purely organizational

2. **Root Package Analysis**
   - Confirmed 150 Go files in root package
   - Identified tight coupling between domains (auth, accounts, sites, backup, deploy)
   - Created dependency map showing extraction challenges

3. **Strategic Documentation**
   - Updated CLAUDE.md with incremental refactoring strategy
   - Identified "do NOT do big rewrites" principle
   - Created Phase 1-4 roadmap with clear success criteria

4. **Foundation Work**
   - Created `internal/accounts/store.go` (domain logic extracted, ~1200 lines)
   - Demonstrated feasible extraction pattern
   - Identified bridge function pattern for dependencies

### Phase 0 Key Decision

**Incremental extraction over Big Rewrite:** Extract domains as they're modified, not all at once. This reduces risk and maintains stability while achieving the refactoring goal.

### Phase 0 Output

```
✅ CLAUDE.md               — Updated with refactoring strategy
✅ internal/accounts/      — Started (store.go created)
✅ REFACTORING_ROADMAP.md  — This document (updated with realistic phasing)
✅ Gate 1 Audit Complete   — Confirms architecture is sound
```

---

## Phase 1: Extract Core Domains (2-3 weeks)

**Status:** Not started | **Timeline:** 2-3 weeks | **Risk:** Low

### What Phase 1 Extracts

Two foundation domains that unblock everything else:

1. **internal/accounts/** (from root/accounts.go)
   - Extract AccountStore, HostingAccount, HostingPlan types (~800 lines)
   - Create accounts.Service interface
   - Move HTTP handlers to internal/http/accounts.go
   - Result: accounts.go reduced from 1330 to <200 lines

2. **internal/security/** (from root/auth.go)
   - Extract Auth type, login/logout logic
   - Extract CSRF, scope checking, capabilities
   - Create auth.Service interface
   - Move HTTP handlers to internal/http/auth.go
   - Result: auth.go reduced from ~500 to <100 lines

### Success Criteria

- ✅ All tests pass
- ✅ root/accounts.go reduced to <200 lines (HTTP handlers only)
- ✅ root/auth.go reduced to <100 lines (HTTP handlers only)
- ✅ Zero circular dependencies
- ✅ HTTP handlers in internal/http/ can be understood independently

### Phase 1 Key Decision: Bridge Functions

During extraction, have root provide dependency injection:

```go
// root/main.go
func setupAccounts(cfg Config) *accounts.Service {
    store, _ := accounts.NewStore(cfg.AccountPath)
    return &accounts.Service{
        Store: store,
        HashPassword: hashPassword,  // from root/helpers.go
    }
}
```

This allows incremental extraction without requiring helpers to move immediately.

### Phase 1 Output

```
internal/accounts/
  store.go           — Account storage (already created!)
  service.go         — AccountService interface (NEW)
  
internal/security/
  auth.go            — Auth type and login/logout
  tokens.go          — API token validation
  
internal/http/
  auth.go            — Login/logout handlers (NEW)
  accounts.go        — Account CRUD handlers (NEW)
```

### Why Start with Accounts & Security

- ✅ Accounts/store.go already created (foundation laid)
- ✅ Auth depends on Accounts, so extraction order is clear
- ✅ Low risk: changes don't cascade to other domains
- ✅ Unblocks Phase 2 (deploy, backup, database all depend on accounts)

---

## Phase 2: Core Domains (v0.8.0) — IN PROGRESS

**Status:** 1 of 4 domains extracted | **Timeline:** 6-8 weeks

### What Phase 2 Extracts

Four independent domain layers extracted from root package:

| Domain | Current | Extracted | Status | LOC |
|--------|---------|-----------|--------|-----|
| **audit** | audit.go (446) | internal/audit/ | ✅ Complete | 387 |
| **identity** | auth.go (736) + accounts.go (1,319) | internal/identity/ | ⏳ Pending | 2,055 |
| **jobqueue** | jobs.go (1,687) | internal/jobqueue/ | ⏳ Pending | 1,687 |
| **backup** | backups.go (836) + helpers.go | internal/backup/ | ⏳ Pending | 836 |

### Phase 2 Progress

**Completed:**
- ✅ `internal/audit/` extraction
  - Tamper-evident HMAC-signed logging
  - HTTP API handlers (Events endpoint)
  - Module-level default logger + backward-compatible wrappers
  - Commit: 4e511b0

**Pending:**
- ⏳ `internal/identity/` (auth, accounts, tokens, quotas)
- ⏳ `internal/jobqueue/` (durable jobs, workers, persistence)
- ⏳ `internal/backup/` (backup jobs, storage backends, verification)

### Phase 2 Pattern Established

Each domain extraction follows:
1. Create `internal/{domain}/` package
2. Define Logger/Manager/Executor interface
3. Move types and logic from root package
4. Create module.go with factory function and wrapper functions
5. Update root package imports
6. Maintain backward compatibility
7. Commit with clear message

### Expected Phase 2 Output

```
internal/audit/           ✅ Complete (4 files)
internal/identity/        ⏳ auth, accounts, quota mgmt (5 files)
internal/jobqueue/        ⏳ jobs, workers, persistence (5 files)
internal/backup/          ⏳ backup creation, storage, verify (5 files)
Updated imports           — config.go, dr.go, main.go, handlers
Backward-compatible API   — wrapper functions in root package
```

---

## Phase 3: Site Operations (v0.9.0) — PLANNED

**Status:** Plan ready | **Timeline:** 8-10 weeks | **Prerequisite:** Phase 2 complete

### What Phase 3 Extracts

Three interdependent domains handling site management and deployments:

| Domain | Current | Extracted | LOC | Dependencies |
|--------|---------|-----------|-----|--------------|
| **migration** | cpmove (618) + importer (321) + doctor (274) | internal/migration/ | 1,213 | jobqueue, backup |
| **sites** | sites (333) + site_lifecycle (409) + tenancy (80) | internal/sites/ | 822 | audit, identity, migration |
| **deployment** | deployments (77) + container (184) + pipeline (230) | internal/deployment/ | 491 | sites, jobqueue |

### Phase 3 Extraction Order (CRITICAL)

**Must extract in this order:**
1. `internal/migration/` first — CPMove, WordPress, archive imports (most independent)
2. `internal/sites/` second — Site lifecycle (depends on migration)
3. `internal/deployment/` third — Containers, builds (depends on sites)

**Why this order?** Sites creation can be triggered by migrations, and deployments target site resources. Extraction order respects these dependencies.

### Phase 3 Interfaces

**Migrator** — Import from external sources
```go
type Migrator interface {
    ImportCPMove(ctx context.Context, archive io.Reader) (*Migration, error)
    ImportWordPress(ctx context.Context, archive io.Reader, req *WordPressImportRequest) (*Migration, error)
    ImportGeneric(ctx context.Context, archive io.Reader, req *GenericImportRequest) (*Migration, error)
}
```

**SiteManager** — Site lifecycle and metadata
```go
type SiteManager interface {
    Create(ctx context.Context, req *CreateSiteRequest) error
    Delete(ctx context.Context, name string) error
    UpdateResources(ctx context.Context, name string, limits *ResourceLimits) error
}
```

**Deployer** — Deployment orchestration
```go
type Deployer interface {
    Deploy(ctx context.Context, req *DeployRequest) (*Deployment, error)
    GetStatus(ctx context.Context, deploymentID string) (*DeploymentStatus, error)
}
```

---

## Phase 4: Infrastructure Foundation (v1.0.0+) — PLANNED

**Status:** Plan ready | **Timeline:** 10-12 weeks | **Prerequisite:** Phases 2-3 complete

### What Phase 4 Extracts

Four foundational infrastructure domains (extracted last because everything depends on them):

| Domain | Current | Extracted | LOC | Scope |
|--------|---------|-----------|-----|-------|
| **dns** | dns.go (69) + dns_desired (134) | internal/dns/ | 203 | Resolution, validation, providers |
| **runtime** | jobs worker logic + recovery (449) | internal/runtime/ | 449 | Workers, recovery, signals |
| **database** | database (106) + db_operations (445) | internal/database/ | 551 | Pooling, schema, replication |
| **controlplane** | main.go startup + controlplane (528) | internal/controlplane/ | 528 | App, routing, bootstrap |

### Phase 4 Extraction Order (CRITICAL)

**Must extract in this order (to avoid circular dependencies):**
1. `internal/dns/` — Lowest coupling (only depends on audit)
2. `internal/runtime/` — Worker management (depends on database)
3. `internal/database/` — Connection pooling (depends on audit, runtime)
4. `internal/controlplane/` — App entry point (depends on all three above)

**Why this order?** Each extraction removes coupling from controlplane. Extract most-dependent last.

### Phase 4 Key Innovation: Config Dependency Injection

**Problem:** controlplane imports all domains, creating circular dependencies.

**Solution:** Config passed by section, not as whole:

```go
// Each domain gets only its config
type DNSConfig struct { Providers map[string]ProviderConfig }
type RuntimeConfig struct { Workers int; RecoveryEnabled bool }
type DatabaseConfig struct { Host string; Pool int }

// No circular dependency: domains don't import app-level Config
dns.New(cfg.DNS)
runtime.New(cfg.Runtime)
database.New(cfg.Database)
```

### Phase 4 Interfaces

**DNSResolver** — DNS operations
```go
type DNSResolver interface {
    Resolve(ctx context.Context, domain string) ([]net.IP, error)
    ValidateZone(ctx context.Context, zone string) error
}
```

**Worker** — Process and job management
```go
type Worker interface {
    Start(ctx context.Context, handlers map[string]JobHandler) error
    Shutdown(ctx context.Context, timeout time.Duration) error
    Health() WorkerHealth
}
```

**DatabaseManager** — Connection and schema management
```go
type DatabaseManager interface {
    OpenConnection(ctx context.Context, cfg *DatabaseConfig) (Connection, error)
    MigrateSchema(ctx context.Context) error
    ValidateConnectivity(ctx context.Context) error
}
```

**ControlPlane** — App lifecycle
```go
type ControlPlane interface {
    Start(ctx context.Context, cfg *Config) error
    Shutdown(ctx context.Context, timeout time.Duration) error
    RegisterRoute(path string, handler http.Handler) error
}
```

---

## Complete Refactoring Timeline

```
┌─ Phase 1: Governance (v0.7.0)        ✅ COMPLETE
│  └─ Standards, roadmap, initial fixes
│
├─ Phase 2: Core Domains (v0.8.0)      ⏳ IN PROGRESS (1/4)
│  ├─ audit/        ✅ EXTRACTED
│  ├─ identity/     ⏳ PENDING
│  ├─ jobqueue/     ⏳ PENDING
│  └─ backup/       ⏳ PENDING
│
├─ Phase 3: Site Operations (v0.9.0)   📋 PLANNED
│  ├─ migration/    (cpmove, wordpress, archive)
│  ├─ sites/        (lifecycle, isolation)
│  └─ deployment/   (containers, builders)
│
└─ Phase 4: Infrastructure (v1.0.0+)   📋 PLANNED
   ├─ dns/         (resolution, validation)
   ├─ runtime/     (workers, recovery)
   ├─ database/    (pooling, schema)
   └─ controlplane/ (app, routing)
```

---

## Codebase Transformation

### Before Refactoring (Now)

```
36,000 lines in root package
├── jobs.go           (1,687 lines) — Job system
├── accounts.go       (1,319 lines) — Identity
├── main.go           (1,323 lines) — App startup + handlers
├── backups.go        (836 lines)   — Backup logic
├── cloud.go          (763 lines)   — S3/B2 backends
├── auth.go           (736 lines)   — Authentication
├── git_deploy.go     (970 lines)   — Git deployments
├── cpmove.go         (618 lines)   — CPMove imports
├── wpress.go         (794 lines)   — WordPress logic
├── ...
└── 50+ more files, each handling cross-cutting concerns
    (routes, DNS, tasks, services, containers, etc.)
```

**Problems:**
- No clear boundaries between subsystems
- Testing requires loading entire codebase
- Refactoring one subsystem affects many others
- Onboarding requires understanding all 36K lines
- Circular dependencies between subsystems

### After Refactoring (Phase 4 Complete)

```
✅ Clean layered architecture
├── internal/
│   ├── audit/        (387 lines)  — Phase 2
│   ├── identity/     (2,055 lines) — Phase 2
│   ├── jobqueue/     (1,687 lines) — Phase 2
│   ├── backup/       (836 lines)   — Phase 2
│   ├── migration/    (1,213 lines) — Phase 3
│   ├── sites/        (822 lines)   — Phase 3
│   ├── deployment/   (491 lines)   — Phase 3
│   ├── dns/          (203 lines)   — Phase 4
│   ├── runtime/      (449 lines)   — Phase 4
│   ├── database/     (551 lines)   — Phase 4
│   └── controlplane/ (528 lines)   — Phase 4
├── main.go           (20 lines!)   — Entry point only
├── handlers/         (deprecated)  — Moved to domain packages
└── internal/metadata/             — Existing (backup index)
```

**Benefits:**
- ✅ Clear domain boundaries (11 independent packages)
- ✅ Testable in isolation (mock interfaces)
- ✅ Refactoring safe (change internals without affecting others)
- ✅ Onboarding fast (understand one domain at a time)
- ✅ No circular dependencies (dependency graph acyclic)
- ✅ Main package: 20 lines (pure entry point)

---

## Success Metrics

After all four phases complete:

| Metric | Before | Target | Status |
|--------|--------|--------|--------|
| Root package size | 36,000 lines | <100 lines | ⏳ Phase 4 |
| Largest file | main.go (1,323 lines) | <200 lines per domain | ⏳ Phase 4 |
| Test coverage | ~60% | ~80% (via mocks) | ✅ Phase 1 laid groundwork |
| Circular imports | Yes (spaghetti) | No (acyclic graph) | ⏳ Phase 4 |
| Onboarding time | 4+ hours | <1 hour per domain | ⏳ Phase 4 |
| Refactoring friction | High | Low | ⏳ Phase 2+ |

---

## How to Read This Roadmap

**For new developers:**
1. Read [CLAUDE.md](../CLAUDE.md) — Code quality standards
2. Read [ARCHITECTURE_ROADMAP.md](ARCHITECTURE_ROADMAP.md) — Planned structure
3. Pick one domain, understand its interface
4. Code within that domain following CLAUDE.md patterns

**For reviewers:**
1. Check against code quality checklist in CLAUDE.md
2. Verify domain boundaries respected (no cross-domain imports except interfaces)
3. Confirm error handling and progress reporting follow Tier 1 standards
4. Test via interface mocks where applicable

**For refactoring work:**
1. Follow phase sequence (1 → 2 → 3 → 4)
2. Follow domain extraction order within each phase (documented in each plan)
3. Each domain: create package → define interface → move code → update imports
4. Maintain backward compatibility via wrapper functions
5. Commit each file extraction separately for reviewability

---

## Phase Progression Criteria

**Phase 2 Ready When:**
- Phase 1 complete ✅
- audit/ extraction working and tested ✅

**Phase 3 Ready When:**
- Phase 2 complete (all 4 domains extracted)
- Audit extraction pattern validated

**Phase 4 Ready When:**
- Phase 3 complete (all 3 site domains extracted)
- No circular dependencies in Phases 2-3

**v1.0.0 Release When:**
- All Phase 4 domains extracted
- All tests passing (unit + integration + smoke)
- No circular imports (go build succeeds)
- All Tier 1 code quality standards met

---

## Related Documents

- [CLAUDE.md](../CLAUDE.md) — Code quality standards (Phase 1 output)
- [ARCHITECTURE_ROADMAP.md](ARCHITECTURE_ROADMAP.md) — Package structure vision
- [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md) — Deployment guide
- [PHASE2_EXTRACTION_PLAN.md](PHASE2_EXTRACTION_PLAN.md) — Audit extraction + 3 pending domains
- [PHASE3_EXTRACTION_PLAN.md](PHASE3_EXTRACTION_PLAN.md) — Site operations domains
- [PHASE4_EXTRACTION_PLAN.md](PHASE4_EXTRACTION_PLAN.md) — Infrastructure foundation
- [CHANGELOG.md](../CHANGELOG.md) — Implementation progress

---

**Questions?** See specific phase plans or ARCHITECTURE_ROADMAP.md for details.

**Contributing?** Follow CLAUDE.md standards + pick a phase and domain to work on.

**Tracking progress?** Check CHANGELOG.md for completed extractions by version.
