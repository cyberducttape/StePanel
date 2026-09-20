# StePanel Code Organization

## Current Structure (v0.7.x)

### Root Package (`package main`)
The root package contains the full application runtime and is divided into logical concerns:

#### Core Infrastructure (bootstrap)
- **main.go** - HTTP server startup, flag parsing, initialization order
- **init.go** - Global state initialization
- **config.go** - Configuration loading from environment
- **assets.go** - Embedded web assets (CSS, JS, HTML)
- **helpers.go** - Shared utility functions used across packages
- **version.go** - Version information

#### Domain Logic (core business)
- **Site Management**: sites.go, site_lifecycle.go, routes.go, domains.go, certificates.go, node_proxy.go
- **Backup/Restore**: backups.go, backup_restore.go, backup_schedule.go, backup_retention.go, backup_restore_validation.go
- **Deployments**: deployments.go, git_deploy.go, git_keys.go, release_pipeline.go
- **Operations**: apps.go, workers.go, jobs.go, tasks.go, staging.go, cpmove.go, wordpress.go, services.go, runner.go, retention.go, offsite.go, redis.go, site_usage.go
- **Platform Support**: resources.go, cloud.go, environment.go, nodejs.go, python.go, php.go, ruby.go, composer.go, node_tooling.go
- **Recovery/Migration**: recovery.go, dr.go, importer.go, doctor.go, domain_claims.go, wpress.go
- **Security**: security.go, security_center.go, malware.go, security_headers.go
- **Tenant Isolation**: ssh_access.go, ssh_inventory.go, tenancy.go
- **HTTP API Handlers**: health.go, logs.go, metrics.go
- **Database**: database.go, database_operations.go
- **Audit**: audit.go, audit_events.go, audit_failure_handler.go
- **Authentication**: This is in internal/auth/ (see below)
- **DNS**: dns.go, dns_desired.go
- **Infrastructure**: controlplane.go, htaccess.go, setup.go

### Internal Packages (`internal/`)

#### internal/auth/
**Authentication & Authorization** - Independent from HTTP routing layer
- auth.go - Core Auth struct, login flow, session management
- accounts.go - User account operations
- account_management.go - Account administration endpoints
- api_tokens.go - API token creation, validation, scoping
- api_token_limiter.go - Rate limiting for API requests
- legacy_token_deprecation.go - Token deprecation tracking
- ratelimit.go - Authentication attempt rate limiting

#### internal/backup/
**Backup Metadata** - Type definitions and schema management (not moved from root)
- types.go - BackupEntry, BackupManifest, BackupResult types
- (Other backup operations remain in root package)

#### internal/importer/
**Site Imports from cPanel/WHM**
- Various importer types and operations

#### internal/migration/
**Migration Utilities**
- Migration helpers and state management

#### internal/helper/
**systemd Helper Interface**
- Communication protocol with stepanel-helper service
- Privilege separation boundary

#### internal/http/
**HTTP Utilities**
- Request timeout configuration
- Handler middleware

#### internal/jobs/
**Durable Job Queue**
- Job state management
- Async job execution

#### internal/metadata/
**Metadata Indexing**
- backup_index.go - SQLite-backed backup metadata
- task_index.go - SQLite-backed task execution history

#### internal/session/
**Session State Management**
- Session storage and validation
- CSRF protection

#### internal/startup/
**Startup Phases**
- Phase tracking (Validate → OpenState → Recover → StartAPI → Reconcile → Ready)
- Startup timeline and diagnostics

#### internal/audit/
**Audit Classification**
- audit_level.go - Three-tier audit classification (MUST_AUDIT, SHOULD_AUDIT, BEST_EFFORT)

## Design Principles

### 1. **Root Package = Runtime**
The root package is the **application runtime layer**:
- HTTP server and handlers
- Request routing
- Configuration loading  
- Database initialization
- Privilege separation (API process boundary)

### 2. **Internal Packages = Reusable Components**
Internal packages contain code that could theoretically be used by other applications:
- Authentication (independent of HTTP)
- Session management
- Audit mechanisms
- Job queues
- Metadata indexing

### 3. **Clear Ownership**
Each file has clear responsibility (see file descriptions above). If a file touches root, database, and HTTP handlers - it stays in root. If it's logic that doesn't depend on the HTTP layer - it goes in internal/.

## File Count by Category

| Category | Count | Location |
|----------|-------|----------|
| Core infrastructure | 7 | Root |
| Site management | 6 | Root |
| Backup/restore | 5 | Root |
| Deployments | 4 | Root |
| Operations | 10 | Root |
| Platform support | 8 | Root |
| Recovery/migration | 4 | Root |
| Security | 4 | Root |
| Tenant isolation | 3 | Root |
| HTTP API | 3 | Root |
| Database | 2 | Root |
| Audit | 3 | Root |
| Authentication | 8 | internal/auth/ |
| Infrastructure | 4 | Root |
| Other internal | 40+ | internal/... |
| **Total** | **~130** | **Root + internal** |

## Future Refactoring Roadmap

### Phase 1: Executable Wrapper (Low Risk)
```
Root: package main (HTTP app)
      ↓ imports
cmd/stepanel/main.go: package main (bootstrap only)
      ↓ calls
stepanel.Run(config)
```

### Phase 2: Root → Importable (Medium Risk)
```
Root: package stepanel (HTTP app)
      ↓ imports
internal/auth/ (no circular deps)
internal/backup/ (types + logic)
internal/...
```

### Phase 3: Handler/Logic Separation (High Risk)
```
stepanel/ (App struct + HTTP handlers)
internal/sitesvc/ (site logic - no App struct)
internal/backupsvc/ (backup logic - no App struct)
internal/...
```

## Compilation Verification

✅ Current build: Clean with 131 Go files  
✅ Test coverage: 60%+ in most packages  
✅ Dependencies: No circular imports  
✅ Helper validation: stackplan-helper protocol validated  

## Guidelines for New Code

1. **New domain logic?** → Consider internal/ package
2. **HTTP handlers?** → Root package
3. **Shared utilities?** → helpers.go or internal/
4. **New service layer?** → Proposal for Phase 3 refactoring

## Related Documents

- [REFACTORING_PLAN.md](../REFACTORING_PLAN.md) - Detailed refactoring roadmap
- [PRODUCTION_SCORECARD.md](PRODUCTION_SCORECARD.md) - Code quality assessment
- [HELPER_ARCHITECTURE.md](HELPER_ARCHITECTURE.md) - Privilege separation design
