# StePanel v1.0.0 Production Readiness Gates

**Status:** CURRENT AUTHORITATIVE RELEASE-GATE DOCUMENT
**Last Updated:** 2026-10-13
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

## Finite release contract

The following items are release gates, not general roadmap suggestions. A
release may not be described as **StePanel 1.0 — production-ready for
operator-managed single-host deployments** until every P0 item has executable
evidence recorded in this document or a linked result artifact.

### The 1.0 deployment promise

If every P0 gate below closes, the release may make exactly this promise:

> StePanel 1.0 supports operator-managed, single-host PHP/WordPress hosting
> on the Linux distribution, database, web server, and PHP runtime matrix
> published with the release. High availability, mutually hostile
> multi-tenant hosting, and hands-off disaster recovery are not included.

Docker and Kubernetes packaging remain single-replica control-plane
packaging ([CERTIFIED_ENVELOPE.md](./CERTIFIED_ENVELOPE.md)); they must not be
described as high availability while the control plane uses host-local state
and host-bound lifecycle operations. Site, domain, and account counts may be
published only as a certified envelope, never as targets.

### P0 — required before 1.0

| Requirement | Current status | Evidence / remaining work |
|-------------|----------------|---------------------------|
| Encrypted durable payloads for archive DB credentials | ✅ Implemented | Archive restore rejects missing `STEPANEL_ACCOUNT_KEY`; encrypted-payload tests cover acceptance and rejection. |
| Fail-closed archive handling | ✅ Implemented | Unsafe paths, links, and unsupported objects abort staged import; importer safety tests pass. |
| Preserve restrictive imported file modes | ✅ Implemented | TAR/ZIP extraction preserves ordinary permission bits and strips special bits; mode tests pass. |
| Realistic disk admission and reservations | ✅ Done | Known `Content-Length` is admitted at the actual archive size; unknown-length streams reserve 256 MiB ahead of the bytes written up to the upload ceiling; cPanel database restores also reserve space on a local database data directory. |
| VM crash testing: power loss, ENOSPC, DB outage, broker interruption, worker death | ⚠️ Partial | The [2026-10-12 KVM run](./lab-results/2026-10-12-rocky9-kvm-certification.md) passed abrupt guest kills (idle and mid-backup), plus real ENOSPC during backup and file restore. ENOSPC in publication/import/database restore and physical power loss remain open. |
| Repeated recovery scenarios | ⚠️ Open | 114 repository-level SIGKILL repetitions are recorded. The 2026-10-12 Rocky KVM guest now has a successful two-iteration full extended matrix, including cpmove, backup, file/database restore, termination, suspension, and Git deployment interruption checkpoints ([evidence](./lab-results/2026-10-12-rocky9-repeated-recovery-matrix.md)). The earlier 2026-10-11 failed two-iteration attempt remains documented ([failure record](./lab-results/2026-10-11-rocky9-repeat-matrix-attempt.md)); the passing rerun is only two repetitions, not the required 20–50. Broker-process kills, control-plane database failure, and some restore-staging boundaries still need separate real-host evidence. |
| Cross-process data consistency | ✅ Implemented; needs matrix evidence | The panel and job worker share the control-plane database. Accounts and site assignments are written per row with the observed payload as a compare-and-swap (`TestStaleAccountStorePersistsOnlyItsOwnChange`, `TestStaleAccountStoreConflictingChangeIsRejected`); deployment history appends with a revision check (`TestDBStoresSharingHistoryDoNotLoseRecords`); API tokens are individual rows. A conflicting write fails with a retryable error instead of overwriting. Two-process soak evidence on an installed host remains to be recorded. |
| Authoritative deployment status | ✅ Implemented | Activation is the commit point for Git deploy, pipeline release, and Git rollback. A failure before activation fails the job; after activation the job succeeds with `outcome` `activated` or `committed_reconciliation_pending`, and history records the completed activation (`TestCommittedReleaseIsNeverReportedAsCancelled`). |
| Application-level WordPress restore proof | ⚠️ Implemented; needs installed-host evidence | Rehearsals of WordPress backups restore into a throwaway `stepanel-proof-<id>` site through the production path: site account and PHP-FPM pool, `restore-wordpress` import as a fresh database user, `wp-config.php` rewritten to those credentials and a private `*.localhost` hostname, a published web server route, and loopback probes of `/` and `/wp-login.php` that fail on database errors, the installer, or off-host redirects ([OPERATIONS.md](./OPERATIONS.md)). Proof resources are tracked in an uncommitted site transaction that startup recovery removes. Covered by `TestWordPressProofRestoresAndServesThrowawaySite`, `TestStartupRecoveryRemovesInterruptedWordPressProof`, and related tests with recording helpers. Remaining: a passing run on a real installed host for each supported web server (Caddy, Apache, OpenLiteSpeed) with a real WordPress backup, recorded under `lab-results/`. The operator-supplied `STEPANEL_RECOVERY_PROOF_COMMAND` hook alone is not sufficient release evidence. |
| Operational notifications | ⚠️ Implemented; delivery evidence required | Failed/dead-letter transitions enter a SQLite outbox atomically; signed HTTPS webhook and STARTTLS SMTP delivery retry independently, and `/api/health/operational` exposes undelivered and failed counts. Tests cover event creation, HMAC signatures, a verified STARTTLS SMTP session including auth and message delivery, redaction, and retry state. Still required: delivery against configured live SMTP/webhook endpoints and outage/retry evidence on an installed host, plus operator-facing alert routing/runbook validation. |
| Capacity certification | ❌ Open | [CERTIFIED_ENVELOPE.md](./CERTIFIED_ENVELOPE.md) lists targets (500 sites, 2,000 domains, 100 accounts, 2 concurrent mutations), not measurements. A reproducible, published run must certify the envelope before any count is claimed. |
| Upgrade and rollback | ⚠️ Partial | The N-1 upgrade smoke (`deploy/lab/upgrade-smoke.sh`, last passing run `37940877632` on 2026-10-09) upgrades the previous release, verifies the audit chain and control-plane state, and proves the installer transaction rolls a deliberately broken candidate back to the running version and its intact database. Remaining: a published list of supported upgrade paths, versioned state-migration evidence for each, a clean-install run, and a tested rollback recorded for the release candidate. |
| Supported runtime matrix | ⚠️ Partial | The installer has package-manager paths for Debian-family and RHEL-family hosts and accepts Caddy, Apache, and OpenLiteSpeed, plus MariaDB/MySQL/PostgreSQL. That is implementation scope, not a certified support promise. The installed-host CI matrix currently exercises AlmaLinux 9 + Caddy + MariaDB and Rocky Linux 9 + Caddy + MariaDB; Debian/Ubuntu, Apache, OpenLiteSpeed, other database engines, PHP-version combinations, SELinux modes, and filesystem combinations must remain unclaimed until individually tested and recorded. Publish the exact supported combinations and evidence with the release. |
| Security lifecycle | ⚠️ Partial | [SECURITY.md](../SECURITY.md) defines private vulnerability reporting and the threat model. Remaining: an independent security review of the release candidate and a supported-version patch policy that applies after 1.0. |
| Typed privileged-operation protocol | ✅ Implemented | Production callsites use concrete root-broker request types, including environment/resource operations, database cleanup/reconciliation, and managed route deletion. `Request` no longer contains a generic helper payload, the broker has no generic handler, and production helper wrappers fail closed without a typed route. A regression test rejects the removed request type. |
| Release documentation reconciled with executable evidence | ⚠️ Open | Every checked claim must link to a current test result, hosted run, or reproducible artifact; stale claims must be downgraded or removed. The release must also publish the supported OS/runtime matrix, upgrade runbooks, a disaster-recovery runbook, and the support/escalation path. |

