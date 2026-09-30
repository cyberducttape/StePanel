# StePanel Project Status

**Version:** v0.7.0 (Operator Beta)  
**Last Updated:** 2026-09-30
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
| **Gate 1** | One Lifecycle Authority | ✅ COMPLETE | None |
| **Gate 2** | Cross-Process Lock Enforcement | 🔄 PARTIAL (lock layer complete) | Full workflow interruption acceptance |
| **Gate 3** | Capability Reporting | ✅ COMPLETE | None |
| **Gate 4** | Automated Archive Database Restoration | ✅ COMPLETE | None |
| **Gate 5** | Failure Recovery Testing | 🔄 PARTIAL | Multi-point failure coverage, real disk-exhaustion and host power-loss testing |
| **Gate 2 extension** | Interrupted Workflow Acceptance | 🔄 OPEN | Full conflicting-workflow recovery evidence |

**Overall:** Operator Beta gates are in place; Gate 5 Phase 7 remains open for production approval.

### Verified hardening since the previous status update

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
- Scheduled-task termination and Git deploy-key generation, retrieval, and
  deletion use typed root-broker requests; corresponding generic helper actions
  are rejected. The wider helper-callsite migration remains incomplete.
- The four reported CodeQL alerts are resolved; CodeQL reported zero open
  alerts on `bf429a3`. CI and the installer matrix passed on that commit; the
  security fuzz workflow passed on `d1d3032`. The offsite-probe cleanup fix at
  `3cfbded` is locally tested but still awaits hosted validation. The
  quota-enabled installation/ENOSPC VM gate remains unverified until its
  current hosted run succeeds.

---

## Key Subsystem Status

### Helper Layer Modernization (foundation complete; migration in progress)

**Status:** Foundation complete; Phase 4 callsite replacement is in progress

- ✅ Phase 1: Design & Foundation (100%)
- ✅ Phase 2: Broker foundation and validation (100%)
- ✅ Phase 3: Integration Testing (100%)
- 🔄 Phase 4: Callsite Replacement (Foundation ready, implementation pending)

**What it is:** Replace 14 shell scripts (1200 lines, high security consequence) with a typed Go root broker. The broker centralizes input validation and fails closed for mutation paths that are not implemented yet; it is not a claim that every helper operation is available.

**Deliverables:** 
- `internal/rootbroker/` — Broker types, validation, supported operations, client, and explicit unsupported-operation responses
- Root-broker tests cover validation and fail-closed mutation behavior
- Integration documentation and migration guide
- `broker_bridge.go` — App integration convenience wrapper

**Status:** Production app/proxy/site/vhost/runner/Git mutation wrappers use the root broker, and TLS certificate issuance now uses a dedicated typed broker request with no separate certbot sudo grant. Database streaming restore/dump still uses dedicated helper paths, so the broker is not yet a complete replacement for every shell helper. Database helper grants and remaining streaming callsites are still migration work.

**Next Action:** Begin Phase 4 callsite replacement in v0.8.0 or v1.0.x release cycle.

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
| **Multi-Tenant Production** | v2.0+ | Requires RBAC, quotas, audit isolation (not in scope) |

See [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) for deployment capabilities and limitations.

---

## Known Limitations (Operator Beta)

### ⚠️ Single-Host Constraints
- **No active-active HA:** Durable jobs tied to local database; no cross-host failover
- **No backup HA:** Manual backup exports; no automatic failover to standby
- **Downtime on deploy:** Updates require restart; no canary/rolling deployment
- **Single point of failure:** All control-plane data on single host

### ⛔️ Not Ready For
- **Multi-tenant SaaS:** Requires RBAC per customer, audit segregation, resource quotas
- **High-availability:** No stateless API, no shared session store, no cross-region replication
- **Automated recovery:** Recovery drills are manual; no auto-remediation without human intervention
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
- `NoNewPrivileges`/SUID restrictions remain off by design because allow-listed
  root helper calls require sudo elevation

✅ **Helper Layer foundation:**
- Broker foundation (types, validator, operations, client, and bridge)
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

### Short-term (Week 3-4)
4. Begin Phase 4 callsite replacement for helper layer
5. Deploy broker alongside shell scripts (no app changes)
6. Monitor for issues in controlled staging

### Medium-term (Month 2-3)
7. Replace first batch of callsites (20% of usage)
8. Validate consistency and performance
9. Prepare v1.0.0 release candidate

### Release (Before v1.0.0)
10. Replace remaining callsites (80% of usage)
11. Run continuous failure injection in staging
12. Monitor 30 days in staging
13. Deploy to production
14. Publish v1.0.0 Production Release

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
