# StePanel v1.0.0 Production Readiness Gates

**Status:** CURRENT AUTHORITATIVE RELEASE-GATE DOCUMENT
**Last Updated:** 2026-09-25
**Target:** Ready to run 100+ production WordPress/PHP customer sites  
**Approach:** Complete existing architectural contracts, add robustness testing

All release approval decisions must use this document. `PRODUCTION_READINESS.md`
is a deployment-status summary; `PRODUCTION_SCORECARD.md` and prior audits are
historical and must not be treated as current approval.

## Executive Summary

v1.0.0 is NOT about adding new features. It's about making existing architecture bulletproof.

The design is sound. The implementation is 85% there. The remaining work is:
- Finishing what's already designed
- Testing failure paths rigorously
- Making recovery guarantees explicit

---

## Gate 1: One Lifecycle Authority

**Requirement:** ALL site mutations must flow through `SiteManager` (internal/sites/Manager)

**Architectural Rule (September 2026 Clarification):**

Two distinct domains exist:
1. **Staging areas** — Opaque, temporary workspaces allocated and owned by SiteManager
   - Domain components (importer, restore, git, backup, etc.) may mutate staging areas granted to them
   - SiteManager grants staging and guarantees cleanup if activation fails
   - Direct filesystem operations within staging are acceptable
   - Staging paths must never be persisted or assumed to exist across operation boundaries

2. **Canonical sites** — Permanent, named site trees at `{webRoot}/sites/{siteName}`
   - ONLY SiteManager.Create/Activate/Delete touch canonical paths
   - No exceptions, no direct filesystem calls
   - All operations verify site exists before mutation
   - All operations acquire mutation locks before touching site state
   - All operations record audit events and recovery metadata

**What this means:**
```
create → SiteManager.Create()
import → SiteManager.GrantStaging() + domain extract + SiteManager.Activate()
clone  → SiteManager.Clone()
restore → SiteManager.GrantStaging() + domain restore + SiteManager.Activate()
update → SiteManager.UpdateConfiguration()
delete → SiteManager.Delete()

# Never acceptable:
❌ Direct filepath.Join(webRoot, "sites", ...) outside internal/sites
❌ os.Mkdir/Create/Rename on /sites/* paths outside manager
❌ Reads of canonical site state without acquiring locks
```

**Current Status:**
- ✅ Manager interface defined (`internal/sites/manager.go`)
- ✅ Manager clone is staged, path-safe, and atomically activated
- ✅ Generic archive activation publishes through the manager
- ✅ Git activation and rollback publish through the manager
- ✅ cPanel, WordPress, and file-backup restores publish through the manager
- ✅ Staging clone publishes through the manager
- ✅ Backup restore-to-staging publishes through the manager
- ✅ cPanel, archive, WordPress, backup-restore, and staging workflows
      allocate publication trees through manager-owned staging
- ✅ Failed staging workflows discard trees through `SiteManager.DiscardStaging()`
- ✅ Site termination uses the manager for final deletion
- ⏳ Remaining direct lifecycle paths (generic create/update/suspend/resume)
      still need to be consolidated

**Acceptance Criteria:**
- [ ] All site creation operations validated to use SiteManager
- [ ] All site modification operations validated to use SiteManager
- [x] Implemented site deletion operations use SiteManager
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
- ✅ DBLocks implementation redesigned and verified (see below)
- ✅ Boot-time reconciliation guarded so only the panel runs it (previous
      behavior let panel and worker concurrently rename recovery journals
      and reconcile host state during startup)
- ✅ All direct mutation lock call sites use the durable fenced-lock wrapper;
      the wrapper retains the process-local mutex as a fast-path and acquires
      `DBLocks` for cross-process fencing.
- ✅ Wired into site termination, Git deployment/rollback, backup/restore,
      route/proxy changes, runtime reconciliation, resources, tasks, workers,
      SSH access, database operations, and account mutations.
- ✅ Resource enforcement acquires both site and owning-account fences, so
      resource updates cannot race account suspension.
- ✅ Two independent SQLite connections now exercise all five conflicting
      mutation lock scenarios; an OS-process regression test also proves
      hold/block/reacquire behavior across panel/worker-style processes.
- ⏳ Adversarial concurrent-operation tests still need to cover the full five
      workflows (not just their lock keys) against real helper boundaries.

**DBLocks fixes (September 2026):**

The prior `internal/operations/db_locks.go` implementation had latent bugs
that meant it could not actually be relied on, so its earlier "✅ implementation"
line above was inaccurate. Rewritten in this cycle:

- `INSERT ... WHERE NOT EXISTS` collided with the resource_key primary key
  on expired-lease takeover; SQLite raised UNIQUE-constraint errors and
  Acquire failed even though the lock was legally re-acquirable. Now uses
  `INSERT ... ON CONFLICT(resource_key) DO UPDATE ... WHERE lease_until <= ?`
  — a single atomic statement with no PK collision.
- `generation` was advertised as a fencing token but was hard-coded to 1
  and never checked by Release/Renew. Now monotonically increments per
  key, is returned to the caller in a `Lease` value, and is required by
  both Release and Renew.
- Release used to `DELETE` the row, which reset generation to 1 on the
  next acquisition and reopened the fencing race. Release now marks
  `lease_until = 0` so the generation counter is preserved across
  release/reacquire cycles.
- `newLeaseUntil` was computed once before the retry loop, so a waiter
  that waited most of the lease duration got an already-near-expired
  lease. `tryOnce` now recomputes both timestamps on each attempt.