### Runtime support boundary (current evidence, not certification)

The installer accepts more combinations than CI has validated. Do not infer
support from package-manager detection, selectable web-server options, or a
successful unit test. At this snapshot the installed-host smoke matrix covers
only:

| Distribution | Web server | Database | Evidence |
|---|---|---|---|
| AlmaLinux 9 | Caddy | MariaDB | `.github/workflows/install-smoke.yml` (`almalinux-9-caddy-mariadb`) |
| Rocky Linux 9 | Caddy | MariaDB | `.github/workflows/install-smoke.yml` (`rocky-9-caddy-mariadb`) |

This matrix is smoke coverage, not production certification. PHP runtime
versions, Apache/OpenLiteSpeed, Debian/Ubuntu, MySQL/PostgreSQL, alternate
filesystems, SELinux policy variants, and upgrade paths must be listed as
untested unless a linked installed-host result says otherwise. Release notes
must not describe those combinations as supported based solely on installer
code paths.

### Upgrade and rollback contract

The installer transaction rollback covers a candidate that fails its
post-install health check: it restores the previous binary/configuration and
the byte-preserved control-plane database file set. This does not promise
rollback after the candidate has successfully started and accepted writes.
For a completed upgrade, the supported recovery path is to stop panel and
worker, reinstall the prior release, and restore the matching pre-migration
database snapshot as documented in [STATE.md](./STATE.md); writes made after
the snapshot are lost. One-way data or encryption migrations may require this
snapshot and cannot be reversed by reinstalling an older binary alone.

