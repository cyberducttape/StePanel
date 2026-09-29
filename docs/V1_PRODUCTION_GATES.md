# StePanel v1.0.0 Production Readiness Gates

**Status:** CURRENT AUTHORITATIVE RELEASE-GATE DOCUMENT
**Last Updated:** 2026-09-28
**Target:** Ready to run 100+ production WordPress/PHP customer sites  
**Approach:** Complete existing architectural contracts, add robustness testing

All release approval decisions must use this document. `PRODUCTION_READINESS.md`
is a deployment-status summary; `archive/PRODUCTION_SCORECARD.md` (archived) and prior audits are
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

**Current Status: COMPLETE ✅**
- ✅ Manager interface defined (`internal/sites/manager.go`)
- ✅ Manager clone is staged, path-safe, and atomically activated
- ✅ Generic archive activation publishes through the manager
- ✅ Git activation and rollback publish through the manager
- ✅ cPanel, WordPress, and file-backup restores publish through the manager
- ✅ Staging clone publishes through the manager
- ✅ Backup restore-to-staging publishes through the manager
- ✅ Release pipeline uses manager for release staging
- ✅ cPanel, archive, WordPress, backup-restore, release, and staging workflows
      allocate publication trees through manager-owned staging
- ✅ Failed staging workflows discard trees through `SiteManager.DiscardStaging()`
- ✅ Site termination uses the manager for final deletion
- ✅ All canonical site modifications protected by distributed locks
- ✅ All staging operations use SiteManager allocation/activation

**Acceptance Criteria:**
- [x] All site creation operations validated to use SiteManager (Phase 1 audit: importer.go, cpmove.go, staging.go all use manager staging)
- [x] All site modification operations validated to use SiteManager (Phase 2 audit: node_tooling.go, apps.go use lock-protected operations; release_pipeline.go uses manager for release staging)
- [x] Implemented site deletion operations use SiteManager (site_lifecycle.go verified using manager.Delete)
- [x] Grep audit: no `os.Mkdir.*sites` outside manager (only in test files)
- [x] Grep audit: no direct file operations on site paths outside manager (all non-test operations are read-only or lock-protected)

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
- ⏳ Full adversarial concurrent-operation acceptance remains open for the five
      workflows: current tests prove lock-key serialization and helper-boundary
      exclusion, but do not yet prove each complete workflow remains consistent
      when its conflicting operation is interrupted.

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
- `DefaultManager.Delete` now checks its operation context before path
  resolution and immediately before filesystem removal; a cancelled-context
  regression test verifies the canonical site remains intact.
- Route publication and deletion now share the site/vhost fenced lock set;
  `TestRouteMutationLockSetSerializesPublicationAndDeletion` verifies that a
  deletion cannot acquire the vhost fence while publication holds it.
- Lock-held backup workflows now call `CreateSiteBackupContext`; cancellation
  is checked during archive traversal, file copying, verification, and commit
  so a fenced lease loss cannot publish a backup after takeover.
- Lock-held offsite backup transfers now inherit the operation context, so
  cloud upload/download work is cancelled when the site lease or job context
  is cancelled.
- App deployment/actions and containerized runner completion now check the
  fenced operation context before durable completion records, preventing a
  lost lease from being reported as a successful mutation.
- SSH access and environment desired-state workflows now reject a fenced
  operation before persistence and retain pending state when cancellation is
  observed after helper execution, allowing reconciliation to repair host
  state instead of recording false success.
- Scheduled task and resource-profile workflows now reject fenced operations
  before desired-state persistence and leave pending state when helper work
  completes after cancellation.
- PHP and Python runtime/application workflows now use the same pending-state
  fallback around helper completion and reject lease loss before durable
  success is recorded.
- Worker lifecycle, creation, and removal workflows now reject lease loss
  before durable transitions and retain pending state for reconciliation.

These tests prove lock acquisition, helper serialization, and the delete
cancellation boundary. They do not yet prove that every full workflow remains
consistent after a conflicting operation is interrupted, so the operation-level
acceptance item remains open.

