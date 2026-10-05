# StePanel Project Status

**Version:** v0.7.0 (Operator Beta)  
**Last Updated:** 2026-10-02
**Status:** Operator Beta; not approved for v1.0 production release

---

## Executive Summary

StePanel v0.7.0 is in Operator Beta — suitable for controlled single-host deployments with experienced operators. The product is **functionally complete** for its stated feature set. The remaining work before v1.0 is **proving production durability and failure recovery** rather than building new features.

**Release Approval:** Governed by [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md) — the sole authoritative release-gate document.

---

## Release Gate Status

See [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md) for complete gate requirements and acceptance criteria.

| Gate | Requirement | Status | Blocker |
|------|-------------|--------|---------|
| **Gate 1** | One Lifecycle Authority | ✅ MET (revised 2026-10-02) | None for publication and removal; configuration updates are broker-executed |
| **Gate 2** | Cross-Process Lock Enforcement | 🔄 PARTIAL (lock layer complete) | Full workflow interruption acceptance |
| **Gate 3** | Capability Reporting | ✅ COMPLETE | None |
| **Gate 4** | Automated Archive Database Restoration | ✅ COMPLETE | None |
| **Gate 5** | Failure Recovery Testing | 🔄 PARTIAL | Multi-point failure coverage, real disk-exhaustion and host power-loss testing |
| **Gate 2 extension** | Interrupted Workflow Acceptance | 🔄 OPEN | Full conflicting-workflow recovery evidence |

**Overall:** Operator Beta gates are in place; Gate 5 Phase 7 remains open for production approval.

### Changes on 2026-10-02 (on `main`, unreleased)

- **Root broker concurrency:** the socket broker serves connections
  concurrently and admits work through a resource-scoped scheduler (per site,
  database, account, or certificate domain; `-max-concurrent`, default 8).
  Health probes bypass the queue, and stalled peers are disconnected.
- **Durable job cleanup** no longer leaves memory and SQLite out of sync when
  a delete fails; fault-injection tests cover BEGIN, DELETE, COMMIT, and
  `SQLITE_BUSY`.
- **Outbound requests** (imports, task webhooks) share `internal/safehttp`:
  public addresses only, checked on the connected address. Task units deny
  cloud metadata ranges.
- **Domain names** are validated by one shared policy in every Go layer.
- **Installer** prints a host preflight, supports `--dry-run`, and requires
  `--take-over-host` (or, in guided mode, explicit consent) before stopping
  an existing web server. `--guided` and `--config` add a verified guided
  setup.
- **Recovery status:** restore rehearsals are recorded, run automatically
  after scheduled backups, and summarized per site
  (`GET /api/sites/recovery/{site}`), at the archive level.
- **Capacity:** a stated control-plane target (500 sites, session check p99
  ≤ 25 ms) is measured at 6–8.6 ms; see
  [LOAD_BASELINE_2026-10-02.md](LOAD_BASELINE_2026-10-02.md).