Only explicitly tested source-to-target version paths may be advertised.
Currently the N-1 lab fixture is v0.6.0 to the candidate; it is not evidence
for every intermediate release, a clean-install certification, or a release
candidate until those exact runs are recorded. Each release must record the
source version, target version, schema versions, clean-install result,
interrupted-upgrade behavior, installer rollback result, and snapshot-based
post-upgrade recovery result.

### P1 — strongly recommended for 1.0

These items do not silently become release claims. They are tracked separately:

- Reversible site suspend/resume with reason, audit event, and capability
  reporting.
- First-class blank PHP, WordPress, Git, and Node site creation workflows.
  Blank PHP sites: implemented (`POST /api/sites`, durable `site.create` job,
  `site_creation_test.go`); WordPress, Git and Node templates are open.
- The operator-supplied `STEPANEL_RECOVERY_PROOF_COMMAND` hook remains
  available for non-WordPress applications. It receives `<site> <stage>`,
  and a rehearsal records the `application` level only when the command
  exits successfully. It does not satisfy the P0 WordPress restore proof.

SMTP/webhook notifications and the WordPress application-level restore proof
were moved from P1 to P0 on 2026-10-11.

### Test coverage ratchet

CI enforces a repository-wide statement-coverage floor of **50%** and
package-specific floors for the security boundary, durable jobs, archive
importer, and legacy root workflow handlers. The 50% value is an executable
ratchet from the current 47.5% baseline, not a production-quality claim by
itself. It must increase as root-package handlers are extracted and tested;
the release process must not lower it to accommodate a regression. Coverage is
supplemental evidence and does not replace the failure-recovery matrix below.
The current local repository-gate run is recorded in
[2026-10-13-repository-production-gate.md](./lab-results/2026-10-13-repository-production-gate.md);
it passed but does not close the installed-host release gates.

---

## Gate 1: One Lifecycle Authority

**Requirement:** Every site lifecycle operation has exactly one owner per
layer, and every write that publishes or removes a canonical site tree goes
through `SiteManager`'s path-safe primitives.

**Roles (October 2026 revision).** An earlier version of this gate said all
site mutations flow through `SiteManager`. The code does not work that way,
and this gate now describes the implementation as it is:

| Layer | Owner | Responsibility |
|-------|-------|----------------|
| Lifecycle orchestration | Root-package lifecycle handlers and durable jobs (`site_lifecycle.go` `handleSiteTermination`, import/restore/clone/release jobs) | Locks, ordering, audit, recovery, and the decision to create, publish, or remove a site |
| Filesystem primitive | `SiteManager` (`internal/sites/manager.go`) | Staging allocation, atomic activation/replacement/rollback, clone, path-safe final deletion. Every path is derived from the manager's own web root |
| Privilege executor | Root broker and helpers (`internal/rootbroker`, `stepanel-sitectl`, `stepanel-appctl`) | Site Unix account, ownership, PHP-FPM pool, SSH access, quotas, services, and the privileged part of site teardown |

