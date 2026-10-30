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
**Status:** CI integration, CodeQL, documentation, browser E2E, and release
evidence passed on exact source `c91a88b`; the installed-host smoke was on the
earlier source `79e1de4`. The `c91a88b` workflow evidence is recorded in
[`docs/lab-results/2026-10-30-current-source-almalinux-install-79e1de4.md`](docs/lab-results/2026-10-30-current-source-almalinux-install-79e1de4.md).
This closes exact-source validation only; production recovery gates remain
open.

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

### M0a — Bound long-running restore and API-token behavior (P1)

**Status:** Implemented and locally verified; exact-source CI, CodeQL,
documentation, and browser E2E passed on `79e1de4`. Installed-host smoke
`41019722358` passed one AlmaLinux 9/Caddy/MariaDB install and recovery-matrix
iteration; it is not a repeat cohort or power-loss certification.

Use operation-specific async deadlines that cover the corresponding helper
timeout and cleanup, renew durable leases throughout long work, and propagate
cancellation into offsite transfer. Bound API-token, account, and trusted-source
rate-limit state with LRU eviction, distinct initial bursts and sustained
rates, and no capacity-based rejection of otherwise-valid tokens.

**Acceptance criteria**

- Restore timeout classes exceed the full helper budget plus an explicit
  compensation reserve; offsite restore includes its configured transfer
  deadline.
- A restore test spans multiple short lease periods, completes without
  cancellation, and proves the durable lease was renewed. Canceled staging
  restore tests preserve the existing recovery/compensation contract.
- Limiter tests prove burst and refill behavior, account/source aggregation,
  bounded cache size, LRU eviction, and successful concurrent admission while
  full. Race-enabled targeted tests are clean.
- `make check` passes on the exact local source and the changelog/progress
  record the evidence and any remaining host-certification limits.

### M0b — Report effective security posture and extract typed operation paths (P1/P2)

**Status:** Security reporting and typed services for Node tooling and Python
deployment are implemented. Remaining site-operation routes still replay
HTTP handlers and are not considered migrated.

Latest Python extraction verification: full `make check`, focused Python
authorization/deployment tests, and the focused race test pass. This closes
the local regression gate for this slice only; it does not close host
certification or the remaining typed-operation work.

Do not infer effective Apache authorization from isolated directive text.
Report managed configuration discovery separately from runtime enforcement.
For durable site operations, progressively move validation, authorization,
mutation locking, and host changes behind typed commands and shared services;
the queue worker should dispatch a typed command rather than synthesize an HTTP
request.

**Acceptance criteria**

- Security API and operator dashboard show `configuration_status` separately
  from `enforcement_status`; a present `Require ip` directive never produces
  a pass absent an effective runtime access test.
- Typed operations validate the same command schema at admission and worker
  execution, rechecks current ownership and token/role scope, and executes
  without HTTP request/response replay. Tests prove the worker does not call
  the route handler. Node tooling and Python deployment currently satisfy this
  criterion individually.
- Migrate each remaining route in focused changes, preserving audit, lock,
  validation, retry, and recovery contracts. Node tooling, Python deployment,
  Composer install, and sandboxed runner builds now use typed worker services.
  Remaining routes still replay handlers; remove the fallback only after every
  registered route has a tested typed service.
- Installed Apache runtime tests from allowed and denied sources are required
  before reporting effective IP enforcement as verified.

### M0c — Exportable cPanel migration compatibility evidence (P1)

**Status:** Feature implemented. The exact-source `c91a88b` CI integration
passed the serialized Go suite, race tests, and full browser E2E, including the
migration journey; CodeQL, documentation, and release evidence also passed.
The earlier isolated complete-browser timeout was not reproduced in the
passing hosted run.

The cpmove preflight reports bounded domain/document-root mappings, SQL dump
version evidence, Maildir structure, and explicit DNS/mail/TLS cutover work.
An optional read-only SSH scan compares source PHP extensions and host
inventory. The import result records observed post-migration checks and the UI
exports both preview and final evidence as JSON and Markdown.

**Acceptance criteria**

- Archive tests cover parsed domain maps, unknown/major-mismatch database
  versions, complete/incomplete Maildir structure, and post-import failure
  states.
- Missing source/runtime data is reported as unknown or warning; PHP CLI,
  staged mail, and archive metadata are not represented as PHP-FPM, delivery,
  or DNS verification.
- Dashboard preview exposes domain mappings, database checks, mailbox
  validation, and a DNS cutover checklist; optional SSH analysis uses the
  existing administrator-only, read-only job workflow.
- Preview and completed import evidence download as valid JSON and readable
  Markdown, without including SSH credentials.
- Focused Go tests, JavaScript syntax/unit/E2E checks, full `make check`, and
  exact-source CI pass. This milestone does not close installed-host recovery
  or production certification gates.

