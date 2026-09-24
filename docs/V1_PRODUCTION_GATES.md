# StePanel v1.0.0 Production Readiness Gates

**Status:** Specification for production release criteria  
**Target:** Ready to run 100+ production WordPress/PHP customer sites  
**Approach:** Complete existing architectural contracts, add robustness testing

## Executive Summary

v1.0.0 is NOT about adding new features. It's about making existing architecture bulletproof.

The design is sound. The implementation is 85% there. The remaining work is:
- Finishing what's already designed
- Testing failure paths rigorously
- Making recovery guarantees explicit

---

## Gate 1: One Lifecycle Authority

**Requirement:** ALL site mutations must flow through `SiteManager` (internal/sites/Manager)

**What this means:**
```
create → SiteManager.Create()
import → SiteManager.ImportArchive()
clone  → SiteManager.Clone()
restore → SiteManager.Restore()
update → SiteManager.UpdateConfiguration()
delete → SiteManager.Delete()
```

No exceptions. No direct filesystem calls. No helper scripts that bypass the manager.

**Current Status:**
- ✅ Manager interface defined (manager_full.go)
- ⏳ Integration into HTTP handlers and job workers
- ⏳ Routing git clone/restore operations through manager
- ⏳ Routing cpmove import through manager
- ⏳ Routing staging operations through manager

**Acceptance Criteria:**
- [ ] All site creation operations validated to use SiteManager
- [ ] All site modification operations validated to use SiteManager
- [ ] All site deletion operations validated to use SiteManager
- [ ] Grep audit: no `os.Mkdir.*sites` outside manager
- [ ] Grep audit: no direct file operations on site paths outside manager

---

## Gate 2: Cross-Process Lock Enforcement

**Requirement:** Panel and worker NEVER independently mutate the same resource

**What this means:**
- Distributed locks (DBLocks) must be acquired BEFORE any site mutation
- Held for the ENTIRE operation duration
- Test suite must include adversarial concurrent scenarios

**Current Status:**
- ✅ DBLocks implementation (SQLite-based)
- ⏳ Wired into site termination job
- ⏳ Wired into git deployments
- ⏳ Wired into backup/restore operations
- ⏳ Wired into resource updates

**Adversarial Test Scenarios:**
```
Test 1: restore + delete simultaneously
  → One must wait or one must rollback

Test 2: deploy + restore simultaneously
  → Files must not be mixed

Test 3: route update + termination simultaneously
  → Route state must be consistent

Test 4: resource update + suspension simultaneously
  → No partial application

Test 5: backup + filesystem restore simultaneously
  → Backup must see consistent state
```

**Acceptance Criteria:**
- [ ] Distributed lock acquired before each site mutation
- [ ] Lock held until operation completes or rolls back
- [ ] Adversarial tests pass (5 scenarios above)
- [ ] No race condition bugs after concurrent operations
- [ ] Failed operations leave system in known good state

---

## Gate 3: Capability Reporting is Brutally Accurate

**Requirement:** Capabilities reflect usable workflows, not binary existence

**What this means:**
```
WRONG:
  "database_restore": {
    "available": true,
    "reason": "mysql binary found"
  }

RIGHT:
  "database_restore": {
    "available": false,
    "mode": "manual",
    "reason": "Archive import locates database dump; operator restores manually. Automated restoration planned for v0.8."
  }
```

**Current Status:**
- ✅ CapabilityMode enum implemented
- ✅ Database restoration reports as "manual"
- ✅ All capabilities updated to report mode
- ⏳ Audit: verify NO "available: true" claims intent rather than actual capability

**Acceptance Criteria:**
- [ ] All capabilities report mode (not just available/unavailable)
- [ ] Mode accurately reflects operation readiness
- [ ] No "available: true" for operations not yet implemented
- [ ] Documentation matches capabilities (capabilities must verify docs are truthful)

---

## Gate 4: Automated Archive Database Restoration

**Requirement:** Database restoration is transactional end-to-end

**What this means:**
```
1. Create database (rollback point)
2. Restore SQL dump
3. Validate table structure
4. Rewrite configuration with new credentials
5. Health check (verify connectivity)
6. Commit state

Failure at any step:
  - Drop created database
  - Restore previous site files
  - Rollback application configuration
  - Operator sees "restoration failed" not "partial state"
```

**Current Status:**
- ⏳ Archive import checks disk space
- ⏳ Recovery journal initialized
- ⏳ But database restoration is manual (v0.7.0)

