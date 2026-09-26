# Gate 1: One Lifecycle Authority - Action Plan

**Status:** P0/P1 Blocker - 4 acceptance criteria unchecked  
**Date:** 2026-09-26  
**Priority:** Critical for v1.0.0 release approval

## Problem Statement

Gate 1 requires: ALL site mutations must flow through `SiteManager` (internal/sites/Manager)

**Current Status:** 26 files access site paths directly outside `internal/sites/`. Many are legitimate (reads, staging operations with locks), but several bypass SiteManager and risk inconsistent lifecycle setup (missing PHP-FPM pools, recovery journals, account ownership, resource envelopes, audit trails).

## Architectural Clarification (September 2026)

### Two Distinct Domains

**Canonical Sites** (permanent at `{webRoot}/sites/{siteName}/*`):
- ONLY `SiteManager.Create()`, `Activate()`, `Delete()` touch these
- All mutations acquire locks + record recovery metadata + audit
- All reads check existence + acquire appropriate locks
- No exceptions

**Staging Areas** (temporary workspaces granted by SiteManager):
- Allocated via `SiteManager.CreateStaging()`
- Domain components extract/restore into staging
- Activated via `SiteManager.ActivateStaged()` (atomic rename)
- Discarded via `SiteManager.DiscardStaging()` on failure
- Paths must NEVER be persisted or assumed to exist after operation

## File-by-File Audit

### ✅ COMPLIANT (No action needed)

**Phase 1 Audit Complete (3/8 CRITICAL files):**

1. **importer.go** ✅ — Uses `manager.CreateStaging()` + extract + `manager.ActivateStaged()`
2. **site_lifecycle.go** ✅ — Uses `SiteManager.Delete()` for canonical removal with journal (line 356)
3. **wpress.go** ✅ — Uses bridge `createSiteManagerStaging()` and `discardSiteManagerStaging()`

### 🔄 IN PROGRESS (Under review from other work)

- **release_activation_journal.go** — Recovery journals; may need refactoring
- **release_pipeline.go** — Release operations; timing-dependent

### ⏳ PENDING (Must audit and potentially refactor)

#### CRITICAL - Canonical Site Mutations (5 remaining)

1. **cpmove.go** (463 lines) — cPanel restore  
   - [ ] Audit handleCPanelImportJob for SiteTransaction integration
   - [ ] Verify uses manager staging for canonical operations
   - [ ] Verify atomic activation pattern
   
2. **backup_restore.go** (558 lines) — Backup restoration
   - [ ] Verify all restore staging uses manager.CreateStaging()
   - [ ] Verify atomic activation (ActivateStaged or replacing)
   - [ ] Document any direct path operations
   
3. **git_deploy.go** (782 lines) — Git deployment
   - [ ] Verify release staging via manager
   - [ ] Verify uses ActivateStagedReplacing() or similar
   - [ ] Verify rollback pattern
   
4. **release_activation_journal.go** (342 lines) — Recovery journals
   - [ ] Verify recovery doesn't bypass SiteManager
   - [ ] Verify journal integration with activation
   - [ ] Check prepared/activated/completed state transitions
   
5. **release_pipeline.go** (483 lines) — Release operations
   - [ ] Verify release workflow uses manager staging
   - [ ] Verify atomic release publication
   - [ ] Document any staging cleanup patterns

#### HIGH - Staging/Restore Operations (3 files)
- **staging.go** — Generic staging operations
- **node_tooling.go** — Node.js environment
- **apps.go** — Application management

#### MEDIUM - Lock-Protected Operations (5 files)
- **composer.go** — PHP Composer
- **python.py** — Python runtime
- **runner.go** — Container runner
- **workers.go** — Background workers
- **htaccess.go** — Apache .htaccess

#### LOW - Safe Read Operations (8 files)
- **backups.go**, **health.go**, **logs.go**, **site_usage.go**, **metrics.go**, **accounts.go**, **helpers.go**, **config.go**

## Acceptance Criteria to Complete

```markdown
- [ ] All site creation operations validated to use SiteManager
- [ ] All site modification operations validated to use SiteManager  
- [x] Implemented site deletion operations use SiteManager
- [ ] Grep audit: no `os.Mkdir.*sites` outside manager
- [ ] Grep audit: no direct file operations on site paths outside manager
```

## Next Steps

### Phase 1: Audit & Document (1-2 days)
1. Review each CRITICAL/HIGH file
2. Classify operations: read vs. write, staging vs. canonical
3. Document any legitimate exceptions in code comments
4. Create refactoring work items for violations

### Phase 2: Refactor Critical Paths (3-5 days)
1. Ensure site_lifecycle.go routes all mutations through manager
2. Ensure wpress.go, cpmove.go use manager staging
3. Verify transaction semantics preserved
4. Add integration tests for each workflow

### Phase 3: Verification (1 day)
1. Run grep audit: `grep -r "filepath.Join.*sites" --include="*.go" | grep -v internal/sites`
2. Verify all canonical site mutations have locks
3. Verify all staging operations came from manager.CreateStaging()
4. Update acceptance criteria checkboxes

## Risk

Without this cleanup:
- Two sites created through different workflows are not equivalent
- One path might forget to:
  - Set account ownership
  - Create PHP-FPM pool
  - Generate resource envelope
  - Initialize recovery journal
  - Update desired state
  - Write audit record
- Leads to hard-to-diagnose configuration issues
- Breaks assumption that "all sites have X"

## Evidence of Progress

- **Importer:** ✅ Now correctly uses manager staging
- **Code improvements:** Error handling, config safety, function-call detection
- **Documentation:** Architectural rule clarified
- **Remaining:** 25 files need audit + potential refactoring