### M0d — Evidence-backed site incident diagnosis (P1)

**Status:** Implemented. Prior exact-source CI integration
([41026353462](https://github.com/cyberducttape/StePanel/actions/runs/41026353462)),
CodeQL ([41026353439](https://github.com/cyberducttape/StePanel/actions/runs/41026353439)),
documentation ([41026353453](https://github.com/cyberducttape/StePanel/actions/runs/41026353453)),
release evidence ([41028522602](https://github.com/cyberducttape/StePanel/actions/runs/41028522602)),
and browser E2E passed on `c91a88b`. The earlier hosted installed-host smoke
`41019722358` passed one AlmaLinux 9/Caddy/MariaDB recovery-matrix iteration
and measured a 7.309-second WordPress recovery proof on `79e1de4`; it is not
current-source host evidence. The repeated recovery gate remains open.
The diagnostics follow-up passes `make check`, focused Go tests, JavaScript
syntax checks, the focused disposable Chromium journey (1/1), and exact-source
hosted CI run [41033987875](https://github.com/cyberducttape/StePanel/actions/runs/41033987875)
(Go coverage, race, recovery/ENOSPC, database restore, container, and UI E2E).
CodeQL [41033987807](https://github.com/cyberducttape/StePanel/actions/runs/41033987807)
and documentation [41033988021](https://github.com/cyberducttape/StePanel/actions/runs/41033988021)
also passed. The exact-source installed-host smoke
[41033987854](https://github.com/cyberducttape/StePanel/actions/runs/41033987854)
is still running; production-host certification remains open.

The current diagnostics refinement distinguishes low free bytes from low free
inodes in filesystem evidence, and classifies database probe failures into
bounded causes such as connection refusal, authentication rejection, socket
unavailability, and timeout. Raw helper output remains hidden. Focused Go tests
and the modified browser journey pass locally, as do `make check` and the
focused disposable Chromium journey. Hosted exact-source CI remains the next
verification step.

Prometheus alert descriptions now report their evaluated values and triggering
thresholds/windows, point to relevant job, readiness, database, storage, or
outbox evidence, and suggest a safe next action. Rule syntax and rendered
annotation tests pass with promtool. The site incident view combines PHP-FPM
worker-memory/saturation evidence, database probes, byte/inode pressure,
certificate renewal history, backup freshness, and resource enforcement. These
per-site checks are snapshot-based; continuous Prometheus series for every one
of those signals remain an instrumentation gap.

Correlate PHP-FPM saturation and historical pressure, systemd memory/task
limits, host byte/inode capacity, database-engine connectivity, TLS metadata
and failed certificate issuance jobs, scheduled-backup failures/staleness,
recent errors, and recovery evidence. Each displayed finding must state its
observed evidence and a safe next action. Probes must report unavailable or
unverified where they cannot establish application-level behavior.

**Acceptance criteria**

- Site diagnostics report timestamped byte and inode measurements for the site,
  backup, and configured database filesystems, and explicitly identify which
  resource has low headroom or unavailable probes. A zero-sized filesystem
  measurement is not divided or presented as a percentage.
- Database connectivity is based on a completed restricted engine query and is
  clearly distinguished from a per-site WP-CLI check using the site's
  configured credentials; the latter runs as that site identity and is bounded
  and briefly cached. Failed engine probes include a bounded failure class and
  a safe diagnostic detail without exposing raw helper output.
- Failed certificate issuance jobs and enabled backup schedules with repeated
  failure or a recovery point older than twice the interval appear as findings
  with timestamps and measured evidence.
- A newly enabled schedule is not reported stale before its first due run; a
  schedule with no successful backup becomes stale only after its initial run
  has been attempted or its first due window has expired by the documented
  grace interval.
- Prometheus exposes per-site enabled-schedule success presence, consecutive
  failures, and latest-attempt age. Alerts identify the site and include the
  failing count or explicit missing-success evidence; enabled schedules that
  have never succeeded are alertable without mislabeling disabled schedules.
- The overview reports cgroup memory/task usage against enforced limits and
  preserves existing PHP-FPM saturation evidence.
- Site-tree byte and inode usage at 90% or more of a configured account
  allowance is promoted to a warning; at or above the allowance it is
  critical. Evidence states that this is a tree measurement and an upper-bound
  headroom estimate for a potentially shared account quota.
- PHP-FPM saturation evidence includes active/max workers, queue depth, slow
  request counters, and the sample timestamp. Failed certificate alerts name
  the durable job ID and time so the authorized job record can be opened.
- Certificate job evidence is queried as the latest durable job per domain;
  activity on one domain cannot hide another domain's latest result. If the
  durable job store is unavailable, diagnostics report renewal history as
  unknown rather than implying a clean renewal state.
- Focused Go tests, JavaScript syntax/unit checks, and `make check` pass; each
  rendered alert includes a diagnosis, supporting evidence, and safe action.
- A browser E2E regression verifies the incident view renders measured storage
  pressure, per-site WordPress DB authentication failure, stale/failed backup
  evidence, and a failed certificate job; the seeded full UI suite passes.

### M0e — Continue typed durable operation extraction (P1)

**Status:** In progress. Node tooling, Python deployment, Composer install,
and sandboxed runner builds have typed worker executors; the remaining durable
site-operation routes still replay HTTP handlers.

**Acceptance criteria**

- Every migrated route uses the same domain service for HTTP and worker entry,
  repeats current authorization at execution, and validates the typed command
  at admission and execution.
- Worker code does not synthesize HTTP requests for migrated routes; tests
  verify lock, audit, state-persistence, cancellation, and stale-token behavior
  remain correct.
- The handler-replay fallback is removed only once all registered routes have
  equivalent services and regression coverage.
- Focused Go tests and `make check` pass for each extraction; host-level
  certification remains a separate gate.

### M0f — Preserve session-store failure semantics (P1)

**Status:** Implemented locally; hosted CI pending. The durable session
registry now returns storage errors distinctly from absent/revoked sessions,
and authenticated HTTP requests surface store failures as 503.

**Acceptance criteria**

- A missing, expired, or revoked session remains an authentication failure;
  a database read failure is reported as service unavailable.
- Existing boolean session checks retain their compatibility behavior for
  non-HTTP callers, while request authentication uses the error-aware API.
- Registry and HTTP regression tests pass, including a closed-database case;
  `make check` and exact-source CI pass.

### M1 — Complete installed-host failure recovery (P0)

**Status:** Open. Exact-source hosted AlmaLinux 9/Caddy/MariaDB run
`41019722358` on `79e1de4` passed one installed recovery-matrix iteration and
the WordPress application proof (7.309 s); see the reviewed
[result](docs/lab-results/2026-10-30-current-source-almalinux-install-79e1de4.md).
Exact-source run `41033987854` on `c155752` also passed one iteration and its
installed WordPress proof (7.440 s); see
[`2026-10-30-almalinux9-install-41033987854.md`](docs/lab-results/2026-10-30-almalinux9-install-41033987854.md).
One Rocky Linux 9 KVM iteration against older source
`1348611198dbb72b5a7099a86f33bf2de43e6012` completed with 123 smoke/matrix
passes; see the reviewed [run report](docs/lab-results/2026-10-30-rocky9-kvm-recovery-matrix-1348611.md).
It is not counted as current-source evidence. Neither run establishes physical
power-loss durability or meets the 20–50 repeat requirement.
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

The API lifecycle policy is now published in
[`docs/API_COMPATIBILITY.md`](docs/API_COMPATIBILITY.md). Candidate validation
against generated SDKs and a supported previous API major remains part of the
acceptance criteria below.

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

1. Continue typed service extraction from high-risk durable operations in
   focused changes. Keep the handler-replay fallback until each route has
   equivalent validation, authorization, audit, locking, and recovery tests.
2. Obtain an accessible disposable KVM certification host/runner and execute
   M1 before broader production claims.
3. Complete M2 and M3 with real installed services while retaining failure
   evidence.
4. Measure M4 and complete M5; then perform the M6 exact-source audit.

### Guided site lifecycle experience (product work; not a release-gate closure)

**Status:** Implemented in the site workspace, with browser regression coverage.

The per-site guide connects existing cPanel/WordPress import, site validation,
deployment history, domain and TLS troubleshooting, recovery points and
rehearsals, staging clone/promotion, logs, PHP-FPM diagnostics, and the
administrator termination preview. New site creation opens the guide after
the creation job completes.

Recovery status identifies the most recent archive-validated backup, the last
restore attempt, the exact backup and evidence level of the last passing
rehearsal, and per-backup offsite upload/restore records. It explicitly avoids
equating an upload record with current remote reachability or application
health. A green recovery confidence state requires proof for the current
newest backup; evidence only for an older backup is degraded and identifies
the exact older recovery point.

Acceptance criteria:

- Each step links to the existing authorized workflow and does not bypass its
  preview, job, or confirmation contract.
- Missing data is shown as unknown/unavailable; process state, `wp-config.php`,
  database inventory, or Caddy certificate metadata is never presented as a
  successful live application/TLS check.
- WordPress WP-CLI status and PHP-FPM/log evidence render with authorization
  checks and bounded API reads.
- The browser regression covers steps, evidence limits, WordPress status,
  logs, staging navigation, and both passing and failing recovery evidence;
  backend recovery-status tests verify the exact offsite timestamps and UI
  accessibility E2E passes.
- This UI does not close the independent installed-host, application recovery,
  or capacity certification gates above.

