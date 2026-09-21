# Phase 3 Extraction Plan (v0.9.0)

**Status:** Planning  
**Scope:** Extract site operations domains (migration, sites, deployment)  
**Estimated commits:** 60-80 across all domains  
**Prerequisite:** Phase 2 (audit, jobqueue, backup, identity) must be complete  

## Domain Dependencies

```
migration/          (cpmove, wordpress, archive import)
  ↓
  ├─→ jobqueue/    (dispatch migration jobs)
  ├─→ backup/      (restore backups during migration)
  └─→ database/    (restore databases)

sites/              (site lifecycle, metadata, resources)
  ↓
  ├─→ migration/   (import via migration jobs)
  ├─→ audit/       (log site operations)
  ├─→ identity/    (user quotas, resource limits)
  └─→ database/    (per-site databases)

deployment/         (containers, builds, pipelines)
  ↓
  ├─→ sites/       (site resource allocation)
  ├─→ jobqueue/    (deployment jobs)
  └─→ audit/       (log deployments)
```

## Extraction Order (Must Respect Dependencies)

### Domain 1: `internal/migration/` (MOST INDEPENDENT)

**Current location:** cpmove.go (~618 lines), importer.go (~321 lines), doctor.go (~274 lines), plus helpers in wordpress.go, archive handlers

**Logic to extract:**
- cPanel migration (`cpmove.go` + `cpmove_test.go`)
- Generic archive import (`importer.go` + archive extraction)
- WordPress-specific import (`wordpress.go` WordPress logic)
- Pre-migration analysis (`doctor.go` → Migration Doctor)
- Distributed lock coordination for migrations

**Dependencies:**
- jobqueue (for durable migration jobs)
- backup (to restore site backups)
- database (to restore databases)
- audit (to log migration events)
- Sites system (to create/restore sites)

**New interfaces:**
```go
type Migrator interface {
    ImportCPMove(ctx context.Context, archive io.Reader, user string) (*Migration, error)
    ImportWordPress(ctx context.Context, archive io.Reader, req *WordPressImportRequest) (*Migration, error)
    ImportGeneric(ctx context.Context, archive io.Reader, req *GenericImportRequest) (*Migration, error)
    AnalyzeMigration(ctx context.Context, source *SourceServer) (*MigrationAnalysis, error)
}

type SourceAnalyzer interface {
    AnalyzeServer(ctx context.Context, hostURL string) (*ServerAnalysis, error)
    CheckCompatibility(analysis *ServerAnalysis) (*CompatibilityReport, error)
}
```

**Files to create:**
- `internal/migration/types.go` — CPMove, WordPress, Archive types
- `internal/migration/cpmove.go` — cPanel import logic
- `internal/migration/wordpress.go` — WordPress-specific logic
- `internal/migration/archive.go` — Generic archive extraction
- `internal/migration/analyzer.go` — Pre-migration analysis
- `internal/migration/executor.go` — Job execution handler

**Breaking changes:** None (jobs system provides execution)

**Root package changes:**
- Delete or stub cpmove.go, importer.go (logic moved)
- Update cpmove_test.go imports
- Handlers in main.go delegate to migration.Executor

---

### Domain 2: `internal/sites/` (MEDIUM DEPENDENCIES)

**Current location:** sites.go (~333 lines), site_lifecycle.go (~409 lines), tenancy.go (~80 lines), plus metadata, DNS routing

**Logic to extract:**
- Site lifecycle (create, delete, recover)
- Site metadata and configuration
- Tenant isolation (per-site user quotas, resource limits)
- Site access control (SSH access, FTP, etc.)
- Per-site service management

**Dependencies:**
- audit (log site operations)
- identity (user quotas, resource enforcement)
- jobqueue (site lifecycle jobs)
- database (per-site DB creation/restoration)
- services (restart PHP, webserver for site)

**New interfaces:**
```go
type SiteManager interface {
    Create(ctx context.Context, req *CreateSiteRequest) (*Site, error)
    Delete(ctx context.Context, siteName string) error
    Recover(ctx context.Context, siteName string) error
    GetMetadata(ctx context.Context, siteName string) (*Site, error)
    UpdateResources(ctx context.Context, siteName string, limits *ResourceLimits) error
}

type TenantIsolation interface {
    EnforceQuota(ctx context.Context, site *Site, resource string, usage int64) error
    ValidateAccess(ctx context.Context, user, siteName string) (bool, error)
}
```

**Files to create:**
- `internal/sites/types.go` — Site, SiteMetadata types
- `internal/sites/manager.go` — Site lifecycle operations
- `internal/sites/isolation.go` — Tenant isolation, quotas
- `internal/sites/recovery.go` — Site recovery logic
- `internal/sites/access.go` — Site access control

**Breaking changes:** Moderate (site endpoints need refactoring)

**Root package changes:**
- Update site creation/deletion handlers
- Update site metadata queries
- Delegate to sites.Manager interface

---

### Domain 3: `internal/deployment/` (HIGHER DEPENDENCIES)

**Current location:** deployments.go (~77 lines), container.go (~184 lines), release_pipeline.go (~230 lines), plus builder and registry logic

**Logic to extract:**
- Container image registry validation
- Build execution (Docker, custom builders)
- Deployment pipelines (Git-based, webhook-based)
- Release pipeline automation
- Image verification (digest validation)