**Two domains:**
1. **Staging areas**: temporary workspaces allocated by `SiteManager`.
   - Domain components (importer, restore, git, backup) may mutate staging
     granted to them.
   - Staging is published only by `SiteManager.ActivateStaged*` and discarded
     only by `SiteManager.DiscardStaging()`.
   - Staging paths are never persisted across operation boundaries.
2. **Canonical sites**: `{webRoot}/sites/{siteName}`.
   - Unprivileged code publishes, replaces, and removes canonical trees only
     through `SiteManager`.
   - The privileged teardown helper (`stepanel-sitectl delete`) also removes
     the site tree, because it must first remove root-owned state inside it.
     `SiteManager.Delete()` then finalizes the removal idempotently. This is
     the one place outside `SiteManager` that deletes a canonical tree.

**How each operation actually flows:**
```
create  → site.create job (site_creation.go): SiteManager.CreateStaging() + template
          → broker/stepanel-sitectl prepare (account, PHP-FPM pool)
          → SiteManager.ActivateStaged() → broker seal; on failure (or after an
          unclean shutdown) broker delete + SiteManager.Delete() remove the new site
import  → SiteManager.CreateStaging() + domain extract + SiteManager.ActivateStaged()
clone   → SiteManager.CreateStaging() + copy (staging.go) + SiteManager.ActivateStaged()
restore → SiteManager.CreateStaging() + domain restore + SiteManager.ActivateStaged()
release → SiteManager.CreateReleaseStaging() + build + SiteManager.ActivateStagedReplacing()
          (rollback: SiteManager.RollbackStagedActivation())
update  → domain handlers + root broker (PHP runtime, resources, quota); SiteManager is not involved
delete  → lifecycle job: backups, services, tasks, routes, databases
          → broker/stepanel-sitectl delete (account, PHP-FPM, SSH, site tree)
          → SiteManager.Delete() (path-safe, idempotent finalization)

# Never acceptable:
❌ os.Mkdir/Create/Rename on {webRoot}/sites/* outside SiteManager
❌ Publishing a staged tree without SiteManager.ActivateStaged*
❌ A new privileged path that deletes canonical trees without being listed above
```

**Not implemented in `SiteManager`** (each returns `ErrNotImplemented`; no
operation above routes through them): `ImportArchive`, `Restore`,
`UpdateConfiguration`, `Suspend`, `Resume`. `TestGateOneDocumentMatchesManager`
fails if this list drifts or the flow above routes an operation to one of them.

**Implemented but unused:** `SiteManager.Create()` and `SiteManager.Clone()`
have no production callers; the flows above use staging instead.

**Current Status: MET for publication and removal; configuration updates are broker-executed**
- ✅ Generic archive, Git (activation and rollback), cPanel, WordPress,
      file-backup restore, staging clone, backup restore-to-staging, and the
      release pipeline all publish through manager-owned staging
- ✅ Failed staging workflows discard trees through `SiteManager.DiscardStaging()`
- ✅ Site termination finalizes deletion through `SiteManager.Delete()` after
      the privileged helper removes root-owned state
- ✅ Canonical site modifications are protected by distributed locks
- ⚠️ Configuration updates, suspend, and resume have no `SiteManager` entry
      point; updates run through domain handlers and the root broker, and
      suspend/resume are not offered

