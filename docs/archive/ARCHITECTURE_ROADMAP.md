# StePanel Architecture Roadmap

**Last Updated:** 2026-09-20  
**Focus:** Code quality standardization and domain organization

## Current State: Two Code Quality Tiers

### Tier 1: Sophisticated Systems (Best Practices)

StePanel's mature subsystems demonstrate rigorous engineering:

- **Durable Job System**: SQLite-backed, transaction journals, recovery semantics
- **Lease Management**: Distributed locks, timeout handling, crash resilience
- **Audit Logging**: Tamper-evident HMAC-signed events, segregated storage
- **Backup Verification**: Published signatures, integrity chains, atomic publication
- **Account Isolation**: Process boundaries, resource quotas (memory/CPU/IO), task limits
- **TOTP Security**: Replay prevention, time-window validation, token lifecycle
- **Configuration**: Atomic writes, permission preservation, rollback capability
- **Desired State**: Reconciliation patterns, eventual consistency, idempotency

### Tier 2: Newer Systems (Quality Gaps)

Newer code (e.g., `internal/importer/executor.go` pre-v0.7.0 fixes) exhibited patterns:

- ❌ Ignored filesystem operation errors
- ❌ Brittle text-based configuration replacement
- ❌ Hardcoded example domains in output
- ❌ Non-monotonic progress calculations
- ❌ Stubs replacing unimplemented critical features
- ❌ Unconditional permission resets
- ❌ Redundant downloads
- ❌ Synchronous blocking on gigabyte transfers

## v0.7 Priority: Quality Parity, Not Feature Count

**Goal:** Bring every subsystem to Tier 1 standards.

**Not:** Add more features at lower quality.

This is the strategic inflection point: a codebase of 36,000 lines becomes unmaintainable if half is Tier 1 and half is Tier 2.

## Package Organization Challenge

### Current State (Root Package Bloat)

The main package still contains ~36,000 lines across:

```
jobs.go              1,687 lines    (job lifecycle, workers, persistence)
accounts.go          1,319 lines    (identity, billing, quotas)
main.go              1,318 lines    (startup, lifecycle, routing)
git_deploy.go          970 lines    (Git-based deployments)
backups.go             836 lines    (backup jobs, verification, exports)
wpress.go              794 lines    (WordPress-specific logic)
cloud.go               763 lines    (S3/B2 integration, offsite backups)
auth.go                736 lines    (authentication, tokens, TOTP)
```

Plus: `cpmove.go`, `sites.go`, `workflows.go`, `helpers.go`, database schema migrations, and more.

### Structural Debt

Large root packages create:

- **Unclear domain boundaries**: What belongs to "job management" vs. "deployment"?
- **Coupling risk**: Refactoring `jobs.go` requires understanding `backups.go`, `wpress.go`, `auth.go`
- **Testing difficulty**: Can't test job system without importing account quotas
- **Onboarding friction**: New developers must understand entire main package to change one subsystem

### The Antipattern to Avoid

**Do not:** Move files to folders by accident (e.g., `internal/jobs/`, `internal/auth/`) without reorganizing dependencies.

This creates: `import "github.com/cyberducttape/StePanel/internal/jobs"` but the logic still depends on `package main` types.

## Recommended Domain Architecture

Reorganize around **business domains** with **explicit interfaces** at boundaries:

### Proposed Internal Packages

