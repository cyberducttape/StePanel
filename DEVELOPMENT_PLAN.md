# StePanel production-readiness plan

**Status:** In progress; production remains **NO-GO**.  
**Release authority:** [`docs/V1_PRODUCTION_GATES.md`](docs/V1_PRODUCTION_GATES.md).  
**Operating promise under consideration:** operator-managed, single-host
PHP/WordPress only. HA and hostile multi-tenant isolation are out of scope.

This plan orders unfinished work by release risk. A milestone is complete only
when its acceptance evidence is attached to the exact release-candidate source
and the authoritative gate document is updated. Passing unit tests or a
successful one-off smoke does not close a host-certification gate.

## Milestones

### M0 — Restore green exact-source CI

**Priority:** Immediate release hygiene  
**Status:** Fix implemented and verified locally; hosted rerun awaits approval
to push.

- Correct the Playwright quota-headroom expectation to match the UI's current
  account-scoped upper-bound wording.
- Keep stateful customer-panel fixtures unique across Playwright workers and
  repeat attempts.

**Acceptance criteria**

- `node --check tests/e2e/critical-navigation.spec.js` passes.
- `CI=true ./scripts/e2e-ui-test.sh ./stepanel` passes from a freshly built
  binary.
- The stateful customer-panel spec passes twice concurrently with two workers,
  without account collisions.
- On the exact source commit, CI integration, CodeQL, documentation, and
  release-evidence checks complete successfully. Any failed job is diagnosed
  from its log/trace and fixed before starting another milestone.

### M1 — Complete installed-host failure recovery (P0)

**Status:** Open; requires an accessible disposable KVM host/runner.  
**Source of truth:** Gate 5 and `docs/REAL_HOST_FAILURE_MATRIX.md`.

Run the installed native systemd stack on each supported OS/filesystem, and
exercise process/broker/worker interruption, database outage, physical or
uncushioned host power loss, and ENOSPC during backup publication, archive
import, file restore, and database restore. Repeat each required
operation/fault combination 20–50 times. Preserve the exact source SHA, guest
and host configuration, injection point, journal/job/database/filesystem
state, service health, and successful post-recovery invariants.

**Acceptance criteria**

- Every required matrix cell has repeatable results on the release candidate;
  no run is counted if the source or environment is not recorded.
- Each interrupted operation deterministically completes, rolls back, or
  presents a documented operator-intervention state; no ambiguous success,
  orphaned active stage, or unreconciled service/database/filesystem state.
- ENOSPC leaves the prior live site recoverable and does not damage unrelated
  sites; host loss is tested without relying on host page-cache survival.
- `docs/V1_PRODUCTION_GATES.md` links reviewed result artifacts for each
  closed gate. Missing hardware or runner capacity remains an explicit block,
  not a pass.

### M2 — Certify application recovery and supported runtime matrix (P0)

**Status:** Partial; existing six-combination results are not all from the
current release candidate.

On a clean disposable destination, restore a real WordPress site including
files and a populated database, configure PHP-FPM and the virtual host, then
verify database consistency and a database-backed HTTP response. Run against
each runtime combination the project intends to support and retain evidence
for restore steps, HTTP result, measured recovery time, and excluded state.

**Acceptance criteria**

- Exact release-candidate source passes the published supported matrix on all
  listed OS/web-server/database combinations.
- Application-level proof is distinct from archive integrity and fails if
  database import, PHP execution, configuration, or HTTP probing fails.
- Unsupported combinations and single-run limitations are explicit in
  installation and release documentation.

### M3 — Prove operational alert delivery (P0)

**Status:** Implemented; live delivery/outage evidence remains open.

Exercise configured webhook and SMTP endpoints on an installed host, including
endpoint outage, retry, restart, backlog recovery, redaction, and operator
visibility of undelivered/dead-letter events.

**Acceptance criteria**

- A real failed job/backup generates a durable alert and reaches each
  configured channel exactly as specified.
- A temporary endpoint outage produces visible pending/failure state and
  successful delivery after recovery without duplicate harmful actions.
- Evidence records endpoint type (secrets redacted), timestamps, retries, and
  alert state; operational runbooks identify who responds and how.

### M4 — Publish a measured capacity envelope (P0)

**Status:** Open; current figures are targets, not claims.

Benchmark a named supported host with representative PHP traffic, databases,
backups, migrations, and control-plane reads/writes. Measure CPU/RAM, disk and
inode pressure, SQLite/WAL growth and lock waits, queue latency, job backlog,
and customer HTTP latency. Include restart and recovery behavior at the target
load.

**Acceptance criteria**

- Each published site/domain/account/concurrency limit is backed by a
  reproducible sustained run on a supported OS/filesystem/storage profile.
- Report p50/p95/p99 API and job latency, resource ceilings, SQLite metrics,
  workload, duration, failures, and recovery results.
- Remove or clearly label any target that lacks evidence; do not extrapolate
  from synthetic session/API-only benchmarks.

### M5 — Certify upgrade, rollback, and security lifecycle (P0)

**Status:** Partial; existing N-1 evidence covers selected prior versions and
environments.

Test clean install, supported N-1 upgrade, interruption at schema and service
activation boundaries, installer rollback, snapshot-based downgrade, and
populated installations on every supported platform. Complete independent
security review and verify the documented disclosure, patch, signing, and
credential-rotation procedures.

**Acceptance criteria**

- Every advertised source-to-target upgrade path passes on the release
  candidate, with schema compatibility, outstanding jobs, secrets, and
  rollback/recovery evidence recorded.
- Independent security findings are triaged and blockers resolved; release
  artifacts have verifiable signing provenance.
- Support matrix, API compatibility policy, security support window, and
  operator runbooks are published and tested against the release candidate.

### M6 — Reconcile release evidence and approve or reject 1.0

**Status:** Repeated each release candidate.

**Acceptance criteria**

- Exact-source `release-evidence` report has no stale or non-equivalent CI
  results and includes all reviewed host evidence.
- Every P0 row in `docs/V1_PRODUCTION_GATES.md` is closed with direct evidence;
  the overall decision is explicitly changed to GO by review.
- If any criterion remains unmet, preserve NO-GO and publish the limitations.

## Current sequencing

1. M0's local fix and tracking documents are committed. Obtain approval before
   pushing or dispatching remote workflows so hosted exact-source checks can
   close M0.
2. Obtain an accessible disposable KVM certification host/runner and execute
   M1 before broader production claims.
3. Complete M2 and M3 with real installed services while retaining failure
   evidence.
4. Measure M4 and complete M5; then perform the M6 exact-source audit.