- Retry budget was advertised as "5 minutes" but was 300 × 100ms = 30
  seconds. Replaced with `Acquire(ctx, key)` — caller supplies the wait
  deadline; no hidden hard-coded cap.
- Lease deadlines are stored as INTEGER unix nanoseconds. Ordering is now
  the same in SQL as in Go (variable-width RFC3339Nano strings were
  string-ordered, which is not temporal ordering).
- `Hold(ctx, lease)` handles automatic renewal on a `leaseTime/3`
  cadence so callers do not have to hand-roll a renewal goroutine.

Test coverage uses **two independent `*sql.DB` handles pointed at the same
file** (WAL mode) and `TestDBLocksAcrossOSProcesses`, which launches separate
test processes. It does not rely on two goroutines sharing one handle. Eight
regression tests cover: expired takeover, live-lease
protection, fencing on release, waiter freshness, honest context budget,
Hold renewal past the original lease, and takeover fencing.

The dead `internal/operations/distributed_locks.go` (file-based locks with
no production callers) has been removed to avoid future confusion about
which implementation to reach for.

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

**Evidence currently available:**

- `TestAdversarialMutationLockScenariosSerializeAcrossConnections` exercises
  all five lock-key combinations with two independent SQLite connections.
- `TestSiteMutationLockSerializesRealHelperBoundaryAcrossApps` runs two
  independent app instances through the same helper boundary and verifies
  that their critical sections do not overlap.

These tests prove lock acquisition and helper serialization. They do not yet
prove that every full workflow remains consistent after a conflicting
operation is interrupted, so the operation-level acceptance item remains open.

**Acceptance Criteria:**
- [x] Distributed lock acquired before each currently implemented mutation
- [x] Lock held until operation completes or rolls back
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
    "available": true,
    "mode": "available",
    "reason": "Managed DB helper provisions, restores, verifies inventory, and rolls back on staged-import failure when credentials are supplied."
  }
```

**Current Status:**
- ✅ CapabilityMode enum implemented
- ✅ Database restoration reports as available only when the managed DB helper is executable
- ✅ All capabilities updated to report mode
- ✅ Audit: capability probes verify the managed DB helper dependency before reporting availability

**Acceptance Criteria:**
- [x] All capabilities report mode (not just available/unavailable)
- [x] Mode accurately reflects operation readiness
- [x] No "available: true" for operations not yet implemented
- [x] Documentation matches capabilities (capabilities verify their dependencies)

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
- ✅ Archive import checks disk space
- ✅ Recovery journal initialized
- ✅ Automatic restoration is transactional within staged import when a valid database password is supplied; the no-credential path remains explicit manual follow-up.

**Implementation Status:**
1. ✅ ArchiveImportRequest accepts opt-in automatic-restore credentials
2. ✅ The control plane injects the existing managed DB helper adapter
3. ✅ Transactional restoration runs before staged site activation
4. ✅ Provisioned database cleanup is retained until activation commits
5. ✅ Capabilities probe the executable DB helper before reporting availability

**Acceptance Criteria:**
- [x] Database restoration is transactional within the staged import
- [x] Failure rolls back the provisioned managed database and staged site
- [x] Config is rewritten with actual credentials for the automatic path
- [x] Managed inventory verification passes before success
- [x] Capability reports availability only when the DB helper dependency exists

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
// $STEPANEL_FAIL_AT="restore:commit" injects a deterministic error.
// The current hooks cover backup init/archive/verify/commit, restore
// verification/extraction/activation/database/commit, termination init,
// deployment activation, account suspension before persistence, and
// transaction init/commit.
failureInjection("restore", "commit")
```

Subprocess regression drills now cover both termination-journal persistence
and filesystem transaction recovery: a child process is killed with
`SIGKILL` after a destructive/replacement step starts, and the parent reloads
the state, restores the original site, and completes the journal. These prove
cross-process persistence for those recovery primitives, but do not replace
the still-open full host-kill/restart drills below.

**Critical Operations to Test:**
1. Backup (boundary tests: init, archive, verify, commit; host-kill drill remains)
2. Restore (boundary tests: verify, extract, activate, database, commit; host-kill drill remains)
3. Deploy (boundary test: activation; clone/build/health-check drill remains)
4. Terminate (boundary test: initiation; backup/cleanup/state-removal drill remains)
5. Account suspension (boundary test: before persistence; helper/state-save drill remains)

**Evidence currently available:** the repository recovery-drill harness passes
partial SQL import cleanup, interrupted transaction recovery, configuration
rollback, and pending runtime reconciliation. Its generated results explicitly
exclude power-loss recovery, so the five-operation process-kill acceptance
criteria remain open.

**Acceptance Criteria:**
- [x] Failure injection framework implemented at transaction init/commit
- [x] Boundary-level failure tests cover all 5 operations
- [ ] Multi-point process-kill/restart drills cover all 5 operations
- [ ] No mysterious half-states discovered
- [ ] Recovery is deterministic

---

## v1.0.0 Release Checklist

### Architecture
- [ ] Gate 1: One Lifecycle Authority - ALL mutations through SiteManager
- [ ] Gate 2: Cross-Process Locks - Distributed locks enforced
- [x] Gate 3: Accurate Capabilities - No "available: true" for unimplemented
- [x] Gate 4: Automated DB Restoration - Transactional end-to-end
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
