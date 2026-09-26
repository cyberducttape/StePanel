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

**Phase 1 Audit COMPLETE (All 8/8 CRITICAL files verified):**

1. **importer.go** ✅ — Uses `manager.CreateStaging()` + extract + `manager.ActivateStaged()`
2. **site_lifecycle.go** ✅ — Uses `SiteManager.Delete()` for canonical removal with journal (line 356)
3. **wpress.go** ✅ — Uses bridge `createSiteManagerStaging()` and `discardSiteManagerStaging()`
4. **cpmove.go** ✅ — Uses `manager.CreateStaging()`, `BeginSiteTransaction()`, `manager.ActivateStaged()` (line 183, 193, 228)
5. **backup_restore.go** ✅ — Uses bridge `createSiteManagerStaging()` with recovery journal (line 281)
6. **release_activation_journal.go** ✅ — Uses `manager.DiscardReleaseStaging()` and `manager.RollbackStagedActivation()` (lines 135, 140, 147)
7. **git_deploy.go** ✅ — Uses bridge `a.createSiteReleaseStaging()` (line 606)
8. **release_pipeline.go** ✅ — Uses bridge `a.createSiteReleaseStaging()`, `a.discardSiteReleaseStaging()`, `a.activateReplacingSite()` (lines 168, 120, 150)

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

## Phase 1: Audit & Document (COMPLETE ✅)

✅ **ALL 8 of 8 CRITICAL files verified as architecturally compliant**

Key finding: **The codebase already implements the staging vs. canonical site distinction correctly.** All major restore/deploy paths follow the architectural rule:
- Request → validate → acquire lock
- Get staging from SiteManager
- Perform operation in staging
- Publish via SiteManager (ActivateStaged/ActivateStagedReplacing/DiscardStaging)
- Commit recovery journal

---

## Phase 2: HIGH Priority Audit (NEXT)

**Files to audit (3 files — staging/restore operations):**
- **staging.go** — Generic staging operations
- **node_tooling.go** — Node.js environment  
- **apps.go** — Application management

**Acceptance Criteria:**
- [ ] Verify all HIGH files operate only within staging areas granted by SiteManager
- [ ] Document any direct filepath operations and confirm they're on staging, not canonical
- [ ] Verify lock acquisition for HIGH-priority modifications

---

## Phase 3: Grep Audit & Verification (AFTER Phase 2)

**Acceptance Criteria Verification:**
1. [ ] Run `grep -r "os.Mkdir.*sites" --include="*.go"` — verify no calls outside internal/sites
2. [ ] Run `grep -r "filepath.Join.*sites" --include="*.go"` — verify canonical paths only accessed through manager or bridge
3. [ ] Verify all canonical site mutations have lock acquisition via `acquireSiteMutationLock`
4. [ ] Update V1_PRODUCTION_GATES.md Gate 1 acceptance criteria checkboxes

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
