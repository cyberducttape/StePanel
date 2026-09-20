# Go File Organization Refactoring Plan

## Current State
- **131 Go files** in root package (main)
- **Target**: <10 files in root (bootstrap only)
- **Moving**: ~120 files to internal packages

## Proposed Package Structure

```
StePanel/
├── cmd/
│   └── stepanel/
│       ├── main.go (HTTP server bootstrap)
│       └── config.go (configuration loading)
├── internal/
│   ├── auth/
│   │   ├── auth.go
│   │   ├── api_tokens.go
│   │   ├── accounts.go
│   │   └── legacy_token_deprecation.go
│   ├── backup/
│   │   ├── backups.go
│   │   ├── backup_restore.go
│   │   ├── backup_schedule.go
│   │   ├── backup_retention.go
│   │   ├── backup_restore_validation.go
│   │   └── index.go
│   ├── database/
│   │   ├── database_operations.go
│   │   └── databases.go
│   ├── deployment/
│   │   ├── git_deploy.go
│   │   ├── release_pipeline.go
│   │   └── rollback.go
│   ├── httpapi/
│   │   ├── routes.go
│   │   ├── handlers.go
│   │   └── middleware.go
│   ├── operations/
│   │   ├── apps.go
│   │   ├── workers.go
│   │   ├── tasks.go
│   │   ├── staging.go
│   │   ├── cpmove.go
│   │   └── wordpress.go
│   ├── platform/
│   │   ├── resources.go
│   │   ├── cloud.go
│   │   ├── environment.go
│   │   ├── nodejs.go
│   │   ├── python.go
│   │   ├── php.go
│   │   └── ruby.go
│   ├── recovery/
│   │   ├── recovery.go
│   │   └── durable_jobs.go
│   ├── site/
│   │   ├── sites.go
│   │   ├── site_lifecycle.go
│   │   ├── domains.go
│   │   ├── routes.go
│   │   ├── certificates.go
│   │   └── ssl.go
│   ├── tenant/
│   │   ├── ssh_access.go
│   │   └── isolation.go
│   ├── admin/
│   │   └── account_management.go
│   ├── audit/
│   │   ├── audit.go
│   │   ├── audit_events.go
│   │   └── audit_level.go
│   ├── importer/
│   │   (already exists)
│   ├── helper/
│   │   (already exists)
│   ├── http/
│   │   (already exists)
│   ├── jobs/
│   │   (already exists)
│   ├── metadata/
│   │   (already exists)
│   ├── migration/
│   │   (already exists)
│   ├── startup/
│   │   (already exists)
│   └── session/
│       (already exists)
└── assets.go (embedded assets - stays in root)

Root package (only):
├── main.go (HTTP server bootstrap)
├── config.go (configuration)
├── assets.go (embedded web assets)
└── types.go (shared types if needed)
```

## Migration Phases

### Phase 1: Create Package Structure (Parallel)
- [ ] Create internal/auth/ package
- [ ] Create internal/backup/ package
- [ ] Create internal/database/ package
- [ ] Create internal/deployment/ package
- [ ] Create internal/operations/ package
- [ ] Create internal/platform/ package
- [ ] Create internal/recovery/ package
- [ ] Create internal/site/ package
- [ ] Create internal/tenant/ package
- [ ] Create internal/admin/ package
- [ ] Create cmd/stepanel/ package

### Phase 2: Move Files (Group by Package)
- **Auth** (8 files): auth.go, api_tokens.go, accounts.go, account_management.go, api_token_limiter.go, + tests
- **Backup** (8 files): backups.go, backup_restore.go, backup_schedule.go, backup_retention.go, backup_restore_validation.go + tests
- **Database** (2 files): database_operations.go, databases.go
- **Deployment** (3 files): git_deploy.go, release_pipeline.go, + related files
- **Operations** (8 files): apps.go, workers.go, tasks.go, staging.go, cpmove.go, wordpress.go + tests
- **Platform** (10 files): resources.go, cloud.go, environment.go, nodejs.go, python.go, php.go, ruby.go, composer.go, node_tooling.go + tests
- **Recovery** (3 files): recovery.go, durable_jobs.go, recovery_lab.go
- **Site** (8 files): sites.go, site_lifecycle.go, domains.go, routes.go, certificates.go, node_proxy.go + tests
- **Tenant** (2 files): ssh_access.go, isolation logic
- **Audit** (4 files): audit.go, audit_events.go, audit_failure_handler.go + test
- **Admin** (1 file): account_management.go

### Phase 3: Update Imports
- [ ] Update all import statements in moved files
- [ ] Update imports in remaining root files
- [ ] Verify compilation

### Phase 4: Update Main
- [ ] Move bootstrap logic to cmd/stepanel/main.go
- [ ] Keep only essentials in root main.go
- [ ] Update initialization

## Risk Mitigation

1. **Backup**: Commit current state before starting
2. **Verification**: Compile after each phase
3. **Testing**: Run full test suite after completion
4. **Incremental**: Do Phase 1 entirely, then Phase 2-3-4

## Timeline
- Phase 1: 30 minutes (create directories)
- Phase 2: 2 hours (move 90+ files)
- Phase 3: 1.5 hours (update imports, fix compilation)
- Phase 4: 1 hour (finalize structure)
- **Total**: ~5 hours

## Status
- [x] Plan created
- [ ] Phase 1 in progress
- [ ] Phase 2 pending
- [ ] Phase 3 pending
- [ ] Phase 4 pending