- **Gate 1** was redefined to match the implementation; see
  [V1_PRODUCTION_GATES.md](V1_PRODUCTION_GATES.md#gate-1-one-lifecycle-authority).

### Verified hardening before 2026-10-02

- Legacy shared-state blobs remain temporary compatibility storage, but writes
  now use revision CAS with bounded reload/merge retries for independent map
  keys. Conflicting same-key writes refresh the local snapshot and fail closed;
  persistence helpers restore in-memory values on failure. A subprocess test
  exercises a panel/worker-style stale writer against the same SQLite file.
- External-worker mode publishes durable worker identity, host, PID, start and
  last-seen times, supported job kinds, current jobs, and build/version. Both
  readiness and operational health require a fresh compatible worker.
- Archive inspection/import handlers return durable output and report operation
  errors through the job state. Archive scanning is context-bound and bounded
  for redirects, compressed bytes, entries, and decompressed content.
- Privileged operations use typed root-broker requests. The generic helper RPC
  and its caller-controlled helper/action/argv payload have been removed; an
  untyped production helper call now fails closed. Development installs retain
  direct helper execution for local workflows.
- The reported CodeQL path and integer alerts are resolved; the hosted CodeQL
  scan for `635ea5f` reported zero open alerts. The local full race suite and
  serial suite pass through `eb39e63`. Validation for the current documentation
  commit `44a765b` is queued; the quota-enabled installation/ENOSPC VM gate and
  the standard disposable-host matrix remain unverified until their current
  hosted runs complete. These checks are release evidence, not claims that the
  remaining production gates are closed.

---

## Key Subsystem Status

### Helper Layer Modernization (typed production protocol complete)

**Status:** The production callsite migration is complete. Root-broker requests
are finite typed operations; no generic helper request type or handler remains.

- ✅ Phase 1: Design & Foundation (100%)
- ✅ Phase 2: Broker foundation and validation (100%)
- ✅ Phase 3: Integration Testing (100%)
- ✅ Phase 4: Callsite Replacement (100%; production mutations and helper-backed reads use typed requests)

**What it is:** Replace 14 shell scripts (1200 lines, high security consequence) with a typed Go root broker. The broker centralizes input validation and fails closed for mutation paths that are not implemented yet; it is not a claim that every helper operation is available.

**Deliverables:** 
- `internal/rootbroker/` — Broker types, validation, supported operations, client, and explicit unsupported-operation responses
- Root-broker tests cover validation and fail-closed mutation behavior
- Integration documentation and migration guide
- `internal/rootbroker/client.go` — direct typed-client integration surface

**Status:** Native production installs route site, app, worker, environment, resource, proxy, vhost, runner, Git, TLS, and database operations through the root-owned Unix-socket broker. The panel and worker have no sudoers grant. The broker accepts concrete request types only; production helper wrappers reject calls without a typed operation. The former `BrokerBridge` wrapper was removed because it had no production callers and exposed unsupported operations through a misleading transitional abstraction.

**Typed protocol evidence (2026-10):** `rootbroker.Request` has no generic
helper field, the broker has no generic helper handler, and production helper
wrappers fail closed instead of serializing arbitrary helper argv. Fixed argv
validation remains internal for resource and runner operations after the broker
constructs arguments from their typed request fields.

**Next Action:** Keep the typed protocol invariant executable in tests and
re-audit every newly added production callsite. Task direct-helper fallback is
limited to non-production development installs.

---

### Gate 5: Failure Injection Testing (partial; Phase 7 remains open)

**Status:** Partial; hosted recovery evidence passes on both disposable distributions for the default drills and the expanded alternate kill-boundary matrix covering cpmove, backup, file restore, termination, account suspension, and Git deploy. A Rocky Linux 9.8 VM passed the same installed-host drills plus abrupt QEMU-process kill/reboot and MariaDB service outage/restart checks; the full Phase 7 failure matrix remains open.

- ✅ Phase 1: Failure Injection Framework (100%)
- ✅ Phase 2: Durable Checkpoint System (100%)
- ✅ Phase 3: Documentation & Guides (100%)
- ✅ Phase 4: Broker Integration (100%)
- ✅ Phase 5: Operation Journals (100%)
- ✅ Phase 6: Workflow Integration Testing (100%)
- 🔄 Phase 7: VM-Level Failure Testing (partial: Rocky 9.8 installed-host recovery, abrupt VM-process kill/reboot, and MariaDB outage checks; true host power loss and disk exhaustion remain untested)

**What it is:** Prove StePanel survives and recovers deterministically from real failures (SIGKILL, disk full, database offline) at operation boundaries.

**Deliverables:**
- Durable journals for 6 critical operations (site create, app deploy, DB provision, vhost config, DB restore, git deploy)
- Atomic write patterns (temp + rename) throughout
- 55+ broker operation tests + 6 workflow integration tests
- Provider-neutral VM test harness scaffold that fails closed until real
  disposable-VM provider commands are implemented; it is not itself
  production evidence
- 2000+ lines of integration guides and failure recovery documentation

**Local evidence supports:**
- ✅ Boundary-level rollback and cleanup tests for the listed operations
- ✅ Deterministic journal behavior in repository-level recovery drills
- ✅ Safe retry behavior for the tested journal paths
- ⚠️ Full host-level crash safety remains unproven until the Phase 7 matrix runs

`DefaultManager.Delete` now refuses to remove a site when its operation
context has been cancelled, with a regression test confirming the site remains
intact. This covers the manager cancellation boundary but does not replace
full conflicting-workflow recovery evidence.

Route publication and deletion now share the site/vhost fenced lock set, with
a regression test confirming that deletion cannot enter the vhost boundary
while publication holds it. Full interrupted-workflow recovery acceptance
remains open.

Domain claim and verification now share the site fence with termination.
Proxy deployment/deletion and route reconciliation also check the fenced
operation context before reporting or persisting successful state after helper
work. Full interrupted-workflow recovery acceptance remains open.

Lock-held backup workflows now propagate their operation context through
archive traversal and publication, with cancellation checks preventing a
fenced backup from being committed after lease loss.

Offsite backup transfers in lock-held jobs now inherit the same operation
context, preventing cloud transfer work from continuing after lease loss or
job cancellation.

Application deployment/actions and runner builds now verify the fenced
operation context before recording successful completion after helper work.

SSH access and environment desired-state updates now check the fenced context
before persistence and preserve pending reconciliation state when cancellation
is observed after helper execution.

Scheduled task and resource-profile updates now use the same pending-state
behavior when their mutation lease is lost around helper execution.

PHP and Python runtime/application updates now preserve pending reconciliation
state when helper execution completes after lease cancellation.

Worker lifecycle and desired-state updates now preserve pending reconciliation
state when their helper execution is interrupted by lease loss.

The same lease boundary is now enforced for Node tooling, WordPress, Composer,
`.htaccess`, database, Redis, account suspension, release activation, staging,
and task reconciliation workflows. Route deletion preserves pending desired
state when its helper completes after lease loss. Full interrupted-workflow
recovery acceptance remains open.

Account creation, assignment, deletion, credential recovery, password/MFA
changes, and session revocation now share the account fence; account resource
profile enforcement also acquires the affected site fences before host work.
API-token, backup-schedule, webhook, and Git deployment/rollback mutations now
use the same account/site fencing and completion checks.
Environment reconciliation also leaves host state pending instead of reporting
success after lease loss.
Route, runtime, resource, task, and worker reconciliation now apply the same
post-persistence lease check and pending-state fallback.

Hosted installation smoke run `36529280956` passes on AlmaLinux 9 and Rocky
Linux 9 for the default recovery drills plus alternate kill boundaries across
cpmove import, backup creation/verification, file restore commit, termination,
account suspension, and Git deployment activation. A disposable Rocky Linux
9.8 VM passed the installed-host recovery sequence, then remained healthy after
an abrupt QEMU-process kill/reboot and a MariaDB stop/start. These checks do
not establish real host power-loss or disk-exhaustion safety.

**Remaining Work:** Complete the Phase 7 failure matrix
- Multi-point kill/restart coverage across each critical operation
- Real ENOSPC during site publication/restore and recovery
- Host power loss / unclean disk-cache loss, beyond killing the QEMU process
- Managed-database outage during an operation and verified recovery
- 100+ deterministic run verification
- SLA measurement (< 5 seconds target)

**Next Action:** Extend the disposable-VM harness to exercise ENOSPC and database outages at operation boundaries, then complete repeatability and recovery-time measurements.

---

## Deployment Classification

**Current:** Operator Beta  
**Target for v1.0:** Single-Host Production Candidate

| Classification | When Ready | Notes |
|----------------|-----------|-------|
| **Development** | ✅ Now | Local development, full feature set |
| **Operator Beta** | ✅ Now (v0.7.0) | Single-host deployment; operator expertise required |
| **Single-Host Production** | After Phase 7 | Requires proof of failure recovery (Gate 5 Phase 7) |
| **Multi-Tenant Production** | v2.0+ | Tenant roles and host resource envelopes exist; requires audit segregation, an HA datastore, and cross-host job routing |

See [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) for deployment capabilities and limitations.

---

## Known Limitations (Operator Beta)

### ⚠️ Single-Host Constraints
- **No active-active HA:** Durable jobs tied to local database; no cross-host failover
- **No backup HA:** Scheduled backups with a required offsite copy, but no automatic failover to a standby host
- **Downtime on deploy:** Updates require restart; no canary/rolling deployment
- **Single point of failure:** All control-plane data on single host

### ⛔️ Not Ready For
- **Multi-tenant SaaS:** Requires audit segregation, an HA datastore, cross-host job routing, and a hostile-workload isolation boundary
- **High-availability:** No stateless API, no shared session store, no cross-region replication
- **Automated recovery:** Restore rehearsals run automatically at the archive level, but restoring a site is an operator action; there is no auto-remediation
- **Enterprise SLAs:** SLA tracking and escalation routing not implemented

---

## Migration Workflows Status

| Workflow | Status | Notes |
|----------|--------|-------|
| **cpmove** | ✅ Implemented & recovery-tested | Most complete; journaled activation |
| **WordPress Import** | ✅ Implemented; pending certification gate | WP-specific optimizations; recovery-tested |
| **Generic Archive** | ✅ Implemented; optional automated DB restore | Manual restore workaround if no credentials |
| **Git Deployment** | ✅ Implemented; git-based rollback | Application-driven; recovery-tested |
| **Backup Restore** | ✅ Implemented; staging + activation | Supports file, database, config restore |

All workflows use **journaled staged activation** — operations are staged in a private directory with recovery journals that enable rollback before production activation.

---

## Recent Completions (This Cycle)

✅ **Security improvements:**
- P1 state persistence error handling (77 lines of safe error helpers)
- 7 locations fixed to no longer silently ignore persistence errors
- Panel and worker systemd units now use filesystem, private-temp, kernel, and
  control-group protections; audited exposure is 6.7 MEDIUM on Rocky 9.8
- Native installs now route all privileged helper calls through the
  root-owned, peer-authorized Unix-socket broker; the panel and worker units
  enable `NoNewPrivileges`, SUID/SGID restrictions, private devices, and
  strict writable-path limits. Older installations must be upgraded to remove
  the transitional sudo policy.

✅ **Helper Layer foundation:**
- Broker foundation (types, validator, operations, and client; the transitional bridge was later removed)
- Fail-closed tests for unsupported mutation paths
- Supported operation paths and remaining gaps documented explicitly

✅ **Gate 5 local recovery evidence:**
- Durable journal pattern proven in production code
- 6 operations with atomic checkpoints
- Workflow integration testing (6 new tests)
- Phase 7 provider-neutral VM scaffold still requires provider commands; separate disposable QEMU VM checks now cover selected recovery paths

✅ **DBLocks (Sep 2026 rewrite):**
- Fixed 5 latent bugs in distributed lock implementation
- Fencing tokens now properly enforced
- Concurrent operation safety proven with regression tests

---

## Roadmap to v1.0

### Immediate (Week 1-2)
1. Extend VM tests to inject ENOSPC and managed-database outages during operations
2. Add multi-point failure coverage and repeatability/recovery-time measurements
3. Complete actual host power-loss validation before approving Gate 5 Phase 7

### Short-term
4. Publish the typed privileged-request model and retain the regression test
   proving that generic helper requests are rejected
5. Extend typed broker operations only through concrete request and response
   structures with resource-scoped validation and locking
6. Extend restore rehearsals to import databases and start the site on a
   staging hostname, so the measured recovery time is a true RTO

### Release (Before v1.0.0)
7. Run continuous failure injection in staging
8. Monitor 30 days in staging
9. Publish v1.0.0 Production Release

---

## What's Next?

**For Operators:** See [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) for deployment guidance.

**For Contributors:** See [ROADMAP.md](./ROADMAP.md) for product roadmap and [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md) for release gates.

**For Security Reviews:** See [SECURITY.md](./SECURITY.md) and [THREAT_MODEL.md](./THREAT_MODEL.md).

**For Architecture:** See [ARCHITECTURE.md](./ARCHITECTURE.md) and [docs/adr/](./adr/) for architectural decisions.

---

## Related Documents

**Status & Release:**
- [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md) — Authoritative release gates (read this for release approval)
- [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) — Deployment capabilities and limitations
- [ROADMAP.md](./ROADMAP.md) — Product roadmap (v0.1 → v2.0)

**Technical Details:**
- [HELPER_LAYER_COMPLETION_SUMMARY.md](./HELPER_LAYER_COMPLETION_SUMMARY.md) — Helper layer detailed status
- [ROOT_BROKER_INTEGRATION.md](./ROOT_BROKER_INTEGRATION.md) — Broker integration guide
- [GATE5_HARDENING_STRATEGY.md](./GATE5_HARDENING_STRATEGY.md) — Durable checkpoint pattern guide
- [GATE5_INTEGRATION_GUIDE.md](./GATE5_INTEGRATION_GUIDE.md) — Phase 4-6 integration details

---

**Questions?** Check the related documents above or open an issue.