**Acceptance Criteria:**
- [x] All site creation operations publish through SiteManager staging (Phase 1 audit: importer.go, cpmove.go, staging.go use manager staging)
- [x] Site modification operations are lock-protected (Phase 2 audit: node_tooling.go, apps.go; release_pipeline.go uses manager release staging)
- [x] Site deletion finalizes through `SiteManager.Delete()`; the privileged helper's tree removal is the documented exception
- [x] Grep audit: no `os.Mkdir.*sites` outside manager (only in test files)
- [x] Grep audit: no direct file operations on site paths outside manager (all non-test operations are read-only or lock-protected)
- [x] This section matches `SiteManager`'s implemented methods (`TestGateOneDocumentMatchesManager`)

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
- ✅ Full interrupted-workflow acceptance (2026-10-02): `workflow_interruption_test.go`
      interrupts each of the five real workflows at every instrumented
      boundary, runs the conflicting workflow, and verifies the final state.

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
- `TestSiteMutationLockLossTerminatesInFlightHelper` starts a real long-running
  helper, fences its durable lease from an independent database connection,
  and verifies the helper is terminated before it can record completion.
- `DefaultManager.Delete` now checks its operation context before path
  resolution and immediately before filesystem removal; a cancelled-context
  regression test verifies the canonical site remains intact.
- Route publication and deletion now share the site/vhost fenced lock set;
  `TestRouteMutationLockSetSerializesPublicationAndDeletion` verifies that a
  deletion cannot acquire the vhost fence while publication holds it.
- Domain claim and verification now share the site fence with termination, and
  proxy deployment/deletion check the fenced context before reporting helper
  success. Route reconciliation also refuses to persist applied/deleted state
  after the lease is lost.
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
- Node tooling, WordPress, Composer, and `.htaccess` workflows now check the
  fenced context both before helper execution and before reporting completion.
- Database provisioning, credential rotation, deletion, Redis desired-state
  updates, account suspension, release activation, staging publication, and
  task reconciliation now reject lease loss at their helper-to-state boundary.
- Account creation, plan/site assignment, deletion, password/MFA changes,
  recovery actions, and session revocation now use the account fence; account
  creation also fences site resource-profile enforcement.
- API-token creation/revocation, backup schedule changes, webhook
  configuration, and Git deployment/rollback now use their account/site fence
  and check lease loss before reporting completion.
- Environment reconciliation now retains the item as pending when its helper
  completes after the site lease is lost.
- Route, PHP/Python runtime, resource, task, and worker reconciliation now
  re-check the lease after the final applied-state write and restore pending
  state when cancellation is observed at that boundary.
- Route deletion now leaves the desired route pending with an error when the
  lease is lost after the webserver helper returns, instead of removing the
  durable desired state.

These tests prove lock acquisition, helper serialization, and the delete
cancellation boundary. They do not yet prove that every full workflow remains
consistent after a conflicting operation is interrupted, so the operation-level
acceptance item remains open.

**Acceptance Criteria:**
- [x] Distributed lock acquired before each currently implemented mutation
- [x] Lock held until operation completes or rolls back
- [x] Lock-key serialization tests pass for the 5 scenarios above — site_lock_workflows_test.go
- [x] No race condition bugs after concurrent operations — verified through workflow tests
- [x] Full conflicting workflows remain in a known good state after interruption — `workflow_interruption_test.go` drives the real workflows: backup interrupted at init/archive/verify/commit then file restore; file restore interrupted at verify/extract/activate/commit then site deletion; release activation interrupted then restore; resource update interrupted mid-enforcement then account suspension then reconciliation; and a pending route update with termination interrupted at each of its nine journaled steps then resumed. Each scenario asserts exact site contents, no leftover staging, terminal recovery journals, no partial backups, and continued usability

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
// verification/extraction/activation/database/commit, termination init and
// each journal boundary (backup/database/routes/proxies/tasks/services/
// site-state/ownership), deployment activation, account suspension before
// persistence, and transaction init/commit.
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