```
internal/controlplane/
  ├── types.go          (Config, Database, Site, Account types)
  ├── lifecycle.go      (startup, shutdown, signal handling)
  └── router.go         (HTTP routing, middleware)

internal/identity/
  ├── auth.go           (TOTP, token validation, session mgmt)
  ├── account.go        (account creation, quotas, billing)
  ├── token.go          (rate limiting, legacy deprecation)
  └── audit.go          (admin actions, compliance logging)

internal/tenant/
  ├── site.go           (site lifecycle, isolation, metadata)
  ├── namespace.go      (DNS, routing, certificate management)
  ├── resource.go       (resource quotas, slices, enforcement)
  └── recovery.go       (recovery mode, privilege escalation)

internal/jobqueue/
  ├── worker.go         (durable job execution, recovery)
  ├── persistence.go    (SQLite storage, transaction journals)
  ├── leases.go         (distributed locks, timeout handling)
  └── schedule.go       (cron, retry policies, deadlines)

internal/migration/
  ├── cpmove.go         (cPanel migrations)
  ├── wordpress.go      (WordPress import)
  ├── archive.go        (generic archive import)
  ├── analyzer.go       (pre-flight analysis)
  └── importer.go       (execution engine)

internal/sites/
  ├── php.go            (PHP runtime, version management)
  ├── git.go            (Git deployment, webhook handling)
  ├── tasks.go          (scheduled commands, systemd)
  ├── database.go       (MySQL/PostgreSQL lifecycle)
  ├── mail.go           (Exim/Postfix management)
  └── webserver.go      (Caddy/nginx configuration)

internal/backup/
  ├── creator.go        (backup job execution)
  ├── storage.go        (S3, B2, local storage)
  ├── verify.go         (integrity verification, signing)
  ├── export.go         (export API, download handling)
  └── recovery.go       (restore operations, rollback)

internal/deployment/
  ├── container.go      (registry validation, image pulling)
  ├── build.go          (builder interaction, image build)
  ├── workflow.go       (deployment pipelines)
  └── registry.go       (allowlist enforcement, security)

internal/database/
  ├── schema.go         (migrations, DDL management)
  ├── replication.go    (read replicas, HA setup)
  ├── pooling.go        (connection management)
  └── monitoring.go     (slow query detection, metrics)

internal/runtime/
  ├── processes.go      (process management, signals)
  ├── resources.go      (cgroups, memory/CPU enforcement)
  ├── monitoring.go     (real-time metrics, alerts)
  └── recovery.go       (automatic restart, health checks)

internal/dns/
  ├── resolver.go       (DNS queries, caching)
  ├── validator.go      (zone validation, DNSSEC)
  └── provider.go       (Route53, Cloudflare, etc.)

internal/audit/
  ├── logger.go         (tamper-evident event log)
  ├── serializer.go     (HMAC signing, verification)
  └── export.go         (audit log export, compliance)
```

### Interface-Driven Boundaries

Define interfaces for cross-domain operations:

```go
// Privileged operations that tests can mock
type SiteManager interface {
    Create(ctx context.Context, req *CreateSiteRequest) error
    Delete(ctx context.Context, name string) error
    UpdateResources(ctx context.Context, name string, limits *ResourceLimits) error
}

type DatabaseManager interface {
    CreateDatabase(ctx context.Context, site string, spec *DatabaseSpec) error
    RestoreDatabase(ctx context.Context, site string, dump io.Reader) error
    ValidateConnectivity(ctx context.Context, site string) error
}

type ServiceManager interface {
    RestartPHP(ctx context.Context, site string) error
    RestartWebserver(ctx context.Context, site string) error
    StopAllServices(ctx context.Context, site string) error
}

type ArchiveFetcher interface {
    Fetch(ctx context.Context, url string, maxSize int64) (io.ReadCloser, error)
    Validate(ctx context.Context, archive io.Reader, format string) (*ArchiveInfo, error)
}
```

## Benefits of Domain Organization

✅ **Testability**: Mock interfaces, test in isolation, avoid side effects  
✅ **Destructive testing**: Inject failures at boundaries, verify rollback behavior  
✅ **Dependency clarity**: Import graph shows domain dependencies, not spaghetti  
✅ **Onboarding**: New developer understands one domain at a time  
✅ **Refactoring safety**: Change internals of `internal/backup/` without affecting `internal/sites/`  
✅ **Feature isolation**: New features don't spread across 8 different root-package files  

## Implementation Strategy

### Phase 1: Governance (v0.7.0)

- ✅ Establish quality standards (completed in security review)
- ✅ Document architecture roadmap (this file)
- ⏳ Code review checklist enforcing Tier 1 patterns

### Phase 2: Core Domains (v0.8.0)

Extract most independent domains first:

1. `internal/identity/` (auth.go, accounts.go, token logic)
2. `internal/audit/` (audit logging, tamper prevention)
3. `internal/jobqueue/` (jobs.go, worker logic)
4. `internal/backup/` (backups.go, storage, verification)

### Phase 3: Site Operations (v0.9.0)

Extract site-management domains:

1. `internal/sites/` (site lifecycle, metadata)
2. `internal/deployment/` (containers, builders, pipelines)
3. `internal/migration/` (all importer types)

### Phase 4: Cross-Cutting (v1.0.0+)

Extract infrastructure domains:

1. `internal/runtime/` (process management, cgroups)
2. `internal/database/` (schema, replication, pooling)
3. `internal/controlplane/` (main startup, routing, config)

## Success Metrics

- ✅ No root-package file exceeds 500 lines
- ✅ Internal packages have <20 external dependencies
- ✅ All destructive operations have failure injection tests
- ✅ Domain interfaces provide 80%+ test coverage via mocks

## Related Documents

- [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) — Current status and roadmap
- [SECURITY.md](./SECURITY.md) — Security boundaries and threat model
- [CONTRIBUTING.md](../CONTRIBUTING.md) — Code standards and review expectations
