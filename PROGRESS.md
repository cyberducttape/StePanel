# Production-readiness progress

## Independent audit-checkpoint key rotation — 2026-10-31

- Added a dedicated `STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY`, separate from the
  local audit-chain HMAC and session key. New checkpoints use signature
  version 2; version 1 checkpoint verification remains supported.
- Durable checkpoint publication now loads a bounded historical keyring from
  `STEPANEL_AUDIT_CHECKPOINT_KEYRING_DIR`. It rejects invalid IDs, excessive
  key counts/sizes, symlink/non-regular key files, weak key lengths, and
  group/other-writable storage. A stored old checkpoint remains verifiable
  after the active checkpoint signing key changes, without changing the local
  audit-chain key.
- Installer and runtime configuration propagate the optional key/keyring
  settings; installation docs describe root ownership, service-group read
  access, permissions, and the checkpoint-only rotation procedure. Removed a
  duplicate M0l section from `DEVELOPMENT_PLAN.md` and retained its open
  destructive-operation idempotency, journal, and crash-test criteria.
- `make check` passes, including API artifacts, shell contracts, `go vet ./...`,
  and the complete Go suite. Audit-package coverage is 95.0%, its race suite and
  the root checkpoint/config race tests pass, as do `bash -n install.sh`, the
  GitHub Action SHA check, and `git diff --check`. The first hosted CI attempt
  on `e8a1889` found a test-fixture permissions mismatch caused by runner umask;
  the fixture now explicitly sets modes. Exact-source CI integration
  [41186123944](https://github.com/cyberducttape/StePanel/actions/runs/41186123944),
  CodeQL [41186123895](https://github.com/cyberducttape/StePanel/actions/runs/41186123895),
  documentation [41186123910](https://github.com/cyberducttape/StePanel/actions/runs/41186123910),
  and release evidence
  [41186233296](https://github.com/cyberducttape/StePanel/actions/runs/41186233296)
  pass on `1353378`. The installed-host smoke
  [41185781415](https://github.com/cyberducttape/StePanel/actions/runs/41185781415)
  was later canceled when a newer source was pushed and is not counted as
  evidence. The exact final source `1e6db185e5fc77e034280e9182d30e47c562c190`
  passed CI integration [41187605734](https://github.com/cyberducttape/StePanel/actions/runs/41187605734),
  CodeQL [41187605838](https://github.com/cyberducttape/StePanel/actions/runs/41187605838),
  documentation [41187605775](https://github.com/cyberducttape/StePanel/actions/runs/41187605775),
  and one AlmaLinux 9/Caddy/MariaDB installed-host smoke
  [41187605802](https://github.com/cyberducttape/StePanel/actions/runs/41187605802).
  That smoke completed one recovery-matrix iteration and an application-level
  WordPress restore proof (7.333 s RTO); it does not configure a checkpoint
  remote or exercise remote publication. See the
  [reviewed host result](docs/lab-results/2026-10-31-almalinux9-install-1e6db18.md).
  Remote-provider append-only and outage/recovery evidence remains open;
  changing `STEPANEL_AUDIT_KEY` remains unsupported because the event chain
  has no per-record key IDs.

## Durable remote audit-checkpoint publication — 2026-10-31

- Added an optional daily scheduler that admits one `audit.checkpoint` durable
  job per UTC day. The worker signs the verified audit-chain head and binds it
  to its durable job ID.
- The job persists the exact signed checkpoint before remote I/O. It uploads
  with rclone immutable semantics, reads the remote object back under a 64 KiB
  cap, compares exact bytes, verifies the signature, then records a signed
  `audit.checkpoint.published` outcome. A simulated upload that succeeds
  remotely but returns an error is resolved through read-back.
- Added target/key-ID validation, installation environment propagation, and
  installation documentation. The destination is opt-in; operators must use
  separately administered storage to establish independent evidence.
- Local evidence so far: focused publication/config tests passed; race tests
  passed for `internal/audit`, `internal/rootbroker`, and `internal/operations`.
  Full `make check` passed, as did focused race tests and the audit package
  coverage floor (95.3%). Exact-source CI [41182651721](https://github.com/cyberducttape/StePanel/actions/runs/41182651721),
  CodeQL [41182651731](https://github.com/cyberducttape/StePanel/actions/runs/41182651731),
  and documentation [41182651795](https://github.com/cyberducttape/StePanel/actions/runs/41182651795)
  passed on `ccb58c9`. Its exact-source installed-host smoke
  [41182651772](https://github.com/cyberducttape/StePanel/actions/runs/41182651772)
  passed one AlmaLinux 9/Caddy/MariaDB install and recovery run. This does not
  establish repeated crash recovery or remote checkpoint-provider behavior.
- Remaining gaps: provider-side append-only policy verification, remote
  outage/recovery evidence, and release-level proof that the destination is
  independent. Local audit-chain key rotation remains unsupported. M0l and
  production readiness remain open.

## Signed audit-checkpoint artifact primitive — 2026-10-31

- Added an audit checkpoint format binding a verified event-chain sequence,
  final hash, UTC timestamp, key ID, and operation ID. Creation serializes with
  the audit writer, verifies the complete chain and authenticated state, and
  refuses empty or tampered logs. Independent verification uses a key-ID
  indexed historical keyring and rejects missing keys or modified fields.
- Added tests for creation, JSON round-trip, historical key lookup, tampering,
  empty logs, and corrupted chains. `go test ./internal/audit`,
  `go test -race ./internal/audit`, and `go vet ./internal/audit` passed.
- Added `FuzzNormalizeName` to the scheduled fuzz workflow; a 60-second local
  run passed 1.94 million executions with no failure.
- Hosted CI on `53593d7` passed browser E2E, CodeQL, documentation, and release
  evidence, but failed the enforced audit-package coverage floor (94.1% vs
  95%). Added checkpoint invalid-field and identity rejection cases; package
  coverage is now 95.2%, and the exact coverage gate passes locally. `make
  check` passed on the follow-up source. Exact source `d5fe183` then passed CI
  [41179096619](https://github.com/cyberducttape/StePanel/actions/runs/41179096619),
  CodeQL [41179096643](https://github.com/cyberducttape/StePanel/actions/runs/41179096643),
  documentation [41179096618](https://github.com/cyberducttape/StePanel/actions/runs/41179096618),
  and release evidence [41179959544](https://github.com/cyberducttape/StePanel/actions/runs/41179959544).
  CI's coverage, full Go, race, recovery, tmpfs ENOSPC, database, deployment,
  and UI checks all passed. The initial coverage failure is resolved.
- Exact source `eaa51a81fd011f1539dc4a905de56b87315b6b9e` passed complete CI
  [41180461643](https://github.com/cyberducttape/StePanel/actions/runs/41180461643),
  CodeQL [41180461695](https://github.com/cyberducttape/StePanel/actions/runs/41180461695),
  documentation [41180461626](https://github.com/cyberducttape/StePanel/actions/runs/41180461626),
  release evidence [41180486632](https://github.com/cyberducttape/StePanel/actions/runs/41180486632),
  and the three-repeat/recovery run [41180463963](https://github.com/cyberducttape/StePanel/actions/runs/41180463963).
  The CI integration included the full race suite, recovery and tmpfs ENOSPC
  drills, MySQL/PostgreSQL restore checks, static analysis, container checks,
  and browser E2E. Exact-source installed-host smoke
  [41180461647](https://github.com/cyberducttape/StePanel/actions/runs/41180461647)
  remains in progress.
- Added a scheduled/manual reliability workflow for `go test ./... -count=3`
  and the isolated recovery-drill script, each with explicit test limits. Its
  first exact-source hosted run [41180463963](https://github.com/cyberducttape/StePanel/actions/runs/41180463963)
  passed both steps. The existing PR integration also runs the full race suite
  and separate recovery and ENOSPC steps. The workflow passes action-pin and
  release-tree checks locally.
- Local exercise of the scheduled commands passed: the three-repeat full suite
  passed all packages (root package 225.7 s). All four recovery-drill groups
  passed; using the pre-existing Go cache reduced the first drill from 150 s
  to 2 s, with the remaining drills at 2–3 s. Removed the script's forced cold
  `/tmp` cache so CI and operators reuse their configured Go build cache.
- This is a format/verification primitive only. It is not yet scheduled or
  journaled as a durable job, not published to independent storage, and has no
  verified remote acknowledgement or production key-rotation policy. M0l stays
  open and production remains **NO-GO**. On this worktree, final `make check`
  passed (API artifact check, shell contracts, `go vet ./...`, and all Go
  packages), and `FuzzNormalizeName` passed a 60-second run. The checkpoint
  artifact and fuzz target are covered by the exact-source hosted checks above;
  durable off-host publication remains unimplemented.

## Per-claim job lease fencing — 2026-10-31

- Added a random, per-claim fencing token to durable jobs and a schema migration
  for existing control-plane databases. The token is separate from the worker
  name and is omitted from JSON/API output.
- Claim renewals, progress and payload checkpoints, failure transitions, and
  completion now use a SQLite compare-and-swap requiring the same token and an
  unexpired lease. The generic completion helper now uses the same fenced
  transition instead of an unconditional upsert.
- Added regressions proving expired claims cannot finish before or after a
  replacement claim, including replacement by the same worker ID. Added a
  separate test for the generic completion helper and updated claim tests and
  benchmark call sites to use claim tokens.
- Verification: focused durable-job suite passed; focused job race tests,
  `internal/rootbroker` race tests, and `internal/operations` race tests passed.
  Final `make check` passed (API artifact check, shell contracts, `go vet`, and
  all Go packages). `GOMAXPROCS=2 go test -p 1 -timeout 10m ./... -count=3`
  passed all packages in 226.5 seconds for the root package. The full
  `GOMAXPROCS=1 go test -race -p 1 -timeout 15m ./...` passed all packages in
  427.1 seconds for the root package. After those full runs, startup
  reconciliation was changed to update only orphaned lease rows with a
  compare-and-swap rather than rewriting the entire in-memory job cache; the
  regression, focused race tests, and final `make check` passed on that later
  source. The full repeat and race suites were not rerun after this narrow
  reconciliation change. Exact-source hosted CI has not yet run.
- Remaining limits: this prevents stale workers from committing SQLite job
  state, but cannot roll back an external mutation already performed after a
  lease was lost. Operation-specific idempotency, explicit
  `committing`/`reconciliation_required` states, complete per-operation crash
  journals, and independent signed audit-checkpoint replication remain open
  under M0l in `DEVELOPMENT_PLAN.md`. Production remains **NO-GO**.

## Broker-side tenant check for privileged file operations — 2026-10-30

- The sealed site capability now carries the durable tenant owner alongside
  the site ID. Customer file-store requests pass both values to the root broker.
- Before resolving account placement or invoking `stepanel-filectl`, the
  broker requires an exact `(site, tenant)` match in `tenant_sites`. Missing
  ownership state, missing tenant claims, and cross-tenant claims fail closed.
  Root-broker startup now validates the ownership table schema.
- Added a regression that directs a validly shaped request at another tenant's
  placed site and verifies the helper is never invoked. Added validation
  coverage for missing tenant context and capability propagation.
- Local validation: `make check` passed, including API artifact validation,
  shell contracts, `go vet ./...`, and `GOMAXPROCS=2 go test -p 1 -timeout
  10m ./...`. Focused race tests passed for `internal/rootbroker`,
  `internal/sitefiles`, `cmd/stepanel-root`, and root-package site-file and
  tenant authorization tests. Hosted CI has not yet run on this change.
- Scope limitation: The panel process remains trusted. The broker independently
  checks the tenant-to-site ownership mapping for file operations, but this is
  not cryptographic caller attestation against a fully compromised panel and
  does not yet re-evaluate every API-token scope for every broker operation.
  Hostile shared-kernel tenancy remains unsupported.

## TOTP, audit-key separation, and cPanel upload admission — 2026-10-30

- Database-backed TOTP replay reads now distinguish `sql.ErrNoRows` from real
  database errors and only compare unsigned counters when a valid persisted
  row exists. Added regressions for first-use success, duplicate rejection,
  cross-instance persistence, concurrent first use, corrupt negative state,
  and acceptance of the next adjacent counter window.
- The audit logger no longer uses `STEPANEL_SESSION_SECRET` when a dedicated
  audit key is missing. Root-package tests receive a separate test-only key by
  default, while explicit missing-key tests still clear it. Audit log paths
  must be regular files; this also prevents a directory from being renamed as
  a legacy log during first-write initialization.
- `RestoreCPMoveContext` now enforces `Config.MaxUpload` against both declared
  size and actual bytes copied, checks context cancellation while streaming,
  progressively reserves capacity, removes partial files, and releases its
  reservation on all return paths.
- `make check` passed after these changes. Focused TOTP/cPanel tests, audit
  package tests, audit outbox/crash-drill tests, focused root race tests,
  `internal/audit` race tests, and the action-pin check passed. The first full
  suite run identified tests depending on the old session-key fallback and a
  directory-as-audit-path edge; both were corrected before the passing run.
- Audit key IDs and a protected historical verification keyring are not yet
  implemented. In-place key rotation remains explicitly unsupported. Legacy
  token access still follows the documented fixed hard cutoff. A customer
  token regression now proves current role downgrade, member suspension, and
  revocation are enforced during the compatibility window; token
  reissuance/rotation coverage remains open. On exact commit `2497cd3`, hosted
  docs, UI E2E, and Go static analysis passed; full CI integration and the
  installed-host smoke were still running when recorded.

## Tenant capability and package-process containment — 2026-10-30

- Site file-store methods now require `sitefiles.SiteAccess`; the customer
  handler passes the private `AuthorizedSite` value created by the ownership
  resolver through every list/read/write/mkdir/rename/delete path. The local
  store rejects nil and empty capabilities. Existing cross-tenant handler
  tests continue to exercise denial before storage access.
- Composer, Node install/build, and Python pip package/build-backend commands
  now execute in transient systemd services with `KillMode=control-group`, a
  15-minute runtime cap, `TasksMax=256`, and the site's resource slice. Rootless
  container builds already used a bounded transient unit and container.
- The shared helper now sends SIGTERM to the helper process group before
  escalating to SIGKILL after 250 ms. The app helper traps that signal and
  explicitly stops its active transient package unit. A regression confirms
  a cooperative signal handler runs; detached descendants still require and
  are placed within the systemd cgroup.
- Durable jobs already persist actor and site data and revalidate current
  ownership before execution; immutable tenant-ID snapshots are not yet a
  common durable-job contract and remain an open authorization hardening item.
- Added a shell contract regression and included it in `make check` and CI;
  updated security documentation to distinguish cgroup process containment
  from hostile-tenant isolation. A per-operation cgroup smoke on real
  installed systemd hosts is still required.
- Verification on this source: `make check` passed (API artifact checks, shell
  contracts, `go vet ./...`, and `GOMAXPROCS=2 go test -p 1 -timeout 10m ./...`).
  Focused `go test -race` passed for `internal/helper`, `internal/sitefiles`,
  and the changed root file-manager authorization tests. ShellCheck, the GitHub
  Action SHA pin check, and `git diff --check` passed. The complete root-package
  race suite was stopped after several minutes without a test failure; it
  remains unverified in this run. Commit `db18dd0` was pushed to `main`; at
  progress update time, exact-commit hosted CI, CodeQL, documentation, and
  installation-smoke workflows were queued or pending, so hosted CI remains
  unverified.
- The production systemd/cgroup behavior still needs an installed-host test
  with a deliberately detached child on every supported OS. This work does
  not establish hostile multi-tenant isolation or close that release gate.

## Authentication and audit-outcome hardening — 2026-10-30

- Centralized startup authentication policy. Missing credentials now stop
  production startup before routes are assembled; unauthenticated operation
  requires explicit `STEPANEL_ENV=development` and a loopback listener.
- `BeginSecurityAudit` now creates a unique operation ID and atomically stores
  both the initiation event and a pending operation record. `Completed`,
  `Failed`, and `Finish` return persistence errors. Terminal event and state
  transitions commit in one SQLite transaction; a failed write leaves the
  operation pending and does not rerun its host mutation.
- Added an administrator-only reconciliation API, evidence-required manual
  closure, 90-day bounded retention for terminal operation records, operational
  health visibility, a Prometheus gauge and critical alert, OpenAPI/API-guide
  documentation, and auth/audit failure-injection regressions.
- Verification: focused tests passed; focused race tests passed; `go vet
  ./...` passed; `GOMAXPROCS=2 go test -p 1 -timeout 10m ./...` passed all
  packages in 70.5 seconds; `make check` then passed on the final code,
  including API artifact validation, vet, and all packages in 68.6 seconds.
  The first full run exposed the new route missing from OpenAPI; the spec was
  updated and its contract test now passes. A subprocess integration test
  starts the actual `Main()` entry point with production mode and no
  credentials and confirms it exits before service startup. Rule YAML parses
  locally. `promtool` is unavailable in this shell, so rendered alert tests
  remain for hosted CI; exact-source CI and CodeQL are also pending.
- Production remains **NO-GO** pending the release gates in
  `docs/V1_PRODUCTION_GATES.md`; this code change is not a production
  certification.

## Exact-source CI restored — 2026-10-31

- Code commit `711948d` is pushed to `main` as Stephan Loesevitz
  `<stephan.loesevitz@gmail.com>` over SSH. Full CI integration passed on that
  exact commit. The current head `5e2ac8a` adds only changelog, plan, and
  progress documentation; its Documentation
  [41158199552](https://github.com/cyberducttape/StePanel/actions/runs/41158199552),
  CodeQL
  [41158199575](https://github.com/cyberducttape/StePanel/actions/runs/41158199575),
  and Release evidence
  [41158252216](https://github.com/cyberducttape/StePanel/actions/runs/41158252216)
  workflows passed. The docs-only delta does not change the tested Go or web
  application sources.
- Exact-source CI integration
  [41157044343](https://github.com/cyberducttape/StePanel/actions/runs/41157044343),
  CodeQL
  [41157044317](https://github.com/cyberducttape/StePanel/actions/runs/41157044317),
  documentation
  [41157044368](https://github.com/cyberducttape/StePanel/actions/runs/41157044368),
  and release evidence
  [41157205535](https://github.com/cyberducttape/StePanel/actions/runs/41157205535)
  all passed. CI includes Go tests, race tests, recovery drills, tmpfs ENOSPC,
  alert-rule tests, static analysis, vulnerability scanning, container and
  deployment checks, and browser E2E.
- Production remains **NO-GO**. The 20-repeat installed-host cohort
  [41155137017](https://github.com/cyberducttape/StePanel/actions/runs/41155137017)
  and a separate local Rocky 9/Caddy/MariaDB KVM run remain active; neither is
  counted as completed certification yet.

## Evidence-led operational alerts — 2026-10-30

- Clarified fired-alert messages for PHP-FPM saturation, database connectivity
  probe failures, low filesystem bytes/inodes, failed certificate issuance,
  and stale scheduled backups. Messages include the observed value or failure
  reason, the supporting metric/sample or durable job evidence, and a safe
  next diagnostic action.
- Updated Prometheus rendered-message expectations for each changed alert.
- Verified both rule syntax and rendered alert tests with Prometheus 3.5.0
  `promtool`: all 41 rules load and the complete alert test file passes.
- This improves operator diagnosis; it does not prove external notification
  delivery or application-level database connectivity for every site. Existing
  database metrics report the managed engine diagnostic probe and should not
  be interpreted as a per-application query/latency check.

## Audit fixes, failure-injection tests, and host evidence — 2026-10-30

- Reconciled the plan's M0f/M0g statuses against the hosted run: CI integration
  [41076494928](https://github.com/cyberducttape/StePanel/actions/runs/41076494928)
  passed the complete Go suite and UI E2E on `6ff12b6`, which includes both
  fixes and their regressions. The previous “hosted CI pending” labels were
  stale.
- **Runtime metrics on real hosts:** `systemctl show` prints
  `CPUQuotaPerSecUSec` as a timespan (`1.500000s` on systemd 259), so CPU
  quota utilization was never derived and the CPU quota alerts could not
  fire. Fixed in `d2d3e5a`/`2f9b0e2`; parsers are now tested against captured
  `systemctl show` output in `testdata/systemctl/`.
- **CI integration was red** from `05565dc` to `7a85b2c`: the CPU near-limit
  warning overlapped the saturation alert and failed `promtool test rules`,
  which also skipped govulncheck, staticcheck, and the container/Helm/
  Terraform checks for that period. CI integration, including those steps,
  passed on `7a85b2c` and `6ff12b6`.
- **Production-only failures in new workflows:** WordPress site creation and
  staging promotion took their database backups without the capacity ledger,
  which production refuses, so every production WordPress creation rolled
  back and every promotion refused (`bd508e9`, `11c0509`). A source guard now
  rejects ledger-less backup calls in panel code.
- **Other defects fixed:** SSH server inventory always reported incomplete
  data because the remote shell re-split the probe script (`c31a3ae`);
  staging clone and promotion did not hold the database locks used by drops
  and credential rotations (`2db17e8`, `fc97f7b`).
- **New tests:** staging promotion runs end to end against a temporary web
  root and fails at each of 14 steps (`STEPANEL_FAIL_AT=promote:*`); every
  failure must restore production files and configuration, leave no pending
  journal or stage, and drop the candidate database exactly when one was
  created. Mutation checks confirmed the cases fail when cleanup is removed.
- **N-1 upgrade smoke** had failed every job since `b6ce902`: the candidate
  installer stopped silently in its database version lookup. Guarded in
  `1ff3233`, which also makes the installer name any unexpected failing line;
  run [41066237188](https://github.com/cyberducttape/StePanel/actions/runs/41066237188)
  passed all six upgrade jobs.
- **Installed-host gate:** the six-host full recovery matrix shard
  `gate-1ff3233` ([41066287752](https://github.com/cyberducttape/StePanel/actions/runs/41066287752))
  passed on Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with Apache and
  Caddy. This is one repetition; it does not count toward the cohort.
- **Started** the 20-repetition installed recovery cohort on `6ff12b6`
  ([41155137017](https://github.com/cyberducttape/StePanel/actions/runs/41155137017));
  results are pending.
- **Not run:** the local `abrupt-loss-operations` KVM drill needs at least
  6 GiB available memory; this workstation had 3.9 GiB.

## Per-site CPU quota saturation evidence — 2026-10-30

- Extended the background runtime sampler to calculate per-site CPU-quota
  utilization from the delta in systemd `CPUUsageNSec` divided by the enforced
  `CPUQuotaPerSecUSec` over the observed interval. No value is emitted for
  unavailable probes, unlimited quotas, counter resets, or stale samples.
- Added warning and critical Prometheus alerts for sustained near-limit and
  saturated CPU quotas. Both identify the site, observed utilization interval,
  threshold, relevant sample/slow-request evidence, and a safe operator action.
- Added regression coverage for the counter calculation, invalid samples,
  metric rendering, and rendered warning/critical alerts. The focused Go tests
  passed. Local Prometheus rule validation could not run because `promtool` is
  unavailable in this shell. **Outcome:** hosted CI then failed `promtool test
  rules` on this source: the near-limit warning also fired at 100%. It was
  fixed in `7a85b2c` (see the entry above). The sampler also could not parse
  real systemd quota values; see `d2d3e5a` and `2f9b0e2`.
- This adds CPU quota pressure to the existing PHP-FPM, database, filesystem,
  certificate, backup, memory, and task alerts. It does not provide CPU PSI or
  kernel throttling counters; the measured quota utilization is an interval
  estimate, not proof of customer-visible latency.

## Full repository production gate — 2026-10-30

- `make production-gate` passed on exact source
  `cab5cd41b861cd90681a04752ad260cfe8ed1419`: `go vet`, all Go tests,
  57.9% total coverage and all package floors, complete race suite, recovery
  drills, control-plane benchmarks, 200-site mixed HTTP load, documentation,
  quota-contract fixtures, action pinning, assets, and shell syntax.
- Mixed-load results were 4.5 ms average / 25.1 ms p95 / 34.9 ms maximum;
  `/metrics` maximum was 25.2 ms. The 50-job batch benchmark measured 51.3 ms.
- The current-source browser journey for the site guided workflow and incident
  diagnostics passed 1/1 against the gate-built `cab5cd4` binary.
  Recovery drill details are retained at `/tmp/stepanel-recovery-drills.md`
  on the local test host; reviewed summary and limits are in
  [`2026-10-30-repository-production-gate-cab5cd4.md`](docs/lab-results/2026-10-30-repository-production-gate-cab5cd4.md).
- These are repository and synthetic-load results. They do not certify live
  filesystem quota enforcement, real PHP/database workload, sustained load,
  installed-host ENOSPC, physical power loss, repeat recovery cohorts, or live
  alert delivery; production remains NO-GO.

## Continuous per-site runtime and certificate alerts — 2026-10-30

- Added a one-minute background sampler with a fixed worker pool, per-probe
  deadlines, and a 5,000-site inventory cap. It samples PHP-FPM worker,
  listen-queue, and slow-request counters plus systemd cgroup memory/task
  usage. `/metrics` renders cached samples only; counters-only broker requests
  avoid scanning worker RSS for telemetry. Inventory/sample timestamps and
  explicit probe status make missing evidence visible.
- Added Prometheus alerts for FPM saturation, unavailable/stale runtime
  samples, per-site memory/task pressure, and certificate issuance failures
  identified by domain and durable job state/time. Existing alerts cover DB
  diagnostics/connectivity, byte and inode pressure, backup freshness/failure,
  and application recovery-proof freshness. Alert annotations state the
  triggering evidence and safe next diagnostic action.
- Follow-up review found that the database alert was generic despite existing
  cause classification. It now exports a bounded failure reason and sample
  timestamp, distinguishing authentication rejection from connection refusal
  without leaking raw helper output.
- Full `make check` passed on the final source for this slice (API artifact
  validation, `go vet ./...`, and all Go packages). Focused Go tests and a
  targeted race run passed; Prometheus 3.5.0 validated and rendered all 39
  rules and alert tests. This verifies code and rule behavior, not live alert
  delivery or host capacity; those certification gates remain open.

## KVM install attempt classified and preflight hardened — 2026-10-30

- Inspected the retained run at `/var/tmp/stepanel-kvm-current-6a77e38` and
  correlated its result table and console with the host kernel journal. The
  boot passed; install was interrupted when the host OOM killer terminated
  QEMU after about five minutes. The run used a 4096 MiB guest and only a
  512 MiB host reserve; this closes no installed-host gate.
- The install harness now refuses reserves below 2048 MiB before creating a
  run directory. A preflight regression invocation confirmed the low-reserve
  case fails with an actionable message and leaves no run directory. Detailed
  evidence and limitations: [`2026-10-30-rocky9-kvm-host-oom-6a77e38.md`](docs/lab-results/2026-10-30-rocky9-kvm-host-oom-6a77e38.md).
- The new minimum prevents this known unsafe override but cannot guarantee
  against other host memory consumers during a long run. Retry requires
  adequate free memory and swap; the recovery and power-loss certification
  gates remain open.

## Continuous host storage alerts — 2026-10-30

- `/metrics` now samples filesystem byte and inode headroom for configured
  hosting, backup, database, and control-plane storage roles. It emits only
  fixed role labels, not host paths, and makes failed probes explicit.
- Added Prometheus alerts for unavailable capacity probes and sustained free
  byte/inode headroom below 10%. Alert descriptions identify the role, measured
  ratio or failure result, threshold, evidence metrics, and a safe operator
  action. Readiness alerts now include the specific failing component label.
- Added metrics and rendered-alert regression fixtures. Focused and full checks
  passed: the targeted Go test, `make check` (API artifact validation, vet, and
  all Go packages), Prometheus 3.5.0 rule validation (28 rules), and rendered
  Prometheus alert tests including readiness component attribution.
  Continuous FPM, certificate-renewal, and per-site resource-limit alert series
  remain unimplemented; existing site
  diagnostics still provide those findings on demand.

## App activation compensation survives request cancellation — 2026-10-30

- App activation and app-state transitions now run compensation in a bounded
  context detached from the canceled operation context. Context values,
  including fencing tokens, are preserved; a lost lease is still rejected by
  the root broker. A failed restore leaves the activation journal in place.
- Added a regression that starts with an already-canceled context and proves
  the previous runtime version, current manifest, and rollback sidecar are
  restored and the journal is removed only after successful compensation.
- Focused cancellation regression and full `make check` passed, including API
  artifact validation, `go vet ./...`, and the serialized `go test ./...`
  suite. Host certification remains separate.

## Installed-host smoke completed — 2026-10-30

- GitHub Actions installed-host run
  [41033987854](https://github.com/cyberducttape/StePanel/actions/runs/41033987854)
  completed successfully on exact source `c155752c64979ec1e4fb960a9c04e318047efd73`
  in AlmaLinux 9/Caddy/MariaDB. Downloaded and reviewed its diagnostic
  artifact. The one-iteration matrix passed its import, backup, file restore,
  database restore, termination, and suspension interruption cases; DB and
  broker outage behavior recovered.
- The installed WordPress HTTP/database recovery proof passed in 7,440 ms.
  Report: [`2026-10-30-almalinux9-install-41033987854.md`](docs/lab-results/2026-10-30-almalinux9-install-41033987854.md).
- This is one exact-source disposable-host run, not a repeated cohort or
  physical power-loss/ENOSPC certification. It does not certify later
  `b6af067` as exact-source host evidence.

## Evidence-backed Prometheus alert descriptions — 2026-10-30

- Updated existing Prometheus alerts to include observed metric values and
  triggering thresholds/windows, relevant diagnostic evidence, and a safe
  operator action. Site-specific findings are already correlated in the site
  incident view; continuous per-site metric collection is not claimed here.
- Updated rendered alert expectations for queue delay and backup overdue.
  `promtool check rules observability/alerts.yml` passed with 25 rules;
  `promtool test rules observability/alerts_test.yml` passed; `git diff --check`
  passed. After recording this slice, `make check` also passed on the complete
  local source, including generated API artifact validation, `go vet ./...`,
  and the serialized `go test ./...` suite (about 70 seconds).
- This documentation and alert-text change does not certify Prometheus
  delivery, PHP-FPM host saturation monitoring, or production host readiness.

## Exact-source repository production gate — 2026-10-30

- `make production-gate` passed on `1ad78a62593ef0ee25ef19b452d366a70ba583de`
  using Go 1.26.10. Results include 57.9% total coverage, all package floors,
  race tests, four recovery drills, control-plane benchmarks, synthetic
  200-site mixed load, docs, quota-contract checks, action pins, dashboard and
  asset validation, and Bash syntax. The run exposed that local XFS project
  quota enforcement was off, ext4 project quotas were not enabled, and the
  tested Btrfs mode is unsupported; the passing contract test does not certify
  actual quota enforcement.
- Full report: [`2026-10-30-repository-production-gate-1ad78a6.md`](docs/lab-results/2026-10-30-repository-production-gate-1ad78a6.md).
  This is software-gate evidence only. Installed-host ENOSPC, physical power
  loss, repeated recovery cohort, clean-host application restoration, live
  notification delivery, and capacity certification remain open.

## Session-store outages are no longer reported as bad credentials — 2026-10-30

- Added `Registry.CheckValid` to distinguish an absent/revoked session from a
  SQLite query failure. The HTTP authentication middleware returns 503 and
  logs a concise diagnostic when it cannot verify a session; malformed,
  expired, missing, or revoked sessions retain the 401 behavior. The legacy
  boolean API remains available for long-poll/session rechecks.
- Added registry tests for missing rows versus a closed database and an HTTP
  regression verifying a previously valid signed session receives 503 during
  a store outage. `go test ./internal/session -count=1`, focused root auth
  tests, and full `make check` passed. Exact-source hosted CI remains pending.

## Backup freshness alert timing — 2026-10-30

- Corrected a false positive where an enabled schedule with no prior run was
  immediately labeled stale. Site diagnostics now include `next_run`; the
  incident view allows a two-interval grace after the first due time or last
  attempt before reporting a missing recovery point as stale. Once a success
  exists, freshness is judged against two configured intervals. Stale findings
  include available last-attempt and next-run timestamps, alongside the
  recovery action.
- Added browser regression coverage for a not-yet-due first run and a
  never-successful overdue schedule. Focused Go tests passed, JavaScript unit
  tests passed (17/17), and the focused disposable Chromium journey passed
  (1/1), including the disabled-schedule/no-policy state. `make check` also
  passed, including API artifact validation, vet, and
  every Go package. This source has not yet passed hosted CI. Production
  recovery and certification gates remain open.

## Recovery state in site incident findings — 2026-10-30

- Surfaced failing, degraded, unverified, and no-backup recovery states in the
  site incident assessment, with rehearsal/backup evidence, redaction of
  recovery reason text, and a recommended next action for each state. When no
  backup schedule is enabled, the panel now says freshness cannot be measured
  and asks the operator to configure a recovery-point cadence instead of
  silently treating an unscheduled backup as fresh.
- Extended the incident browser journey to assert the unverified-recovery
  finding; the same journey checks every finding has a title, evidence, and
  next step, and it switches the schedule off to verify the explicit unknown
  freshness state and its recommended action. `go build -trimpath`, JavaScript
  syntax checks, `npm run test:unit` (17/17), and the focused Chromium E2E
  journey (1/1) passed. `git diff --check` passed. On exact source `c91a88b`,
  hosted CI integration [41026353462](https://github.com/cyberducttape/StePanel/actions/runs/41026353462),
  CodeQL [41026353439](https://github.com/cyberducttape/StePanel/actions/runs/41026353439),
  documentation [41026353453](https://github.com/cyberducttape/StePanel/actions/runs/41026353453),
  and release evidence [41028522602](https://github.com/cyberducttape/StePanel/actions/runs/41028522602)
  passed. CI included full Go tests, race tests, ENOSPC/recovery drills,
  database restore integrations, and browser E2E. This improves operator
  visibility; it does not establish backup freshness without an explicit
  policy or close any host recovery certification gate.

## Production plan evidence reconciliation — 2026-10-30

- Updated M0, M0c, and M0d in `DEVELOPMENT_PLAN.md`: the source-exact CI,
  CodeQL, documentation, browser E2E, and release-evidence runs now reference
  `c91a88b`; migration and incident browser checks are included in that
  passing full E2E run. The prior single installed-host smoke remains evidence
  for `79e1de4` only.
- Exact current worktree `c1e8661` is clean and matches `origin/main`. A new
  full local KVM install is deferred until the host has more memory headroom:
  it currently reports about 5.2 GiB available with swap exhausted, below the
  lab's documented 6 GiB guest-plus-host-reserve requirement. This is an
  environment constraint, not a production gate closure.

## Per-site backup alert evidence — 2026-10-30

- Prometheus now exports enabled per-site schedule success presence,
  consecutive failure count, and age of the latest attempt. Failure alerts name
  the affected site and include the measured failure count. A separate alert
  identifies enabled schedules with no successful backup; disabled schedules
  do not emit these series.
- Added metric regression coverage for never-succeeded, recently attempted,
  successful, and disabled schedules, plus Prometheus rule fixtures for the
  per-site alert labels and evidence text. Focused metrics tests, `make check`
  (API artifact validation, `go vet ./...`, and the complete serialized Go
  suite), Prometheus 3.5.0 `check rules` and `test rules`, and
  `git diff --check` all passed locally. Exact-source CI integration, CodeQL,
  documentation, browser E2E, and release evidence also passed on
  `79e1de4`. Its exact-source installed-host smoke and artifact review are
  recorded below; this change does not close any production certification
  gate.

## Exact-source hosted validation — 2026-10-30

- Source `79e1de4ac3799ab5642488b69fd6b7b063d03d9b` is pushed to `main`.
  CI integration passed (run [41019722389](https://github.com/cyberducttape/StePanel/actions/runs/41019722389)),
  including Go coverage and race suites, recovery/ENOSPC drills, database
  restore contracts, static analysis, container checks, and mixed load.
  CodeQL ([41019722342](https://github.com/cyberducttape/StePanel/actions/runs/41019722342)),
  documentation ([41019722422](https://github.com/cyberducttape/StePanel/actions/runs/41019722422)),
  browser E2E (part of CI integration), and release evidence
  ([41021459620](https://github.com/cyberducttape/StePanel/actions/runs/41021459620))
  passed on the same source.
- Installation smoke [41019722358](https://github.com/cyberducttape/StePanel/actions/runs/41019722358)
  passed on AlmaLinux 9/Caddy/MariaDB. Its reviewed artifact records a
  one-iteration installed recovery matrix and WordPress HTTP/database restore
  proof with 7.309 s measured RTO. Details and limits are in the
  [result report](docs/lab-results/2026-10-30-current-source-almalinux-install-79e1de4.md).
- This exact-source evidence does not close the physical power-loss,
  20–50 repeated installed-host matrix, broad ENOSPC, current-source six-host
  matrix, supported capacity, live notification, or independent security
  review gates.

## Repository production gate — 2026-10-29

- Tested source: `70bdc34cc96ac698ca822c2796267c5ca36f5ad5`
  (`fix: preserve certificate alerts across busy domains`).
- `make production-gate` passed, including `go vet ./...`, coverage thresholds
  (57.8% overall), the full serialized race suite (root package 407.4s),
  repository recovery drills, control-plane benchmarks, mixed-load test,
  documentation/reference checks, quota-state contract checks, Action pin
  checks, asset validation, and shell syntax checks.
- Mixed-load evidence: 200 sites, 180 requests, four readers and 20 backup
  jobs; p95 request latency 26.0ms, maximum 35.6ms in this synthetic run.
- These results establish repository-level validation only. The gate itself
  reports real-host ENOSPC, physical power-loss, supported-distro, screenshot,
  and load-capacity evidence as external gates. No production-readiness gate
  is claimed closed by this run.

## Site alert evidence improvements — 2026-10-29

- Site-tree byte and inode consumption now raises explicit warning/critical
  findings at 90% and 100% of configured account allowances. Evidence labels
  the tree measurement and explains that other account data can consume quota.
- PHP-FPM saturation and worker-limit findings include queue/slow-request
  counts and the sample timestamp. Certificate renewal findings identify the
  durable job ID, terminal state, and start time while keeping raw job output
  in authorized job history.
- The incident-view browser regression now verifies that every rendered
  finding has a diagnosis title, supporting evidence, and a recommended next
  action. The focused Chromium E2E passed against a freshly built
  current-source binary. An initial run against the stale existing binary was
  discarded after rebuilding and rerunning successfully.
- Focused Go diagnostics tests and JavaScript syntax/unit checks passed; the
  disposable Playwright incident journey passed with saturation, byte/inode
  quota pressure, database failure, stale backup, and certificate-job evidence.
  The final `make check` passed on this source, including API artifact
  validation, `go vet ./...`, and the serialized Go suite (`go test -p 1
  -timeout 10m ./...`; root package 65.7s). JavaScript syntax/unit checks
  passed (17/17), and the focused disposable Playwright journey passed (1/1).

## Installed-host recovery matrix completed — 2026-10-30

- The Rocky Linux 9 KVM installed-host harness completed one full matrix
  iteration on clean source `1348611198dbb72b5a7099a86f33bf2de43e6012`.
  Boot and installation passed; the harness recorded 123 smoke/matrix passes
  over 12,155 seconds. It covered cPanel imports, backup publication, file and
  database restore, termination checkpoints, staging rollback, account
  suspension, and additional installed-host workflows. Scenario counts,
  environment, artifact hashes, and limitations are recorded in the
  [reviewed run report](docs/lab-results/2026-10-30-rocky9-kvm-recovery-matrix-1348611.md).
- This is one iteration on an older source revision with QEMU writeback cache.
  It tests process interruption, not physical host power loss, and does not
  satisfy the 20–50 repetition or current-source requirements. Gate 5 remains
  open.

## Certificate alert coverage correction — 2026-10-29

- Review found that the incident snapshot fetched only ten certificate jobs
  across all site domains, which could hide a quiet domain's renewal failure
  behind activity on other domains.
- Added a durable SQLite query that returns the latest matching job per owner
  and kind. Diagnostics now use it for certificate issuance; if history cannot
  be read, each domain is explicitly marked unknown with the reason.
- Added a regression fixture with 18 domains and 15 additional terminal jobs
  for one busy domain. The old globally limited query omits owners; the new
  query returns each owner's latest job. `make check` passed on the updated
  source; the focused race test passed; JS syntax and unit tests passed (17/17);
  the complete disposable Playwright suite passed (21 passed, one optional
  customer-auth case skipped); and `git diff --check` passed. The repository
  exact-source `make production-gate` also passed for
  `70bdc34cc96ac698ca822c2796267c5ca36f5ad5`: full coverage and race checks,
  repository recovery drills, benchmarks, mixed load, docs, quota contracts,
  workflow pins, assets, and shell syntax. Host-level certification remains
  open as recorded above.

## Current-source Rocky Linux 9 ENOSPC drill — 2026-10-30

- Verified the Rocky GenericCloud image against its published CHECKSUM file,
  confirmed `/dev/kvm` access, and ran the harness on source
  `1130c071f3c5f1319c004b67580c7ac1e9d538cd` with a clean worktree.
- The KVM guest booted with `/var/www` ext4 quota options and mounted the
  dedicated 96 MiB ENOSPC volume. Both `TestBackupRecoversFromRealENOSPC` and
  `TestRestoreFilesRecoversFromRealENOSPC` passed. Environment, phase results,
  and raw logs are retained in `/var/tmp/stepanel-kvm-enospc-70bdc34`; the
  committed report is `docs/lab-results/2026-10-30-rocky9-kvm-focused-enospc-1130c07.md`.
- Full installed-host certification was not launched: the host's available
  memory was below even the reduced required reserve for the 4 GiB install
  guest. This focused test does not cover systemd install/recovery, database
  restore ENOSPC, import/publication exhaustion, power loss, or the 20–50
  repetition cohort. Production remains NO-GO.

## Current change — per-site WordPress database health (2026-10-29)

- Added an application-level database probe using StePanel's closed, read-only
  WP-CLI `db check` operation. Production calls execute through the broker as
  the site identity; development uses the same restricted argument builder.
- The probe runs only when a regular `wp-config.php` is present, has a 10-second
  request bound, and caches results for 20 seconds (maximum 256 sites) to avoid
  bootstrapping WordPress on every dashboard refresh. It records a timestamp,
  separates passed/failed/unknown/not-applicable, and withholds raw helper
  output while identifying common connection/auth/database failure classes.
- The incident panel now distinguishes host engine reachability from the
  application's configured WordPress credentials and surfaces each result
  with evidence and a safe next action.
- Focused tests passed for success, bounded caching, failure redaction,
  non-WordPress classification, and filesystem status. One initial test reused
  a cached not-applicable result after creating `wp-config.php`; the fixture
  was corrected to use a separate WordPress site. Validation passed: focused
  race test, full `make check`, JavaScript syntax, frontend unit tests (17/17),
  and `git diff --check`. The full Go race suite also passed on the immediately
  preceding typed-worker revision (453.4s).
- Added a browser regression for pressure, application database authentication,
  backup failure/staleness, and certificate job failure evidence. The complete
  disposable seeded browser suite passed: 21 passed, 1 skipped (optional
  customer-auth isolation needs a customer password).

## Current change — typed durable Composer and runner execution (2026-10-29)

- Composer install now validates one strict JSON command at queue admission and
  worker execution. The queued worker rechecks site ownership and current
  `site:deploy` scope, verifies that the path names the same site, obtains the
  site's mutation lease, invokes the existing bounded privileged helper, saves
  the operation result, and audits success through a shared service.
- The synchronous handler route remains compatible and uses the same service;
  status reads remain unchanged. Worker execution no longer needs to replay the
  Composer HTTP handler.
- Sandboxed runner builds now follow the same typed-worker model: strict
  admission/execution decoding, current owner and scope checks, image policy
  revalidation, site lease acquisition, and broker invocation through the
  shared build service. Runner definition write and close failures are both
  checked.
- Added regression coverage for malformed, trailing, unknown, and unsafe build
  request data, plus typed durable-executor registration and rejection without
  an authorized principal.
- Focused Composer and runner Go tests pass. Combined-revision validation passed:
  `make check` (API artifact check, vet, all Go packages), JavaScript syntax,
  frontend unit tests (17/17), and `git diff --check`.
- M0e in `DEVELOPMENT_PLAN.md` tracks remaining replay routes. Production
  readiness is still NO-GO until the host certification gates close.

## Current change — evidence-backed site diagnostics (2026-10-29)

- Extended the per-site diagnostics snapshot with measured host filesystem byte
  and inode headroom, database-engine probe status and timestamp, enabled backup
  schedule cadence/results, and recent failed certificate issuance jobs.
- Expanded the incident panel to explain low filesystem headroom, systemd slice
  memory/task pressure, database connectivity failures, stale or repeatedly
  failing scheduled backups, and certificate renewal failures, alongside its
  existing PHP-FPM, quota, TLS, deployment, log, and recovery findings.
- Evidence distinguishes host database reachability from application
  credentials. Backup failure details remain in authorized job history rather
  than being copied into the site diagnostics response.
- Added tests for measured filesystem evidence and explicit unknown status when
  a filesystem probe fails.
- Validation: `make check` passed (API artifact validation, `go vet ./...`,
  and the complete serialized Go suite); filesystem diagnostic tests passed;
  `node --check web/static/sites.js` passed; frontend unit tests passed (17/17);
  and `git diff --check` passed. The first JavaScript syntax run caught a
  quoting error in the new message, which was corrected before the passing run.
- The new WP-CLI probe checks WordPress's configured database connection as
  the site identity and withholds raw helper output. Non-WordPress applications
  report not applicable; their credentials, live HTTPS behavior for Caddy, and
  a complete restore remain unverified by this check.

**Updated:** 2026-10-30
**Published base:** `ffb9e02419dd8c8ae3218bde60cbdd993349bb6c`  
**Verified local commits:**
`2d2e2cda02022b49aad0fbb44b89fdc6a6585fb8`,
`3d2131293818867388a9f71bd5ba90dbc722edf8`,
`24f8bee2524ef8dd0ee2cf7643189b2d45b78eba`
**Decision:** **NO-GO** for production; see [`docs/V1_PRODUCTION_GATES.md`](docs/V1_PRODUCTION_GATES.md).

## Completed in this work period

- Diagnosed the failing browser E2E assertion from the hosted Playwright trace:
  the test expected old quota copy, while current UI correctly describes an
  account-scoped upper-bound estimate.
- Updated the browser assertion to match the current UI wording and kept the
  repeat/worker-isolated customer fixture from `ffb9e02`.
- Built `./stepanel` from the current worktree and ran the full CI-mode browser
  suite: **18 passed, 1 skipped**. The skip is the optional customer-auth
  isolation test because no customer password is supplied to this disposable
  harness.
- Ran `make check` after the local E2E fix: API artifact check, `go vet`, and
  all Go tests passed.
- Ran `make audit` on the current local source: `go vet ./...`, the full Go
  suite, the full race suite, repository recovery drills, and release-contract
  checks all passed. The root package took 66.9 seconds normally and 408.0
  seconds under `-race`; the full race suite completed successfully.
- Recovery drill artifact `/tmp/stepanel-recovery-drills.md` reports PASS for
  partial SQL import cleanup (164s), interrupted transaction recovery (1s),
  configuration rollback (2s), and pending runtime reconciliation (1s).
  These are synthetic repository-level drills, not installed-host certification.
- Ran `make production-gate` on source `24f8bee`: coverage floors, race tests,
  recovery drills, benchmarks, mixed HTTP load, documentation, quota contracts,
  action pins, assets, and shell syntax all passed. Aggregate coverage was
  57.6%; the synthetic 200-site load had 28.6 ms p95 latency and completed 20
  backup jobs. Detailed results and scope limits are in
  [`docs/lab-results/2026-10-30-repository-production-gate-24f8bee.md`](docs/lab-results/2026-10-30-repository-production-gate-24f8bee.md).
- Ran `make release-check`, `bash scripts/check-action-pins.sh`,
  `node --check tests/e2e/critical-navigation.spec.js`, and `git diff --check`;
  all passed.
- The exact-source GitHub run for `ffb9e02` completed: Go tests, race tests,
  coverage, recovery drills, and other Go CI steps passed; CodeQL,
  documentation, and release-evidence workflows passed. CI integration failed
  only at the stale browser E2E assertion described above. Local commit
  `2d2e2cd` fixes that assertion; hosted CI has not run against it because
  pushing is not authorized.
- Repeated `customer-panel.spec.js` twice with two workers after making its
  account worker/repeat-specific: **2 passed**.
- A full-suite two-repeat stress run exceeded the 30-second test timeout during
  an Axe scan in the customer-panel journey; 35 other cases passed and 2 were
  skipped. This is a resource-contention observation under twice the normal
  suite workload, not the hosted single-run failure. The timeout was not
  increased to mask it.
- `gh issue list --state open` returned no open GitHub issues at review time.

## Current local work

- Expanded backup recovery status into an explicit evidence report. It names the
  latest validated backup and its verification time, last rehearsal attempt,
  exact passing recovery point/proof level, confirmed offsite upload, and last
  successful files/database restore from that offsite copy. The UI states that
  upload records do not prove live remote reachability, and that archive-level
  restore does not prove database import or application health. Recovery
  confidence is now degraded when a newer backup has not itself passed a
  rehearsal, even if an older recovery point passed.
- Added Go regression coverage for offsite upload/restore timestamps and
  current-backup confidence, plus browser cases for both failed-newest/older-
  proven and passing-newest/offsite-present states. Final evidence: `make
  check` passed (API artifact check, `go vet ./...`, all Go packages; root
  package 71.403s); focused race tests passed; JavaScript unit tests passed
  (17/17); the fresh browser suite passed (20 passed, 1 optional auth test
  skipped), including Axe on recovery status; `make build`, syntax checks, and
  `git diff --check` passed. These checks do not establish a real offsite
  provider restore or installed-host application recovery.

- Added the first per-site guided lifecycle workflow. It links existing import
  and create flows, validation, deploy history, TLS troubleshooting, backup
  proof/rehearsals, staging clone/promotion, and administrator retirement
  preview. WordPress/WP-CLI state, PHP-FPM counters, recent bounded error-log
  samples, and deployment status are shown in a diagnostic panel. New site
  creation now opens this guide. The UI explicitly leaves live application
  HTTP health and Caddy HTTPS handshake as unverified without probes.
- Browser regression now exercises the workflow, its unknown-proof messaging,
  WordPress status action, log excerpt, and staging link. Initial E2E review
  caught and fixed a detached-container output reference and invalid direct
  children on the ordered list. `node --check` passed, the UI unit suite passed
  (17/17), and `make build` passed. The final disposable browser run passed
  **19 tests with 1 optional customer-auth test skipped**. It includes the
  lifecycle workflow's Axe scan and all administrator workspace navigation.
  The navigation smoke intentionally avoids repeating a full-page Axe scan on
  every workspace after that made the broad test exceed its timeout; dedicated
  accessibility scans remain on the overview, customer panel, and new workflow.
  `node --check` and `git diff --check` passed. This UI-only change does not
  close any installed-host production certification gate.

- Corrected the database-admin posture check: it no longer parses loose Apache
  `Require` lines and claims they restrict the phpMyAdmin/phpPgAdmin endpoint.
  The security response now exposes `configuration_status` and
  `enforcement_status`; installed config is `detected`, while effective access
  remains `unverified`. The dashboard renders both fields. Apache config
  discovery uses `Lstat` and only recognizes a regular non-symlink file.
- Started the handler-replay migration with Node tooling. Its request has a
  strict typed decoder/validator at queue admission and execution. The worker
  uses a `siteOperationCommand` and `siteOperationPrincipal`, rechecks current
  durable ownership and role/token scope, acquires its site mutation lock, and
  calls the shared Node tooling service directly. Regression tests verify the
  worker operation succeeds without relying on the HTTP handler and reject
  unknown fields and inadequate scopes. Other site-operation routes still
  replay HTTP handlers; this architectural migration remains open.
- New security and typed-operation tests passed normally and under `-race`;
  `git diff --check` passed. Final `make check` passed on this source: API
  artifact check, `go vet ./...`, and all packages, with the root package at
  67.125 seconds. No Apache runtime allow/deny test was available, so the
  security posture remains explicitly unverified.

- Continued the typed operation extraction with Python deployment. Both the
  direct HTTP adapter and durable worker now call a shared lifecycle service;
  the worker validates a strict command, rechecks current site ownership and
  `deploy:write` policy, and no longer replays the Python HTTP handler. The
  service owns the site mutation lock, audit intent, pending/running manifest
  transitions, and helper invocation. Caller-supplied root/state fields are
  ignored. A regression test checks the helper invocation and persisted
  manifest through the durable worker path.
- Updated `DEVELOPMENT_PLAN.md` and `CHANGELOG.md` to track this bounded
  architecture increment. Other durable site operations still use handler
  replay; job persistence/orchestration and the broader cPanel stage
  decomposition remain open work, not completed milestones.
- Final-source verification passed: `make check` (API artifact check, `go vet
  ./...`, and all Go packages; root package 70.983 seconds), focused Python
  authorization/deployment tests, focused `go test -race`, and `git diff
  --check`. The earlier exact-source AlmaLinux 9 Caddy/MariaDB installation
  smoke, run 40992833027 for base commit 6ca9cbf, also completed successfully;
  it does not close repeated host recovery certification.

- Implemented the P1 restore-timeout and API-token-limiter corrections in the
  working tree. Site-operation routes now carry timeout classes; local restore
  has two 120-minute helper windows plus a 15-minute compensation reserve,
  staging creation has a 165-minute budget, and offsite restore adds the
  configured total-transfer deadline. The offsite staging download now uses
  the operation request context, so worker cancellation reaches the transfer.
- Reworked API-token throttling into bounded LRU caches for token, authenticated
  account, and trusted client source. Initial bursts are 60/token, 600/account,
  and 1,200/source, separate from sustained rates of 600, 6,000, and 12,000 per
  minute. A full cache evicts least-recently-used state instead of rejecting a
  new valid token; raw credentials, usernames, and client addresses are not
  retained as keys.
- New focused tests pass for timeout classes, lease renewal during a long
  restore, token burst/refill, per-account and per-source aggregation, LRU
  admission under 100 concurrent new identities, and bounded cache behavior.
  The pre-existing canceled staging database restore test verifies cleanup and
  recovery-required reporting when compensation itself fails.
- `make check` passed after the implementation: API artifact check, `go vet
  ./...`, and all Go packages; the root package completed in 67.654 seconds on the final source.
  Targeted `go test -race` passed for timeout, lease-renewal, and limiter
  concurrency tests; a final `go vet ./...` and `git diff --check` also passed.
  These tests do not substitute for installed-host power-loss/ENOSPC
  certification.

- `tests/e2e/critical-navigation.spec.js`: expect the actual upper-bound quota
  copy (`up to … estimated bytes remain`).
- `CHANGELOG.md`: record the corrected browser regression assertion.
- `DEVELOPMENT_PLAN.md`: prioritized milestones and objective acceptance
  criteria for all open production gates.
- `docs/lab-results/2026-10-30-repository-production-gate-24f8bee.md`: exact
  source and measurements for the repository production gate.
- `docs/API_COMPATIBILITY.md`: published the beta versus post-1.0 API
  compatibility contract and linked it from API, upgrade, and release guides.
- `PROGRESS.md`: this evidence and next-step record.

API contract validation passed: `make api-check`, `make release-check`, the
targeted OpenAPI contract tests, documentation reference/link checks, and
`git diff --check`. The M5 API-policy publication item is done;
release-candidate SDK compatibility testing and the independent security
review remain open.

Ran the pinned local security tools on `b434e3b`: govulncheck found zero
reachable vulnerabilities and one uncalled `golang.org/x/crypto/openpgp`
module advisory (no fixed version); staticcheck reported no diagnostics. The
advisory is documented and not suppressed in
[`docs/lab-results/2026-10-30-local-security-scan-b434e3b.md`](docs/lab-results/2026-10-30-local-security-scan-b434e3b.md).

The E2E fix is committed locally as `2d2e2cd`; the plan/progress documents are
also committed on the local branch. This file records the latest audit
evidence. Nothing has been pushed or dispatched remotely. The E2E run used a
binary rebuilt from this worktree.

## Current work — cPanel migration compatibility evidence

Implemented an expanded migration preflight and export flow:

- Bounded cPanel userdata inspection now proposes main/addon/subdomain/parked
  domain mappings and source document roots, with provisional status and
  collision/activation caveats.
- SQL dump headers contribute source database version/family evidence and are
  compared with destination configuration. Missing values and cross-family or
  major-line differences remain unknown/warnings rather than guarantees.
- Maildir inspection reports address validity, `cur/new/tmp` structure,
  approximate message-file counts, and data bytes. It explicitly does not
  claim mailbox credentials or mail delivery are validated.
- The optional existing SSH Migration Doctor flow now displays source PHP
  extension checks and compares against the destination PHP CLI inventory.
  Source and destination database versions identify server/client/configured
  evidence. CLI data does not claim per-site PHP-FPM parity.
- Preview and post-import reports show a DNS/mail/TLS cutover checklist and
  distinct post-migration checks for files, databases, domains, mail, and
  application rehearsal. Evidence downloads as JSON and Markdown; SSH keys are
  never included in reports.
- Added Go regression coverage for archive inspection and post-import statuses,
  plus a Playwright upload/preview/download journey. The E2E runner accepts an
  optional grep for focused runs.

Validation on the current worktree: `make check` passed (API artifact check,
`go vet ./...`, all Go packages); focused migration and scanner tests passed;
JavaScript syntax checks and `npm run test:unit` passed (17 tests); the focused
Playwright migration report and customer-panel journeys passed (2 tests).
The complete Playwright suite passed the new migration journey but timed out
once in the pre-existing customer-panel test while closing a browser context;
the isolated rerun passed. This does not change the documented host recovery,
mail delivery, DNS propagation, or production certification gaps.

Committed locally as `6c12cd1` (`Stephan Loesevitz <stephan.loesevitz@gmail.com>`).
No remote push or merge was performed for this task.

## Release blockers and evidence gaps

The authoritative P0 table still marks installed-host power loss and broad
ENOSPC testing, 20–50 repeated recovery runs, current-source application
restore/runtime certification, live notification delivery, capacity
certification, upgrade/security lifecycle evidence, and release-evidence
reconciliation as partial or open. Existing passing code tests and historical
QEMU runs do not close those gates.

An earlier environment snapshot said KVM was unavailable; that is no longer
the current state. The Rocky Linux 9 KVM job below is running as `stephanl`
with `/dev/kvm` accessible. Its source is still older than `HEAD`, its virtual
disks use writeback cache, and the repeated matrix has not completed, so it
does not close a current-source or power-loss gate.

## Next steps

1. Review the active local Rocky matrix artifacts when its process exits; run
   a current-source cohort only after deciding which matrix cases it can
   validly establish.
2. Continue with repeated host recovery, application restore, operational
   notification, capacity, upgrade, and independent security gates in
   `DEVELOPMENT_PLAN.md`.


## More specific site incident evidence — 2026-10-30

- Filesystem-pressure findings now state whether free bytes, free inodes, or
  both fell below the 10% headroom threshold, while retaining actual capacity
  counts and sample time. This gives operators a concrete resource to address.
- Database engine probe failures now distinguish timeout, connection refusal,
  authentication rejection, missing socket/helper, and other probe failures.
  Diagnostic details are bounded and do not echo raw helper output or secrets.
- Added Go regression coverage for the classification and redaction rules and
  extended the browser incident journey to assert byte/inode and refused-
  connection evidence. Focused `go test ./... -run ...`, `node --check`, and
  `git diff --check` passed. Full `make check` passed, and the focused
  disposable Chromium journey passed (1/1). Hosted exact-source CI and
  production-host certification gates remain open.

## Hosted verification for site incident diagnostics — 2026-10-30

- Exact source `c155752c64979ec1e4fb960a9c04e318047efd73` passed hosted CI
  [41033987875](https://github.com/cyberducttape/StePanel/actions/runs/41033987875),
  including formatting, release checks, vet, full Go coverage suite, race
  tests, recovery drills, tmpfs ENOSPC, MySQL/PostgreSQL restore integration,
  static analysis, container validation, and browser E2E. CodeQL
  [41033987807](https://github.com/cyberducttape/StePanel/actions/runs/41033987807)
  and documentation [41033988021](https://github.com/cyberducttape/StePanel/actions/runs/41033988021)
  also passed.
- The exact-source AlmaLinux 9/Caddy/MariaDB installed-host smoke
  [41033987854](https://github.com/cyberducttape/StePanel/actions/runs/41033987854)
  is still running without a reported failure. It is not counted as passed yet.
  Repeated recovery, physical power loss, production capacity, and other P0
  certification gates remain open.