**Evidence currently available:** hosted installation smoke run
[38238726272](https://github.com/cyberducttape/StePanel/actions/runs/38238726272)
passes on both AlmaLinux 9 and Rocky Linux 9 for the default recovery drills
and an expanded alternate kill-boundary matrix covering cpmove import, backup
verification, file-restore commit, termination, account suspension, and Git
deployment activation. The deployment drill verifies that startup recovery
restores the original site content and clears the activation journal. The
repository
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

The newer [2026-10-11 hosted installation smoke](./lab-results/2026-10-11-hosted-install-smoke.md)
passed the default installed-host recovery matrix once each on Rocky Linux 9
and AlmaLinux 9 at commit `a0c0585`. This is privileged systemd-container
evidence: it covers same-site replacement import, SFTP, backup/file/database
restore, termination, suspension, and deployment interruption, but does not
replace repeated KVM/VM matrix runs, physical power-loss testing, or ENOSPC
outside backup creation.

**KVM certification runs:** a 2026-10-10 Rocky Linux 9 KVM guest passed the
installation and full 107-pass recovery matrix once; this run did not execute
the separate abrupt-loss or ENOSPC phases. See
[lab-results/2026-10-10-rocky9-kvm-full-matrix.md](./lab-results/2026-10-10-rocky9-kvm-full-matrix.md).
The 2026-10-09 Rocky Linux 9 KVM guest with
SELinux enforcing passed the install smoke, the bounded matrix (57 drills),
and the full recovery matrix (105 drills) killing every supported journal
boundary of cpmove activation, backup, restore, termination, and suspension.
Killing the QEMU process left the guest recoverable both idle (`/readyz` ready
in 23 s including boot) and mid-backup (the job completed and the backup
verified, with no staging left behind). A backup hitting real ENOSPC on a
dedicated filesystem failed without publishing anything partial. The run also
found and fixed five defects invisible to container CI. It is one run, a QEMU
kill is not physical power loss, and ENOSPC was exercised only for backup;
the `restore:provision`/`restore:provisioned` boundaries have no real-host
drill yet. See
[lab-results/2026-10-09-rocky9-kvm-certification.md](./lab-results/2026-10-09-rocky9-kvm-certification.md).

The 2026-10-12 Rocky Linux 9 KVM run adds one full 108-check install/matrix
pass, idle and mid-backup abrupt guest-loss recovery, and real backup/file-
restore ENOSPC evidence. Its initial runner phase failed because SELinux
relabeling was attempted on a root-owned build-script bind mount; the
site-owned/read-only staging fix then passed runner and release-pipeline
retests on the same guest. Because the original full run records that failure
and the fix retest used the existing guest, this is not a clean all-green
full-run rerun. The 20–50× matrix, physical power loss, and other ENOSPC
boundaries remain open. See
[lab-results/2026-10-12-rocky9-kvm-certification.md](./lab-results/2026-10-12-rocky9-kvm-certification.md).

The installed systemd units now enable a root-owned, peer-authorized Unix
socket broker for host mutations. The panel and worker use
`NoNewPrivileges=true`, `RestrictSUIDSGID=true`, `PrivateDevices=true`,
`ProtectHome=true`, `ProtectProc=invisible`, and strict writable-path limits;
the broker is the only root process and has an explicit host-management
write-path allowlist. The hosted install smoke asserts the enabled protections
and runs the recovery suite under them. Existing installations must be
upgraded to remove the transitional sudo policy.

The transient rootless build service uses a closed device policy with access
only to `/dev/fuse`, required by the unprivileged overlay storage driver. The
privileged broker and panel/worker services retain `PrivateDevices=true`.

**Acceptance Criteria:**
- [x] Failure injection framework implemented at transaction init/commit
- [x] Boundary-level failure tests cover all 5 operations
- [x] Multi-point process-kill/restart drills cover all 5 operations at repository level — `crash_drill_test.go` kills a child process with SIGKILL at every instrumented boundary of backup (4), file restore (4), termination (9), and account suspension (2), then runs the production startup recovery (`recoverUncleanShutdown`) or resumes the durable job and verifies the final state; deploy activation is covered by `TestReleaseActivationRecoversAfterProcessKill`. Hosted installed-host runs remain the evidence for real services
- [x] No mysterious half-states discovered — the drills found two real leaks, both fixed: restore scratch trees under the import root and manager staging trees were never cleaned after a crash
- [x] Recovery is deterministic at repository level — 114 consecutive SIGKILL drills (6 repetitions of all 19 kill points) recovered to the same verified state
- [ ] Real disk exhaustion (ENOSPC) and host power loss on a disposable VM (partial: backup and file-restore ENOSPC passed on 2026-10-12; publication, import, database restore, and physical power loss remain; see [run record](lab-results/2026-10-12-rocky9-kvm-certification.md))
- [ ] Repeat the real-host recovery matrix 20–50 times per operation/fault
  combination, with post-boot invariant evidence retained (see
  `docs/REAL_HOST_FAILURE_MATRIX.md`)

---

## v1.0.0 Release Checklist

### Architecture
- [x] Gate 1: One Lifecycle Authority - ALL mutations through SiteManager (VERIFIED: 8/8 CRITICAL + 3/3 HIGH files compliant)
- [x] Gate 2: Cross-Process Locks - Lock layer enforced; interrupted-workflow acceptance covered by `workflow_interruption_test.go`
- [x] Gate 3: Accurate Capabilities - No "available: true" for unimplemented
- [x] Gate 4: Automated DB Restoration - Transactional end-to-end
- [ ] Gate 5: Failure Injection - Survives failure at every step (repository-level kill and failure matrix complete; full matrix passed once on a KVM guest; repeated runs, ENOSPC beyond backup, and power-loss evidence remain)

### Testing
- [x] Adversarial concurrency tests (5 scenarios) — the five conflicting lock pairs across two SQLite connections (`TestAdversarialMutationLockScenariosSerializeAcrossConnections`) and across OS processes (`TestDBLocksAcrossOSProcesses`); the real restore, termination, resource-update, and suspension entry points are proven to wait for a lock held by another process without mutating (`cross_process_workflow_test.go`); deploy and route updates are covered at the lock-key level
- [ ] Failure injection tests (all critical paths)
- [ ] Disposable host integration tests (real OS, full workflow)
- [ ] Production load simulation (concurrent operations)

Initial synthetic SQLite microbenchmarks now cover 10/100/500-site mutation
maps, 10 concurrent mutators, panel/worker connection-pool contention, two
actual concurrent test processes, 50-job backup/restore batches, and
audit-heavy writes ([LOAD_BASELINE_2026-09-29.md](./LOAD_BASELINE_2026-09-29.md)).
A mixed HTTP workload now runs in CI on every push: a real `stepanel` process
with 200 sites, 8 concurrent readers across 12 endpoints, and 20 real backup
jobs running at the same time, failing on any error, unfinished job, or p95
above 500 ms ([HTTP_LOAD_BASELINE_2026-10-02.md](./HTTP_LOAD_BASELINE_2026-10-02.md)).
It found and fixed a quadratic site-overview scan and an uncoordinated
service-status refresh. This still does not satisfy the production load
gate: it is not representative production hardware, real web servers and
databases, or more than a few hundred sites.

### Documentation
- [ ] v1.0.0 Production Gates (this doc)
- [ ] Architecture diagrams updated
- [ ] Capability documentation accurate
- [ ] Recovery procedures documented

### Quality
- [x] No TODO comments in critical paths (`TestNoDeferredWorkMarkersInCriticalPaths` guards production Go code, helpers, and the installer)
- [x] Error categorization deployed (Persistence/Corruption/Temporary/Cleanup): durable job persistence failures (temporary when SQLite is busy), quarantined recovery journals, and periodic cleanup failures feed `stepanel_state_errors_total`
- [x] Audit trail verified at every mutation (`TestEveryMutatingRouteIsAudited`: every mutating route is behind the fail-closed `Auth.Require` audit or is a reviewed self-auditing route; webhook deploys now audit before mutating)
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
- ❌ Not high availability, multi-host, or multi-region operation
- ❌ Not SLA monitoring/escalation, automated incident response, or enterprise support tiers

Those belong to the 2.0 shared-hosting platform in [ROADMAP.md](./ROADMAP.md).
Adding them to the 1.0 scope requires changing this document and the roadmap
together.
- ❌ Not competing with cPanel on breadth

## What v1.0.0 IS

- ✅ Complete architectural contracts
- ✅ Bulletproof failure handling
- ✅ Provable cross-process safety
- ✅ Production-hardened hosting control panel