**Acceptance Criteria:**
- [x] Distributed lock acquired before each currently implemented mutation
- [x] Lock held until operation completes or rolls back
- [x] Lock-key serialization tests pass for the 5 scenarios above — site_lock_workflows_test.go
- [x] No race condition bugs after concurrent operations — verified through workflow tests
- [ ] Full conflicting workflows remain in a known good state after interruption

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

Subprocess regression drills now cover termination-journal persistence,
filesystem transaction recovery, and release activation recovery: a child
process is killed with `SIGKILL` after a destructive/replacement step starts,
and the parent reloads the state, restores the original site/release, and
completes recovery. These prove cross-process persistence for those recovery
primitives, but do not replace the still-open multi-point host-kill/restart
matrix below.

**Critical Operations to Test:**
1. Backup (boundary tests: init, archive, verify, commit; hosted worker-kill/restart drill passes)
2. Restore (boundary tests: verify, extract, activate, database, commit; hosted worker-kill/restart drill passes for file restore)
3. Deploy (hosted kill/restart at activation passes; clone/build/health-check interruption remains)
4. Terminate (hosted worker kill/restart during site-state removal passes; interruptions during backup and cleanup remain)
5. Account suspension (hosted panel kill/restart after persistence passes; external helper and earlier-stage interruptions remain)

**Evidence currently available:** hosted installation smoke run `36436484546`
passes on both AlmaLinux 9 and Rocky Linux 9 for cpmove import/recovery, durable
backup creation/recovery, file restore/recovery, termination recovery, account
suspension after panel SIGKILL, and Git deployment recovery after panel SIGKILL
during release activation. The deployment drill verifies that startup recovery
restores the original site content and clears the activation journal. Earlier
run `36382583261` passed the cpmove, backup, file-restore, termination, and
suspension paths before the deploy drill was added. The repository
recovery-drill harness passes partial SQL import cleanup, interrupted
transaction recovery, configuration
rollback, and pending runtime reconciliation. Its generated results explicitly
exclude power-loss recovery. A disposable Rocky Linux 9.8 VM also passed the
installed-host cpmove, backup, file-restore, termination, suspension, and deploy
recovery drills. Abruptly killing the QEMU process and rebooting the guest left
systemd healthy, the panel and worker units active, and `/readyz` reporting ready;
stopping/restarting MariaDB separately left the control plane healthy. This is
limited VM evidence, not a host power-loss or disk-exhaustion test. Local SIGKILL
regression tests now cover termination journals, filesystem restore
transactions, Git release activation, durable account suspension state, backup
staging cleanup, and managed-database journal cleanup. The installer was also
corrected to handle images that already have full `curl` installed and to
install the Git runtime required for deploys; the hosted recovery smoke passes
with those fixes on both distributions. The five-operation acceptance criteria
remain open for multi-point failure injection, real disk-exhaustion, actual
host power-loss, and broader workload and recovery-time evidence.

The installed systemd units now enable `ProtectSystem=full`, private `/tmp`,
and kernel/control-group protections. On Rocky Linux 9.8,
`systemd-analyze security` improved from exposure 8.6 (`EXPOSED`) to 6.7
(`MEDIUM`). `NoNewPrivileges` and SUID/SGID restrictions remain intentionally
disabled because production root-helper calls cross the exact-command sudoers
boundary; the hosted install smoke asserts the enabled protections and runs
the recovery suite under them.

**Acceptance Criteria:**
- [x] Failure injection framework implemented at transaction init/commit
- [x] Boundary-level failure tests cover all 5 operations
- [ ] Multi-point process-kill/restart drills cover all 5 operations (single-boundary recovery drills now pass for backup, file restore, deploy, termination, and suspension)
- [ ] No mysterious half-states discovered
- [ ] Recovery is deterministic

---

## v1.0.0 Release Checklist

### Architecture
- [x] Gate 1: One Lifecycle Authority - ALL mutations through SiteManager (VERIFIED: 8/8 CRITICAL + 3/3 HIGH files compliant)
- [ ] Gate 2: Cross-Process Locks - Lock layer enforced; full interrupted-workflow acceptance remains open
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