**Dependencies:**
- sites (deployment targets site resources)
- jobqueue (deployment jobs)
- audit (log deployments)
- git (Git-based deployments)

**New interfaces:**
```go
type Deployer interface {
    Deploy(ctx context.Context, req *DeployRequest) (*Deployment, error)
    GetStatus(ctx context.Context, deploymentID string) (*DeploymentStatus, error)
    GetHistory(ctx context.Context, site string, limit int) ([]Deployment, error)
}

type BuildExecutor interface {
    Build(ctx context.Context, source *BuildSource) (*BuildResult, error)
    Validate(ctx context.Context, req *BuildRequest) error
}

type RegistryValidator interface {
    ValidateImage(ctx context.Context, imageRef string) ([]string, error)
    IsAllowed(image string) bool
}
```

**Files to create:**
- `internal/deployment/types.go` — Deployment, BuildRequest types
- `internal/deployment/deployer.go` — Deployment orchestration
- `internal/deployment/builder.go` — Build execution
- `internal/deployment/registry.go` — Registry validation
- `internal/deployment/pipeline.go` — Pipeline automation

**Breaking changes:** Moderate (deployment handlers refactored)

**Root package changes:**
- Update deployment endpoints
- Delegate to deployment.Deployer interface
- Registry allowlist moved to deployment package

---

## Extraction Strategy (Per Domain)

Same pattern as Phase 2:

1. **Create the package** (`internal/{domain}/`)
2. **Define interfaces** (from CLAUDE.md patterns)
3. **Extract types** (move structs, enums)
4. **Extract functions** (move logic, update imports)
5. **Update root package** (keep only routing, delegate to domain)
6. **Update tests** (rewrite with interfaces)
7. **Verify** (no breaking changes to callers)
8. **Commit** (one logical commit per file extracted)

---

## Impact per Domain

### migration/
- **Files changed:** cpmove.go, importer.go, doctor.go, handlers in main.go
- **Import statements:** ~30 (migration calls scattered in job handlers)
- **Risk:** Low-medium (well-isolated, self-contained)
- **Tests:** cpmove_test.go, adversarial_test.go need updates

### sites/
- **Files changed:** sites.go, site_lifecycle.go, handlers throughout app
- **Import statements:** ~50+ (site operations called from many places)
- **Risk:** Medium-high (site lifecycle is foundational)
- **Tests:** sites_test.go, site_lifecycle_test.go need rewriting

### deployment/
- **Files changed:** deployments.go, container.go, release_pipeline.go, handlers
- **Import statements:** ~25 (deployment operations less common)
- **Risk:** Medium (deployment logic is isolated but critical)
- **Tests:** deployments_test.go, container_test.go need updates

---

## Dependency Ordering Constraints

**Must extract in order:**
1. ✅ Phase 2 domains (audit, jobqueue, backup, identity)
2. ✅ `internal/migration/` — depends only on Phase 2
3. ⚠️ `internal/sites/` — depends on migration + Phase 2
4. ⚠️ `internal/deployment/` — depends on sites + Phase 2

**Never extract:**
- ❌ Extract sites before migration (creates circular dependency)
- ❌ Extract deployment before sites (no resource allocation)
- ❌ Move types without updating all callers (breaks compilation)

---

## Scope Clarification

### What NOT to Extract in Phase 3

- ❌ `internal/database/` (Phase 4: DDL, replication, pooling)
- ❌ `internal/runtime/` (Phase 4: process management, cgroups)
- ❌ `internal/controlplane/` (Phase 4: startup, routing, config)
- ❌ `internal/dns/` (Phase 4: DNS resolution, provider integration)

These have higher coupling to control plane startup and are deferred to Phase 4.

---

## Rollback Plan

If extraction breaks something:

1. **Revert commits** (fast, extraction creates small logical commits)
2. **Identify breakage** (what changed between before/after)
3. **Fix in situ** (keep domain logic in root, extract later)
4. **Document blocker** (ARCHITECTURE_ROADMAP.md notes constraint)

---

## Time Estimates

- migration/ alone: 8-10 commits, 8-12 hours (well-isolated)
- migration/ + sites/: 20-25 commits, 20-30 hours (higher dependencies)
- All 3 domains: 60-80 commits, 3-5 days of careful work

---

## Next Steps

**Before proceeding:**
- [ ] Verify Phase 2 is complete (audit extracted, tests passing)
- [ ] Review migration/ dependencies — ensure no missing constraints
- [ ] Confirm sites/ ordering — migration must be extracted first
- [ ] Plan Phase 3 commits — one per file extracted

**Proceed with migration/ extraction?**
- Extract migration/ as proof-of-concept for Phase 3
- Use same patterns as Phase 2 audit extraction
- Expect higher complexity due to job execution integration

---

## Related Documents

- [PHASE2_EXTRACTION_PLAN.md](PHASE2_EXTRACTION_PLAN.md) — Phase 2 foundation
- [CLAUDE.md](../CLAUDE.md) — Code quality standards for refactoring
- [ARCHITECTURE_ROADMAP.md](ARCHITECTURE_ROADMAP.md) — Full planned structure
- [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md) — Release schedule