**Implementation Plan:**
1. Extend ArchiveImportRequest to accept DB credentials
2. Add RestoreDatabaseRequest to Manager interface
3. Implement transactional restoration in handleArchiveImportJob
4. Add rollback on any restoration failure
5. Update capabilities to reflect "available" once complete

**Acceptance Criteria:**
- [ ] Database restoration is transactional
- [ ] Failure rolls back all changes
- [ ] Config is rewritten with actual credentials
- [ ] Health check passes before success
- [ ] Capability reports mode: "available" (once implemented)

---

## Gate 5: Failure Injection Testing

**Requirement:** StePanel survives and recovers from failure at every step

**What this means:**
```
For each critical operation (backup, restore, deploy, terminate):
  Test failure at:
    - Job start
    - 25% through
    - 50% through
    - 75% through
    - After success but before commit
    
  Then restart worker and verify:
    - Operation completes OR rolls back (never leaves half-state)
    - Operator can see what happened
    - Retry works
```

**Test Framework:**
```go
// $STEPANEL_FAIL_AT="backup:50" to inject failures
// $STEPANEL_RESTART_AT="backup:75" to test recovery

func shouldFailAt(operation string, progress int) bool {
    failAt := os.Getenv("STEPANEL_FAIL_AT")
    if failAt == "" { return false }
    
    parts := strings.Split(failAt, ":")
    if parts[0] != operation { return false }
    
    reqProgress, _ := strconv.Atoi(parts[1])
    return progress >= reqProgress
}
```

**Critical Operations to Test:**
1. Backup (kill at: init, archive creation, compression, upload, verify)
2. Restore (kill at: download, extract, config update, health check)
3. Deploy (kill at: clone, build, symlink switch, health check)
4. Terminate (kill at: backup, cleanup, state removal)
5. Account suspension (kill at: service stop, config update, state save)

**Acceptance Criteria:**
- [ ] Failure injection test framework implemented
- [ ] All 5 operations pass failure tests
- [ ] No mysterious half-states discovered
- [ ] Recovery is deterministic

---

## v1.0.0 Release Checklist

### Architecture
- [ ] Gate 1: One Lifecycle Authority - ALL mutations through SiteManager
- [ ] Gate 2: Cross-Process Locks - Distributed locks enforced
- [ ] Gate 3: Accurate Capabilities - No "available: true" for unimplemented
- [ ] Gate 4: Automated DB Restoration - Transactional end-to-end
- [ ] Gate 5: Failure Injection - Survives failure at every step

### Testing
- [ ] Adversarial concurrency tests (5 scenarios)
- [ ] Failure injection tests (all critical paths)
- [ ] Disposable host integration tests (real OS, full workflow)
- [ ] Production load simulation (concurrent operations)

### Documentation
- [ ] v1.0.0 Production Gates (this doc)
- [ ] Architecture diagrams updated
- [ ] Capability documentation accurate
- [ ] Recovery procedures documented

### Quality
- [ ] No TODO comments in critical paths
- [ ] Error categorization deployed (Persistence/Corruption/Temporary/Cleanup)
- [ ] Audit trail verified at every mutation
- [ ] Operational visibility complete

---

## Success Criteria

After v1.0.0 is released, the following should be true:

1. **Can run 100+ production sites** - Proven by integration tests on multiple OS versions
2. **No mysterious failures** - Every failure path tested and recoverable
3. **Operators trust it** - Features work as documented; capabilities are accurate
4. **Incident response is clear** - Failed operations leave auditable state
5. **Concurrent safety proven** - Adversarial tests pass consistently
6. **Recovery is fast** - Can restore from any failure in <5 minutes

---

## Timeline Estimate

| Component | Effort | Dependency |
|-----------|--------|------------|
| Gate 1 (Lifecycle Authority) | 2-3 days | None |
| Gate 2 (Distributed Locks) | 3-4 days | Gate 1 |
| Gate 3 (Capability Accuracy) | 1 day | None (mostly done) |
| Gate 4 (DB Restoration) | 2-3 days | None |
| Gate 5 (Failure Injection) | 3-4 days | Gate 1 + Gate 2 |
| Integration Tests | 2-3 days | All gates |
| **Total** | **14-18 days** | **Sequential** |

---

## What v1.0.0 is NOT

- ❌ Not adding 50 new features
- ❌ Not replacing the UI
- ❌ Not implementing multi-tenant SaaS
- ❌ Not competing with cPanel on breadth

## What v1.0.0 IS

- ✅ Complete architectural contracts
- ✅ Bulletproof failure handling
- ✅ Provable cross-process safety
- ✅ Production-hardened hosting control panel
