# Production-readiness progress

## First-run setup flow — 2026-11-21

- Commits `e0918674`, `6e97b1f7`, and `b7863b39` add live operator and
  customer setup checklists, with the primary action following the next
  unresolved step. Customer Home distinguishes unavailable site/backup data
  from empty state and does not equate archive integrity with application
  recovery.
- Commit `bd928e50` adds a domain-route check and direct navigation to the
  affected site's Domains tab. It explicitly avoids claiming that a configured
  route proves public DNS or HTTPS. `0cc1eb56` adds operator checklist E2E
  coverage for the changing primary action.
- Focused local account-owner and operator browser tests passed; the 17
  frontend unit tests, `make build`, documentation link check, and documentation
  reference check passed. CI run [44935589869](https://github.com/cyberducttape/StePanel/actions/runs/44935589869)
  passed on `b7863b39`. The domain-check change's browser job passed in
  [44937587330](https://github.com/cyberducttape/StePanel/actions/runs/44937587330);
  its full Go job was canceled when the subsequent checklist-test commit was
  pushed, not reported as a test failure. Exact-source CI for `0cc1eb56` is queued as
  [44937938000](https://github.com/cyberducttape/StePanel/actions/runs/44937938000).
- `docs/INSTALLATION.md` now separates the current-source guided path from
  v0.7.0's environment-variable installer. Current-source CI remains the
  authority for the newest checklist commits; the product is still Operator
  Beta and its production gates remain open.
- Commit `1a4b301f` groups the customer workspace into Overview, Guided
  workflow, Domains, and Backups, with specialist controls under Advanced
  administration; existing deep links expand that group. Focused customer
  Chromium E2E and accessibility checks passed, as did the operator test that
  exercises all tabs. Exact-source CI for this addition is pending.

## Current evidence and install-drill correction — 2026-11-21

- Reviewed cache-disabled abrupt-loss run [44735170833](https://github.com/cyberducttape/StePanel/actions/runs/44735170833): all five scenarios passed once on source `c3e37ebc`; the report is in [`2026-11-20-kvm-abrupt-loss-operations-cache-none-c3e37eb.md`](docs/lab-results/2026-11-20-kvm-abrupt-loss-operations-cache-none-c3e37eb.md). This bypasses the guest host page cache, but remains a QEMU guest-loss test, not physical-host power-loss evidence.
- Reviewed diagnostic capacity run [44737124383](https://github.com/cyberducttape/StePanel/actions/runs/44737124383) on `634e99ce`: 191/9,830 page requests timed out before first response byte; sampled FPM pools showed no queue/saturation but coverage was incomplete. Later evidence isolated the PHP-FPM master descriptor ceiling and fixed its systemd limit. The confirming same-profile run [44897996332](https://github.com/cyberducttape/StePanel/actions/runs/44897996332) passed with 70,715 page requests, zero failures, and 126.9/132.9 ms p99. The earlier post-fix run [44886417491](https://github.com/cyberducttape/StePanel/actions/runs/44886417491) also had zero page failures; its only failure was a readiness probe racing an offsite check before measurement. Two passes on one KVM runner type do not certify a capacity envelope. Details: [`2026-11-21-capacity-large-06f6e94-fpm-descriptor-fix.md`](docs/lab-results/2026-11-21-capacity-large-06f6e94-fpm-descriptor-fix.md).
- Install smoke [44897989372](https://github.com/cyberducttape/StePanel/actions/runs/44897989372) passed its ordinary recovery matrix, then failed in the optional identity-repair database-checkpoint drill. Artifact review found that the smoke killed a shell wrapper while its `stepanel-root` child could continue after SQLite lock release. The drill now launches the helper directly and waits for the pre-commit SQLite snapshot before killing it. Exact-source installed-host rerun is required to verify this correction.
- The six-host 20-repeat cohort [44725816245](https://github.com/cyberducttape/StePanel/actions/runs/44725816245) was canceled on stale source `f464f79d` after nine six-host iterations; its tenth iteration was interrupted. These are useful diagnostic results but count as zero current-source repeats.

## PHP-FPM descriptor exhaustion fixed; backup-interrupted drill passes — 2026-11-21

- Capacity run 44880222946: the new static-file probe never failed (0/572)
  while PHP pages timed out, locating the stall in PHP-FPM.
- Diagnostic run 44886286422 on `5ad16f3f` confirmed the cause: the shared
  PHP-FPM master hit its 1,024 open-file limit ("failed to prepare the
  stderr pipe: Too many open files" 279 times; live workers capped at 257).
- `06f6e94d` sets `LimitNOFILE=65536` for PHP-FPM and restarts it when the
  drop-in changes. Capacity run 44886417491: 81,362 page requests, 0
  failures, p99 97.1 ms baseline / 115.0 ms during backups; 557 live
  workers, 1,622 descriptors. The run was marked failed only by one
  `/readyz` 503 at t=0 (off-site target probe in progress); the probe now
  waits for readiness before measuring. Install smoke 44886416024 passed
  with the new limit check. Report:
  [`2026-11-21-capacity-large-06f6e94-fpm-descriptor-fix.md`](docs/lab-results/2026-11-21-capacity-large-06f6e94-fpm-descriptor-fix.md).
- Confirming capacity run 44897996332 on `3def0129` passed: 70,715 page
  requests, 0 failures, p99 126.9 / 132.9 ms, slowest success 13.3 s.
- KVM run 44880219867 passed install, the five abrupt-loss scenarios, the
  WordPress-shaped database ENOSPC smoke and the SMTP alert smoke; the new
  backup-interrupted scenario failed only because its site name exceeded 32
  characters. After the fix, run 44886289678 passed it: the interrupted
  backup completed on recovery, both backups verified, the earlier one
  restored state A, and a fresh backup completed.

## Post-fix 500-site capacity run and new recovery smokes — 2026-11-21

- Capacity run 44852768241 on `fd14d657`: successful p99 fell from 9.3 s to
  112 ms and every pool reported, but 271/11,810 requests (2.29%) still timed
  out in bursts of eight (one per client) every ~17 s, a host-wide stall that
  predates the FPM fix. Report:
  [`2026-11-21-capacity-large-e19d737-after-fpm-fix.md`](docs/lab-results/2026-11-21-capacity-large-e19d737-after-fpm-fix.md).
  The probe gained a request timeline and a static-file probe.
- Ubuntu 24.04/Apache install smoke 44863669797 passed the new root-broker
  mid-seal restore smoke (job completed, state A, every file owned by the
  site account) and the identity repair smoke with a real SIGKILL at the
  database checkpoint. The AlmaLinux run failed the mid-seal smoke only
  because the host lacks `cmp`/`diff`; fixed.
- N-1 upgrade run 44863673113 passed, including the new whole-guest loss
  during `filesystem_migration`.
- KVM run 44863676671 failed in the existing customer-site smoke (a customer
  site creation did not complete within 180 s; no Go changes since the last
  pass). The smoke now prints the job as the administrator sees it.

## Installed-host suspension, ENOSPC, and alert evidence — 2026-11-21

- Site suspension smoke passed on AlmaLinux 9/Caddy (44860226545) and
  Ubuntu 24.04/Apache (44860227848): 503 with Retry-After, no PHP, pool
  removed, units recorded, database logins and deploys refused, full resume.
  Tenant isolation passed on both. Both runs then failed in the identity
  repair smoke because its 30 s write lock expired before the mapping
  commit; fixed in `16d205cd`.
- KVM run 44859646620 passed database restore ENOSPC and SMTP alert
  delivery/outage/retry on Rocky Linux 9; see
  [`2026-11-21-kvm-db-enospc-alerts-7e2a575.md`](docs/lab-results/2026-11-21-kvm-db-enospc-alerts-7e2a575.md).

## Diagnose 500-site HTTPS timeouts — 2026-11-21

- Run 44725885994's failures came in bursts across unrelated sites, and the
  sites missing FPM data formed contiguous blocks that moved between scrapes.
  Both point to a php-fpm-wide stall, not a single saturated pool.
- Reproduced in a 4-CPU Rocky Linux 9 container (PHP 8.0.30): one master,
  500 pools rendered from the `stepanel-sitectl` template, 8 clients at the
  profile's ~16 req/s, plus the panel's 16-worker status sweep every minute.
  Hangs that ran to the 15-second timeout came about 10 s after each sweep.
  That is the idle timeout of the ~500 workers the sweep had forked. php-src
  `fpm_pctl_on_socket_accept` skips spawning while any child is still idle,
  and a child that was just sent SIGQUIT still counts as idle until reaped.
  PHP 8.3 and master have the same logic.
- Fix: the counters-only broker probe reports an `ondemand` pool with no
  workers (and a live master) as `idle` without connecting. Same 300 s
  workload: waking sweep 5/5,048 stalled, passive sweep 1/5,184, no sweep
  1/5,186.
- Still open: the underlying php-fpm race remains at about 0.02% of requests
  for sites whose traffic gaps match the idle timeout. Mitigations (a longer
  `pm.process_idle_timeout`, or `pm = dynamic` at a per-site memory cost) are
  a default change. Decision (2026-11-21): keep `ondemand` with a 10 s idle
  timeout, documented as a known php-fpm limitation in `docs/PERFORMANCE.md`. Capacity run 44848048837 on pre-fix
  source supplies a before-fix baseline. A post-fix run is required before
  any capacity claim.
- The before-fix run 44848048837 failed 193/9,903 site requests (1.95%), all
  15-second first-byte timeouts; FPM RSS averaged 3.4 GiB. Failure-correlated
  FPM samples showed only weak enrichment (28% of failures vs 17% of sites
  with a failed probe). Report:
  [`2026-11-21-capacity-large-e19d737-before-fpm-fix.md`](docs/lab-results/2026-11-21-capacity-large-e19d737-before-fpm-fix.md).

## Review cache-bypassing abrupt-loss drill — 2026-11-20

- Reviewed the artifacts of KVM drill
  [44735170833](https://github.com/cyberducttape/StePanel/actions/runs/44735170833)
  on `c3e37ebc` (Rocky Linux 9.9, `VM_DISK_CACHE: none`). Boot, install (20
  smoke/matrix checks), and all five abrupt-loss scenarios passed: file
  restore, database restore (safety backup reused, 8/8 tables matched), Git
  deployment (dead-letter, original release served), retention, and
  control-plane writes (write 679 kept after 679 acknowledged / 680
  attempted). Report:
  [`2026-11-20-kvm-abrupt-loss-operations-cache-none-c3e37eb.md`](docs/lab-results/2026-11-20-kvm-abrupt-loss-operations-cache-none-c3e37eb.md).
- The guest boot log shows `Failed to start Enable File System Quotas`, while
  quota enforcement still worked during install smoke; recorded for follow-up.
- One pass on non-current source; not physical power-loss evidence. The gate
  stays open.

## Verify customer workspace and file-manager review findings — 2026-11-20

- Confirmed the account-owner site form includes the WordPress setup fieldset;
  `sites.js` also tolerates forms that omit optional fields. Customer navigation
  uses hash-driven views and disables the operator dashboard's scroll spy.
- The customer-panel browser test verifies no uncaught page errors, WordPress
  form initialization, invalidation of a delayed file read after switching
  sites, and clearing/inerting stale directory actions while navigation is
  pending. The hosted `ui-e2e` job passed on code source
  [`d0ed0fa3`](https://github.com/cyberducttape/StePanel/commit/d0ed0fa361574e037760ecf90c1dd8e5ab14b11a)
  in CI run [44739044630](https://github.com/cyberducttape/StePanel/actions/runs/44739044630).
  Browser coverage includes Chromium and accessibility checks; it is not a
  cross-browser certification.
- Local `make check` passed for the concurrent file-read limiter. The later
  exact-source CI and installed-host results are recorded below. Production
  remains **NO-GO** pending the open release gates.
- The initial docs workflow caught a reference-check false positive from a
  regex written in the progress note. The wording was corrected; the docs
  workflow passed on updated `main` in run
  [44739502546](https://github.com/cyberducttape/StePanel/actions/runs/44739502546).
- Dedicated CI was first dispatched on code-equivalent source `1e66dc58` and
  passed. A later exact-source run on `de6f96f7` also passed; see the current
  CI section below.

## Current-source CI and installed-host smoke passed — 2026-11-20

- Source `de6f96f7a902833397820bc9dd3e8f720327ea73` passed CI integration
  [44741756748](https://github.com/cyberducttape/StePanel/actions/runs/44741756748)
  including the full Go suite, race tests, UI browser/accessibility job, and
  package checks; PR-CI
  [44741882511](https://github.com/cyberducttape/StePanel/actions/runs/44741882511),
  CodeQL [44741756870](https://github.com/cyberducttape/StePanel/actions/runs/44741756870),
  and documentation [44742113006](https://github.com/cyberducttape/StePanel/actions/runs/44742113006)
  also passed.
- The exact-source installed-host smoke
  [44741756889](https://github.com/cyberducttape/StePanel/actions/runs/44741756889)
  passed on AlmaLinux 9/Caddy/MariaDB. It passed one recovery-matrix iteration,
  including filectl, database/import/backup/restore recovery and broker
  interruption checks, and proved a restored WordPress application over HTTPS
  with an 8.929-second measured RTO. This is one host and one iteration; it
  does not close repeated recovery or physical-host failure gates. Production
  remains **NO-GO**.

## Bound concurrent file reads and directory listings — 2026-11-20

- Added a nonblocking admission limiter around authorized file reads and
  directory listings: four concurrent operations per tenant and 32 per panel
  process. Tenant identity comes from the resolved site-access capability,
  so requests across the tenant's different sites share the same budget.
  Saturation returns HTTP 429 and `Retry-After: 5`; active-only map entries
  are removed when requests complete.
- Regression tests cover per-tenant fairness, the host cap, reusable slots,
  idempotent release, empty-identity rejection, and the HTTP retry response.
  Focused package tests, the race-focused limiter tests, and `make check`
  passed. `make check` includes `go vet ./...`, the serial full Go suite, and
  the repository's local validation scripts. `DEVELOPMENT_PLAN.md` M0o records
  the six-host file-manager race result and the added concurrency bound.
  The hosted customer browser journey passed on code-equivalent source
  `d0ed0fa3`; its full Go CI job was still running. Installed-host validation
  of the new limiter remains pending. Production remains **NO-GO**.

## Reconcile installed-host evidence status — 2026-11-20

- Rechecked the tracked workflow handles. Installation smoke
  [44724016501](https://github.com/cyberducttape/StePanel/actions/runs/44724016501)
  is complete and successful on `6e80a189`, with one database-backed
  WordPress restore proof (RTO 5.022 s); it is not current-commit or repeated
  recovery evidence. The six-host 20-repeat cohort
  [44725816245](https://github.com/cyberducttape/StePanel/actions/runs/44725816245)
  remains in progress on `f464f79d`. Main has advanced to `634e99ce`; the
  changes since that cohort are in the capacity harness, documentation, and
  abrupt-loss workflow, so its host results need source mapping and review
  before counting.
- Abrupt-loss run using QEMU writeback
  [44731756198](https://github.com/cyberducttape/StePanel/actions/runs/44731756198)
  completed successfully on `94b90f5e`. Artifact review found all five
  injected-loss scenarios passed; the report is
  [`2026-11-20-kvm-abrupt-loss-operations-94b90f5.md`](docs/lab-results/2026-11-20-kvm-abrupt-loss-operations-94b90f5.md).
  This is one pass, with writeback caching, and does not close the gate. The
  cache-bypassing follow-up
  [44735170833](https://github.com/cyberducttape/StePanel/actions/runs/44735170833)
  and the 20-repeat cohort remain in progress. Exact-source CI Go tests and
  capacity profiling are also in progress. Recovery and production gates
  remain open.

## Investigate 500-site KVM capacity failure — 2026-11-20

- Reviewed the completed Rocky Linux 9 profile
  [44725885994](https://github.com/cyberducttape/StePanel/actions/runs/44725885994)
  and its uploaded report, JSON, capacity log, installation log, and guest
  console. Boot and installation passed; the 500-site, eight-client HTTPS
  phase failed with 196 first-byte timeouts among 9,658 site requests (2.03%).
  Both 512 MiB backup jobs completed in 22.1/40.7 seconds. `/api/sites` and
  `/api/jobs` remained responsive, with zero SQLite lock errors.
- At sampled peaks, FPM reported no saturated pools and a zero listen queue,
  but up to 96 of 504 sites lacked FPM samples and the 15-second scraper was
  not correlated with individual timeouts. CPU peaked at 99.3%; the available
  evidence does not establish the cause. No 500-site capacity claim is
  supported. Details and limits are in
  [`the reviewed report`](docs/lab-results/2026-11-20-capacity-large-f464f79.md).
- Improved the next-run capacity report to break request counts, errors, and
  HTTP statuses out by baseline and backup windows. Retained failed requests
  now include the most recent bounded per-site FPM/cgroup resource snapshot
  and its age. Added regression tests for window accounting, site-scoped FPM
  details, and missing sample reporting.
- Local verification: `python3 -m unittest deploy/lab/test_capacity_profile.py`
  passed 10 tests; Python compilation, `git diff --check`, and full `make
  check` passed (`go vet ./...` and all Go packages; root package
  119.779 seconds). A new
  exact-current-source 500-site KVM run is still required to collect the
  additional diagnostics and determine whether this is a code defect or a
  host/workload capacity boundary. M4 and production readiness remain open;
  production remains **NO-GO**.

## Current-source release validation — 2026-11-20

- Exact-source evidence for current code `94b90f5e7c24a5345e365076da387e962f4b917a`
  is in [release-evidence run 44731402244](https://github.com/cyberducttape/StePanel/actions/runs/44731402244).
  Its artifact confirms documentation, CI integration (including race tests,
  ENOSPC, recovery drills, database integration, and browser E2E), CodeQL, and
  the 6/6 N-1 upgrade matrix passed on identical source content. It recorded
  4/21 P0 gates complete and 17 incomplete; the scheduled six-host smoke and
  20-repeat recovery cohort were still in progress at report time.
- Current-code AlmaLinux 9/Caddy/MariaDB installed smoke
  [44724016501](https://github.com/cyberducttape/StePanel/actions/runs/44724016501)
  passed. I reviewed its uploaded host log: the installed WordPress
  database-backed HTTP recovery proof passed with measured RTO 5.022 s; the
  one-iteration recovery matrix, database outage/recovery, broker restart,
  worker interruption across import/backup/restore/termination, and account
  suspension recovery also passed. This is one host and one repetition only.
- N-1 upgrade matrix
  [44724039602](https://github.com/cyberducttape/StePanel/actions/runs/44724039602)
  passed all six jobs. It covered v0.6.0/v0.7.0 in disposable systemd
  containers and Ubuntu 24.04 QEMU/Apache guests, including verification
  interruption. This is one limited matrix run, not full upgrade certification.
  Six-host recovery cohort
  [44725816245](https://github.com/cyberducttape/StePanel/actions/runs/44725816245),
  and 500-site KVM capacity profile
  [44725885994](https://github.com/cyberducttape/StePanel/actions/runs/44725885994)
  are still running. The cohort has started its first six-host iteration; no
  full repetition is counted until all six jobs and their artifacts are
  reviewed.
- The scheduled six-host install smoke
  [44729127788](https://github.com/cyberducttape/StePanel/actions/runs/44729127788)
  passed on code-equivalent source `059ba72e`. All six Ubuntu 24.04,
  AlmaLinux 9, and Rocky Linux 9 Apache/Caddy combinations passed installed
  file-manager smoke, detached-package-process cgroup containment, installed
  WordPress recovery proof, and a one-iteration recovery matrix. Measured
  WordPress proof RTOs were 4.588–6.866 seconds. Details are in
  `docs/lab-results/2026-11-20-six-host-install-smoke-059ba72.md`.
  This remains one smoke pass, not the 20-repeat recovery cohort or a physical
  host power-loss certification.
- Re-ran the customer account-owner Playwright journey against a fresh binary
  built from current `main` (`c2f976ec`): 1/1 passed. The test verifies the
  WordPress controls initialize with no uncaught browser errors, delayed file
  reads cannot write across a site switch, and old directory rows are cleared
  and inert during navigation. This confirms the customer UI findings already
  fixed in `fdec5c05`/`0afc3c6e`; no additional code change was needed.
- Exact-source evidence generation initially hit the unauthenticated GitHub
  API rate limit locally; the authenticated evidence workflow completed and
  uploaded the artifact. No product failure was observed. An older-source
  recovery cohort was cancelled to free runners; its partial work does not
  count.
- QEMU abrupt-loss operation drill
  [44731756198](https://github.com/cyberducttape/StePanel/actions/runs/44731756198)
  completed on `94b90f5e`. All five injected guest-loss scenarios passed:
  file restore, database restore, Git deployment, retention, and control-plane
  writes. The database stayed unchanged immediately after the interrupted
  restore; retry used the first safety backup and matched all eight tables.
  See the [reviewed artifact report](docs/lab-results/2026-11-20-kvm-abrupt-loss-operations-94b90f5.md).
  This is one pass and not physical host power-loss certification.
- The initial abrupt-loss run uses QEMU `writeback`, so host page cache may
  preserve acknowledged writes after QEMU is killed. Updated the workflow to
  use `VM_DISK_CACHE=none` for follow-up testing and dispatched
  [run 44735170833](https://github.com/cyberducttape/StePanel/actions/runs/44735170833)
  for the same five scenarios. It later passed all five; see the entry
  "Review cache-bypassing abrupt-loss drill" above. `cache=none` strengthens guest-crash
  evidence but does not simulate physical host power loss.

## Current-code N-1 upgrade matrix passed — 2026-11-20

- N-1 matrix [44724039602](https://github.com/cyberducttape/StePanel/actions/runs/44724039602)
  completed 6/6 jobs on source `6e80a189`, whose non-Markdown tree matches
  current `main`. Both v0.6.0 and v0.7.0 were exercised in disposable systemd
  containers and Ubuntu 24.04 QEMU/Apache guests, with ordinary and verification
  interruption paths. All jobs passed.
- This does not certify populated installations, every supported OS/web-server
  combination, every rollback boundary, or a production release. The release
  decision remains **NO-GO** pending the open P0 gates.

## Verify database-restore command defenses and audit integrity — 2026-11-20

- Current-code-equivalent CI integration
  [44724016547](https://github.com/cyberducttape/StePanel/actions/runs/44724016547)
  passed the MySQL 8 client-command smoke and PostgreSQL restore smoke. The
  PostgreSQL test restored a valid custom archive, proved concurrent loader
  role grants are serialized across tenants, and rejected plain SQL and the
  literal `\!`, `\copy`, and `\o` meta-commands without changing the live
  database. Fixtures assert their exact backslash bytes; earlier doubled-slash
  fixtures are excluded from evidence.
- The same run passed the full suite and race tests, and its non-Markdown tree
  matches current `main`. Current-source CodeQL
  [44730175604](https://github.com/cyberducttape/StePanel/actions/runs/44730175604)
  passed. Production remains **NO-GO** because installed-host recovery,
  capacity, repeated failure drills, and other P0 evidence remain incomplete.

## Retry transient package-mirror failures in host test setup — 2026-11-20

- The N-1 run [44712444760](https://github.com/cyberducttape/StePanel/actions/runs/44712444760)
  had a container interruption case fail before StePanel upgrade execution:
  Ubuntu's package mirror returned HTTP 520 while the lab installed its
  `rclone` prerequisite. Updated both installed-host lab scripts to use apt's
  bounded `Acquire::Retries=3` for index and package downloads, so a transient
  mirror response is retried rather than recorded as an installer failure.
- `bash -n` and CI-equivalent `shellcheck -S warning -x -P SCRIPTDIR` passed for
  both modified scripts; `git diff --check` and full `make check` passed (root
  package 119.644 seconds). The original N-1 workflow was cancelled after
  diagnosis; its other matrix cases were incomplete and did not count as a
  result. The retry fix awaits the current-source N-1 run below.
- Current-source N-1 upgrade smoke [44724039602](https://github.com/cyberducttape/StePanel/actions/runs/44724039602)
  and six-host install smoke [44724016501](https://github.com/cyberducttape/StePanel/actions/runs/44724016501)
  are queued. CI integration [44724016547](https://github.com/cyberducttape/StePanel/actions/runs/44724016547)
  has started; CodeQL [44724016529](https://github.com/cyberducttape/StePanel/actions/runs/44724016529)
  and documentation [44724016486](https://github.com/cyberducttape/StePanel/actions/runs/44724016486)
  remain queued. Obsolete queued checks and long-running host tests on older
  commits were cancelled to free runner capacity; their incomplete results
  are not counted. No host certification result is claimed. Production
  remains **NO-GO**.

## Revalidate pushed source and start installed-host smoke — 2026-11-20

- On exact source `498f214235bdb5a30013f9d04ae0e9e5716e4854`, `make check`
  passed generated API artifacts, shell and helper contracts, `go vet ./...`,
  and all Go packages; the root package completed in 120.039 seconds. The
  action-pin policy check also passed.
- Dispatched the standard six-host installation smoke against that exact SHA:
  [run 44722820266](https://github.com/cyberducttape/StePanel/actions/runs/44722820266).
  It is queued at host selection; no installation or recovery result is
  counted yet. This standard smoke is one iteration and does not satisfy the
  20–50 repetition release gate.
- Exact-source CI integration, CodeQL, and documentation runs are also queued.
  The release evidence report on this SHA correctly distinguishes queued
  checks from running jobs and records 4/21 P0 gates complete, 17 incomplete.
  Production remains **NO-GO**.

## Evidence report job-state accuracy — 2026-11-20

- Fixed the release evidence generator so queued, waiting, running, completed,
  and unrecognized GitHub Actions jobs are reported as distinct states. A
  queued CI job is no longer misleadingly counted as running or incomplete
  execution evidence.
- Added tests for state classification and rendered summaries. `go test
  ./internal/evidence -count=1`, `go test -race ./internal/evidence -count=1`,
  `git diff --check`, and full `make check` passed. The full check included
  generated API validation, helper and shell contracts, `go vet ./...`, and
  all Go packages.
- Exact-source hosted CI and host certification for the resulting commit are
  pending; this change does not close any production certification gates.
  Production remains **NO-GO**.

## Run CI-equivalent race suite and inspect upgrade failure — 2026-11-20

- `go test -p 1 -race -timeout 15m ./...` passed all packages on the current
  code. The root package took 790.988 seconds; the focused audit race test also
  passes. An initial local run with `GOMAXPROCS=2` and a 10-minute timeout
  expired at the root-package limit, with the final test just starting; this
  was a too-short local limit, not a failure in that test. The CI command's
  15-minute package timeout completed successfully.
- Inspected N-1 run
  [44712444760](https://github.com/cyberducttape/StePanel/actions/runs/44712444760)
  on source `50e73c7d`: v0.6.0 QEMU passed; the v0.7.0 container with
  verification interruption failed before upgrade execution because Ubuntu
  package fetch returned HTTP 520. One QEMU case is still running and four
  matrix cases are queued. This is an external package-mirror failure, not a
  product result; re-run that case after the matrix terminates. The source is
  not equivalent to current main, so none of its passes certify current code.
- Exact-source local report for `6940c2bc` at 10:38 UTC remains **NO-GO**:
  4/21 P0 gates complete, 17 incomplete, and 0/9 executable checks complete
  on equivalent hosted source. Current-source install smoke
  [44716595496](https://github.com/cyberducttape/StePanel/actions/runs/44716595496),
  CI integration [44716264869](https://github.com/cyberducttape/StePanel/actions/runs/44716264869),
  CodeQL [44716447796](https://github.com/cyberducttape/StePanel/actions/runs/44716447796),
  and capacity run [44711012147](https://github.com/cyberducttape/StePanel/actions/runs/44711012147)
  are queued or still running; the capacity run is on older source.
- No production gate is closed by the software race suite. Repeated installed
  recovery, physical/uncushioned host loss, broad ENOSPC, current-source
  WordPress restore proof, capacity, live alert delivery, and current-source
  N-1 evidence remain open.

## Harden audit scan cadence and refresh exact-source status — 2026-11-20

- Changed the audit full-scan due check so a future-dated persisted timestamp
  (for example, after a host clock rollback) forces a full-chain verification
  instead of delaying scans until wall time catches up. Added deterministic
  fresh/expired/future timestamp tests. Audit tests and `go test -race
  ./internal/audit -count=1` pass; full `make check` passes on the change
  (root package 118.6 seconds).
- Exact-source release evidence for
  [`c18e2a1d`](https://github.com/cyberducttape/StePanel/commit/c18e2a1d51d1f40809227259e9b1014913e8912c)
  generated at 10:06 UTC reports **NO-GO**, 4/21 P0 items complete, 17
  incomplete, 0/9 executable checks complete, and 6 checks without equivalent
  source evidence. CI integration, CodeQL, and documentation are queued for
  this code change. Installation/recovery and capacity runs still available
  are on older code trees and do not count.
- Local installed-host certification cannot run under this account:
  `/dev/kvm` is not writable by the user and `sudo -n` requires interactive
  authentication. The certification scripts only mutate disposable guests,
  but hosted KVM evidence is still needed; no host gate is claimed from local
  unit tests.
- Pushed audit and plan changes as `c18e2a1d`, authored by Stephan Loesevitz.

## Verify audit tamper detection and customer UI findings — 2026-11-20

- Added `TestAppendPeriodicallyVerifiesSameSizeSameMtimeAuditTampering`. It
  preserves the audit file size and mtime, expires the signed full-scan
  checkpoint, alters a middle event, and verifies that the next append fails
  without changing the tampered ledger. The focused regression and
  `go test ./internal/audit -count=1` both pass. This covers local periodic
  detection; independent off-host checkpoints and installed alert delivery
  remain separate production evidence gaps.
- Full `make check` passed on `807d680455f2c57953f76d4e4f6c8f694faa397a`:
  generated API artifacts, shell/helper contracts, `go vet ./...`, and
  `GOMAXPROCS=2 go test -p 1 -timeout 10m ./...`; the root package took
  119.6 seconds. This progress update changes documentation only.
- Rechecked the reported customer workspace and file-manager issues. They are
  already fixed in source: the customer create form includes WordPress fields
  and the initializer handles optional markup; file-editor context is bound to
  site/path/generation; folder navigation clears old rows and makes actions
  inert while loading. Existing Playwright coverage exercises account-owner
  initialization, delayed reads across a site switch, and stale-row clearing.
  The local browser test skipped because no seeded E2E host was configured;
  its E2E job passed on code-equivalent UI source in run
  [44710979526](https://github.com/cyberducttape/StePanel/actions/runs/44710979526).
- Pushed as `c83727f1` (`test: catch same-metadata audit tampering`) with the
  requested author identity. Focused CI integration and exact-source release
  evidence runs are queued:
  [44714460763](https://github.com/cyberducttape/StePanel/actions/runs/44714460763),
  [44714473301](https://github.com/cyberducttape/StePanel/actions/runs/44714473301).

## Verify capacity-report fix and current-source checks — 2026-11-20

- `make check` passed on `ae5b003a00be21150c00292df876fa84e67af0cd`, including
  API artifact validation, shell/helper contracts, `go vet ./...`, and
  `GOMAXPROCS=2 go test -p 1 -timeout 10m ./...`. The root package completed
  in 119.5 seconds. The specifically reported
  `TestGitStageQuotaIsSkippedOnlyInDevelopment` also passed independently in
  6.0 seconds; its earlier long wait is not reproduced on this tree.
- CodeQL [44710979906](https://github.com/cyberducttape/StePanel/actions/runs/44710979906)
  and Documentation [44710979560](https://github.com/cyberducttape/StePanel/actions/runs/44710979560)
  passed on the code-equivalent source. CI integration
  [44710979526](https://github.com/cyberducttape/StePanel/actions/runs/44710979526)
  completed its UI E2E job successfully; its Go job was cancelled before
  completion. Installation smoke
  [44710979561](https://github.com/cyberducttape/StePanel/actions/runs/44710979561)
  remains queued.
- The account-owner UI E2E job passed. A current-main N-1 upgrade matrix
  [44712444760](https://github.com/cyberducttape/StePanel/actions/runs/44712444760)
  was dispatched on `50e73c7d` and is queued; prior passing upgrade runs are
  historical and do not verify the current installer or schema.
- The corrected 500-site KVM profile
  [44711012147](https://github.com/cyberducttape/StePanel/actions/runs/44711012147)
  is running on the exact code tree at `ae5b003a`; its unit test passed before
  provisioning. The earlier run `44699996203` failed only while aggregating
  differently shaped samples and emitted no measurements. No capacity limit
  is certified; host recovery and N-1 release gates remain open.

## Reconcile installed evidence with current source — 2026-11-20

- The phase-instrumented 500-site run
  [44699996203](https://github.com/cyberducttape/StePanel/actions/runs/44699996203)
  on `3acc333c` completed site setup and workload collection but failed during
  report aggregation. The summary helper unpacked admin samples as four-field
  tuples although they contain three fields, so no report artifact was
  produced. Added a regression test covering both site and admin sample
  shapes; all 8 capacity-probe unit tests pass locally. A full profile rerun is
  still required, and the old run provides no usable capacity measurements.
- Generated the exact-source release report for `4ff67a09` at 09:20 UTC. It
  recorded **NO-GO**, 4 complete and 17 incomplete P0 items, with 9 executable
  checks incomplete and 6 without equivalent-source evidence. Current
  installed-host and capacity runs had not produced reviewed results.
- Compared source trees instead of relying on labels in older notes. The
  successful one-host WordPress/cgroup run `44686046786` is from `e1195ccc`;
  22 non-Markdown files changed between that source and current `main`,
  including production file-manager/helper code. The successful upgrade matrix
  `44663143624` is from `3f2580b`; 50 non-Markdown files changed since it.
  Those results are valid for their recorded sources but are not current-source
  certification.
- Corrected `DEVELOPMENT_PLAN.md` and `docs/V1_PRODUCTION_GATES.md` to mark the
  old runs as historical, distinguish runtime-equivalent from exact-source
  results, and retain the required reruns as open. Current release evidence
  still awaits exact-source CI, installed recovery, upgrade, and capacity
  results. Production remains **NO-GO**.
- `bash scripts/check-action-pins.sh` passes; documentation references, links,
  and whitespace checks pass against these corrections.

## Customer website workspace and file-manager race regressions — 2026-11-20

- Rechecked the reported initialization and cross-context file actions on
  current `main` (`6a88aae6`). The customer template includes the WordPress
  fields; file reads and mutations carry their originating site/path/generation;
  folder navigation clears prior rows, invalidates the editor, and makes the
  toolbar inert while the new listing is pending.
- Built a fresh binary from this checkout and ran
  `STEPANEL_E2E_GREP='customer panel: views, file manager and accessibility'
  scripts/e2e-ui-test.sh /tmp/stepanel-ui-current`. The seeded account-owner
  browser flow passed (1/1), including no page errors, WordPress form
  initialization, a delayed read across a site switch with no cross-site
  write, stale-row clearing, and accessibility checks.
- These findings are already fixed and their regression coverage is already in
  the repository; this run adds current-source local evidence. M0y remains
  separate from host certification and production remains **NO-GO** pending
  the open installed-host gates.

## Alert retry persistence across restart — 2026-11-20

- Added a regression that records a webhook outage in the durable alert
  outbox, closes/reopens the control-plane database, verifies pending/failure
  status and retry attempts survive, advances the retry deadline, and confirms
  the event can be marked delivered. This covers database restart recovery,
  not delivery through a real installed SMTP/webhook endpoint.
- Focused normal and race-enabled tests passed. Full `make check` passed on
  the change (root package 119.695s), including generated API validation,
  shell contracts, `go vet ./...`, and all packages.
- M3 remains open until installed-host delivery, endpoint outage/recovery, and
  operator-visible status are exercised against configured endpoints.

## Correct PostgreSQL restore attack fixtures — 2026-11-20

- Review of `deploy/lab/dbctl-restore-pg-smoke.sh` found its purported
  `\\!`, `\\copy`, and `\\o` inputs encoded two literal leading backslashes.
  The smoke could reject plain SQL while never testing the actual `psql`
  client commands named in the test and plan. Earlier PostgreSQL smoke passes
  are not counted as evidence for those exact commands.
- Corrected the inputs to single-backslash `\!`, `\copy`, and multiline `\o`
  fixtures. Added an in-smoke assertion that rejects fixture strings unless
  they start with exactly one backslash. The extracted fixtures passed the
  assertion locally, including the multiline case; `bash -n`, ShellCheck, and
  `git diff --check` passed.
- Applied the same fixture-integrity check to the MySQL/MariaDB import smoke
  for both `\!` and `\.` commands. `bash -n`, ShellCheck, smoke-workflow bounds,
  and a local single-backslash fixture check pass; database execution remains
  part of hosted integration CI.
- The corrected real-server test has not yet run. A current-source CI run must
  pass the PostgreSQL restore smoke before this security regression can be
  considered verified.
- On exact source `0caad3f4`, `make check` passed API artifact validation, shell
  contracts, `go vet ./...`, and all Go packages (root package 119.6s). The
  current hosted integration run is queued. This host has no MySQL server or
  `mysql` OS account, so both real-server database-client smokes remain
  unverified locally.

## Recovery cohort snapshot — 2026-11-20

- The older-source 20-repeat cohort
  [44686066552](https://github.com/cyberducttape/StePanel/actions/runs/44686066552)
  on `e1195ccc` completed repetition 1 across all six OS/web-server pairs.
  All six jobs and their uploaded smoke logs passed review. The second
  repetition has three host jobs running and three queued. Its outputs show
  cPanel import, backup/restore/termination interruption, and account
  suspension recovery passing for that source. It predates current
  file-manager changes, so none of these results count for current `main`.
- The full six-host matrix
  [44701903618](https://github.com/cyberducttape/StePanel/actions/runs/44701903618)
  on `f35e4b71` remains in its first repetition. The 20-repeat cohort
  [44702646288](https://github.com/cyberducttape/StePanel/actions/runs/44702646288)
  on `a7594a45` also remains in its first repetition. Both use non-current
  source trees; no complete repetition is counted for current `main`.
- Code-equivalent current-source installation smoke
  [44710979561](https://github.com/cyberducttape/StePanel/actions/runs/44710979561)
  remains queued. Exact-source CI, recovery, and capacity evidence are tracked
  in the current progress entry above. Production remains **NO-GO**.

## Reverify customer workspace and file-context regressions — 2026-11-20

- Confirmed the reported customer UI defects are fixed in current `main`
  (`f35e4b71`, a documentation-only descendant of UI-tested source
  `3acc333c`): WordPress creation fields are supported in the customer form
  and guarded when absent; editor sessions bind the site/path/revision;
  navigation invalidates pending reads, clears stale rows, and disables folder
  actions while loading; inventory failures preserve the distinction between
  unavailable and empty.
- Exact-source browser E2E and accessibility job passed in
  [CI run 44699909154](https://github.com/cyberducttape/StePanel/actions/runs/44699909154).
  It includes the account-owner workspace, page-error assertions, delayed
  cross-site read, stale folder navigation, and verification that no write is
  sent to the newly selected site. Local `npm run test:unit` passed 17/17.
- The installed-host smoke and current 500-site KVM profile for this commit
  were still running at review time; no host-readiness or capacity conclusion
  is drawn from the UI result. Production remains **NO-GO** pending the open
  release gates in [`docs/V1_PRODUCTION_GATES.md`](docs/V1_PRODUCTION_GATES.md).

## Review exact-source 500-site capacity failure — 2026-11-20

- Confirmed run [44686066350](https://github.com/cyberducttape/StePanel/actions/runs/44686066350)
  completed as **failure** on exact source `e1195ccc`. It recorded 184 of
  9,791 site requests timing out at 15 seconds; successful site p99 was
  8,074.5 ms. Both 512 MiB backups completed, admin APIs remained responsive,
  and no SQLite lock errors appeared.
- Reviewed its JSON/Markdown artifacts and added
  [`the diagnostic report`](docs/lab-results/2026-11-20-capacity-large-e1195ccc.md).
  FPM telemetry showed no saturation, but was available for only 473/504
  sampled sites. Host CPU peaked at 99.9%; available memory remained at least
  7,901 MiB. This does not prove the cause, so no worker or capacity defaults
  were changed. `DEVELOPMENT_PLAN.md` and the authoritative production-gate
  table now describe the run as failed rather than in progress.
- Next capacity evidence needs TCP/TLS/web-server/PHP stage timings, per-site
  availability for every expected pool, and a repeated exact-release run after
  the bottleneck is isolated. Capacity remains uncertified; production remains
  **NO-GO**.
- Added those missing probe measurements: client/TCP setup, TLS handshake,
  time-to-first-byte, response transfer, failure phase counts, and bounded site
  names for unavailable/saturated FPM probes. Regression tests exercise each
  failure phase and telemetry aggregation; `python3 -m unittest
  deploy/lab/test_capacity_profile.py` passed 7 tests, and Python compilation
  passed. The probe has not yet been rerun on a host.

## Reject stale customer file-editor writes — 2026-11-20

- File reads now return a SHA-256 content revision. The editor binds it to the
  selected site/path, sends it as `If-Match`, and receives HTTP 409 if content
  changed before save. The customer can intentionally reload the latest
  version. Both the development store and the descriptor-relative production
  file helper enforce the revision; unconditional upload behavior remains
  available when no revision is supplied.
- Added local-store/API/helper regressions, a production-helper contract test,
  and browser coverage for conflict and reload. `make check` passed, including
  `go vet ./...`, all Go packages, generated API checks, and shell contracts.
  `npm run test:unit` passed 17/17; ShellCheck, shell/JS syntax, and
  `git diff --check` passed.
- The focused customer browser E2E passed against a freshly built binary
  (1/1). Exact-source CI passed the full Go/race and UI suites in
  [44697055872](https://github.com/cyberducttape/StePanel/actions/runs/44697055872);
  CodeQL [44697055748](https://github.com/cyberducttape/StePanel/actions/runs/44697055748),
  documentation [44697055795](https://github.com/cyberducttape/StePanel/actions/runs/44697055795),
  and release evidence [44697340919](https://github.com/cyberducttape/StePanel/actions/runs/44697340919)
  passed. Installed-host validation
  [44697055839](https://github.com/cyberducttape/StePanel/actions/runs/44697055839)
  is still running. Release remains **NO-GO** pending the P0 host recovery, application
  recovery, alert-delivery, and capacity gates in
  [`docs/V1_PRODUCTION_GATES.md`](docs/V1_PRODUCTION_GATES.md).

## Stabilize race-suite lease-renewal regression — 2026-11-20

- Exact-source CI [44690329247](https://github.com/cyberducttape/StePanel/actions/runs/44690329247) on `c127e212` exposed a race-only test failure: the 90 ms lease expired during SQLite work before renewal began. The test was adjusted to use a 2-second lease and a 5-second simulated restore, preserving the multiple-renewal assertion while allowing loaded CI scheduling.
- The focused race test passed five consecutive local runs. The full race suite then passed on exact source `0afc3c6e` in CI [44697055872](https://github.com/cyberducttape/StePanel/actions/runs/44697055872); CodeQL, documentation, and release evidence also passed. Installed-host validation [44697055839](https://github.com/cyberducttape/StePanel/actions/runs/44697055839) is still running. The N-1 upgrade matrix [44692262234](https://github.com/cyberducttape/StePanel/actions/runs/44692262234) completed successfully. The 500-site profile [44686066350](https://github.com/cyberducttape/StePanel/actions/runs/44686066350) completed with a site-request failure and is documented above; the six-host recovery cohort [44686066552](https://github.com/cyberducttape/StePanel/actions/runs/44686066552) remains active.

## Diagnose the 500-site capacity failure with per-site runtime evidence — 2026-11-20

- Reviewed completed Rocky Linux 9 profile [44674647682](https://github.com/cyberducttape/StePanel/actions/runs/44674647682), source `8382e9c4`. The 500-site run failed with 210/9,609 site requests timing out at 15 seconds; management API and job-list p99 stayed below 23 ms, two 512 MiB backups completed, available host memory stayed above 7.8 GiB, and SQLite lock errors were zero. This is evidence against the proposed 500-site envelope; it is not an exact-current-source result and does not isolate the site request bottleneck. Recorded full measurements and limitations in [`the lab report`](docs/lab-results/2026-11-20-capacity-large-8382e9c.md).
- Extended the capacity probe to scrape authenticated `/metrics` every 15 seconds and retain bounded per-site PHP-FPM saturation/queue/worker/slow-request metrics and cgroup CPU/memory use, along with scrape failures and sample age. Aggregation now preserves separate PHP-version pools instead of overwriting metrics for the same site. Added regression coverage for multiple pools, missing resource limits, failed scrapes, and bounded HTTP diagnostics.
- Local evidence: `python3 -m unittest deploy/lab/test_capacity_profile.py` passed 5 tests; Python compilation and `git diff --check` passed. Exact-source CI [44684486774](https://github.com/cyberducttape/StePanel/actions/runs/44684486774) on `3ee6aba2` passed Go tests and UI E2E, then was cancelled during race testing by the follow-up probe fix. Installed smoke [44684486808](https://github.com/cyberducttape/StePanel/actions/runs/44684486808) was cancelled after the same probe defect was confirmed in the capacity run. Next: commit and push the corrected probe, rerun installed-host validation, then rerun the 500-site KVM profile, inspect per-site diagnostics, and measure a targeted correction. M4 remains open; production remains **NO-GO**.

## Fix installed package-containment probe environment — 2026-11-20

- Exact-source KVM capacity run [44684499265](https://github.com/cyberducttape/StePanel/actions/runs/44684499265) and recovery cohort [44685011516](https://github.com/cyberducttape/StePanel/actions/runs/44685011516) both stopped in the shared install smoke before workload/recovery checks: `stepanel-appctl` could not locate the probe's temporary Composer executable because the smoke inherited a restricted `PATH`.
- Set an explicit deterministic Composer lookup path for the temporary probe and added a shell-contract check. Local bash syntax, ShellCheck, cgroup contract, shell contract, and diff checks pass. The failed cohort used this same source and is not counted; a new exact-source installed smoke, recovery cohort, and capacity run must follow the fix.

## Exact-source installed cgroup and WordPress recovery proof — 2026-11-20

- Installed-host smoke [44686046786](https://github.com/cyberducttape/StePanel/actions/runs/44686046786) passed on `e1195ccc` for AlmaLinux 9/Caddy/MariaDB. The probe launched a detached Composer child, confirmed it shared the transient unit and site slice, verified task/runtime/memory limits, and confirmed the child exited when the unit stopped. The installed WordPress database-backed HTTP proof passed with measured RTO 5.273 seconds; the recovery matrix passed one iteration.
- Exact-source CI [44686046784](https://github.com/cyberducttape/StePanel/actions/runs/44686046784), CodeQL [44686046899](https://github.com/cyberducttape/StePanel/actions/runs/44686046899), and documentation [44686046789](https://github.com/cyberducttape/StePanel/actions/runs/44686046789) passed. The 20-repeat six-host cohort [44686066552](https://github.com/cyberducttape/StePanel/actions/runs/44686066552) and instrumented 500-site capacity run [44686066350](https://github.com/cyberducttape/StePanel/actions/runs/44686066350) remain in progress. One host iteration does not close those gates; production remains **NO-GO**.

## Add installed-host package descendant containment probe — 2026-11-20

- Added `deploy/lab/site-package-cgroup-smoke.sh` and wired it into the
  installed-host smoke after applying a site's resource slice. It invokes the
  real `stepanel-appctl composer-install` path using a temporary fake Composer
  executable, launches a detached session, confirms its `/proc` cgroup matches
  the transient unit, checks the unit's site slice and task/runtime bounds,
  verifies the site slice has an effective memory limit, stops the unit, and
  checks the detached process is no longer running. The existing Composer
  executable is restored on normal exit and the disposable host is the target
  for this privileged integration probe.
- Added shell syntax coverage to `make shell-contract-check` and added the
  probe to CI ShellCheck. Local `make shell-contract-check`, ShellCheck of the
  new probe, `scripts/check-action-pins.sh`, and `git diff --check` passed. The
  probe cannot be exercised on this non-systemd development host; exact-source
  installed-host execution is required before M0i can claim host evidence.

## Current-source install recovery evidence — 2026-11-20

- The exact-source AlmaLinux 9/Caddy/MariaDB installation and one-iteration
  recovery run [44679148230](https://github.com/cyberducttape/StePanel/actions/runs/44679148230)
  passed on code `9f8c75ac`, which is identical in non-Markdown files to
  current `main`. Its installed smoke passed control-plane DB outage
  recovery, file-manager and PHP-FPM checks, cPanel and database client-command
  restore checks, broker interruption, app lifecycle, worker interruption
  during import/backup/restore/database restore/termination, account
  suspension, and deployment checks. Installed WordPress recovery proof
  passed with measured RTO 5.084 seconds. This is one iteration on one host
  combination; it does not satisfy Gate 5's repeated six-host cohort or prove
  physical host power-loss durability.
- Exact-source release evidence [44682232548](https://github.com/cyberducttape/StePanel/actions/runs/44682232548)
  reports installation smoke passed and production NO-GO (4/21 P0 gates
  complete). Its generated report is downloadable from the workflow run.
- Dispatched ordinary CI against exact current main source
  [44682398082](https://github.com/cyberducttape/StePanel/actions/runs/44682398082)
  to replace the prior older-source result in release evidence; it is still
  running. The six-host 20-repeat cohort [44662607070](https://github.com/cyberducttape/StePanel/actions/runs/44662607070)
  and Rocky Linux capacity rerun [44674647682](https://github.com/cyberducttape/StePanel/actions/runs/44674647682)
  are also still running and are not counted as passed.

## Fix exact-source staticcheck failure — 2026-11-20

- Exact-source CI integration for `4ef3514a` passed the UI E2E/accessibility,
  race, and tmpfs ENOSPC stages, then failed `staticcheck` on seven
  `SA4006` reports: pre-lock authorization capabilities were assigned but
  intentionally replaced after lock acquisition, and a test marshal error
  was overwritten before checking.
- Kept both pre-lock and post-lock authorization checks; changed the first
  check to discard its unused capability and made the post-lock value the one
  used for mutations. The test now checks each JSON marshal result directly.
- Evidence on the working tree: `go run honnef.co/go/tools/cmd/staticcheck@2026.1 ./...`
  passed; focused tenant/lifecycle tests passed; full `make check` passed
  (vet, all Go packages, API artifacts, shell contracts). Commit `9f8c75ac`
  is pushed with the requested author identity. Exact-source CI integration
  [44679148277](https://github.com/cyberducttape/StePanel/actions/runs/44679148277)
  passed both jobs, including static analysis, race tests, recovery drills,
  real tmpfs ENOSPC recovery, MySQL restore/ENOSPC/server-kill checks,
  PostgreSQL restore privileges, Python deployment, container and manifest
  validation, and the browser E2E/accessibility suite.
- The exact-source AlmaLinux 9/Caddy installation smoke and the large KVM
  capacity rerun are still running; neither is counted as passed. Production
  remains **NO-GO**; M0i and host-certification gates remain incomplete.

## Bind customer site creation to its reserved tenant — 2026-11-20

- Site-creation admission now records the site tenant after customer assignment
  is reserved. The worker validates that immutable tenant before work and again
  after acquiring its site lock. Old unbound jobs fail closed when tenancy is
  configured.
- Added a regression that moves a reserved site to a different tenant before
  worker execution; the queued creation is refused before host changes. Updated
  existing customer provisioning fixtures to use the durable tenant contract.
- Evidence: `make check` passed after the change (vet, all Go packages, API
  artifacts, and shell contracts); focused site-creation and tenant-binding
  tests passed. Hosted exact-source CI is pending.
- The tenant-binding code now covers site operations, Node deployment, backup
  create/restore/rehearsal, termination, cPanel/WPress restore, and site
  creation. M0i still requires review of remaining direct job kinds and
  installed-host cgroup containment evidence; production remains **NO-GO**.

## Bind queued archive restores to their admitting tenant — 2026-11-20

- cPanel and WPress durable restore payloads now include the destination site's
  tenant snapshot captured before queue admission. Workers fail closed for old
  unbound payloads when account ownership is configured, and compare the
  snapshot again after acquiring the site lock before validating or applying
  the import.
- Added reassignment regressions for both archive workflows. The tenant-bound
  backup/termination changes from the preceding commit remain covered by the
  full crash-recovery suite.
- Evidence: focused import reassignment tests passed; `make check` passed,
  including `go vet ./...`, all Go packages, shell contracts, and API artifact
  checks. This is local evidence; hosted exact-source checks will run after
  commit.
- M0i remains partial: site creation and installed-host cgroup behavior still
  require work/evidence. Overall release remains **NO-GO**.

## Investigate 500-site capacity profile failure — 2026-11-20

- Reviewed the Rocky Linux 9 KVM profile [44663139216](https://github.com/cyberducttape/StePanel/actions/runs/44663139216)
  and its report, capacity log, installation log, and guest console. Boot and
  installation passed; the 500-site capacity phase failed with 202 non-success
  HTTPS responses out of 9,829 and long p99 tails. Both 512 MiB backups
  completed within 40 seconds; measured admin APIs and SQLite lock-error count
  were healthy. The artifact does not retain response details, so the 202
  responses have not been attributed to a product or workload cause.
- Added a source-labeled evidence report at
  [`2026-11-20-capacity-large-3f2580b.md`](docs/lab-results/2026-11-20-capacity-large-3f2580b.md)
  and improved `capacity-profile.py` to retain bounded response status counts,
  failure samples, and connection diagnostics. Three fast unit tests cover
  HTTP errors, connection failures, and the response-detail bound; the KVM
  capacity workflow runs them before its long host drill. A rerun is required;
  no 500-site capacity certification is claimed.

## Extend immutable tenant binding to backup and termination jobs — 2026-11-20

- Backup create, scheduled backup, backup restore, scheduled/manual recovery
  rehearsal, and site-termination payloads now snapshot the site's tenant at
  admission. Workers reject a missing/changed tenant binding and recheck after
  acquiring site mutation locks where applicable. Durable worker protocol v5
  prevents v4 and older workers from silently ignoring the expanded payload
  contract.
- Added regression coverage for reassignment during admin backup restore and
  termination. Crash drills and cross-process lock tests retain recovery
  behavior, including the final termination boundary where site ownership has
  already been removed. That exception requires the same admin job's existing
  journal to show site-state removal and refuses a site that has been
  reassigned.
- Evidence: `make check` passed (`go vet ./...`, all Go packages, API artifact
  check, shell syntax, and cgroup/runtime contracts); focused termination
  crash, interruption, reassignment, and cross-process lock tests passed;
  docs reference check (90 documents) and link check passed. Hosted checks for
  this commit are pending.
- M0i remains partial: other site-bound durable job kinds and installed-host
  cgroup evidence are still open. Overall production release remains NO-GO
  until the documented host certification gates pass.

## Bind queued site mutations to their admitting tenant — 2026-11-20

- Site-operation and Node-deployment admission now stores the site tenant ID
  in the encrypted job payload. Workers compare it with durable current site
  ownership and the actor's current tenant before invoking a typed executor;
  Node deployment repeats the check after acquiring its site mutation lock.
  Signed webhook deployments also retain and verify the tenant snapshot when
  account ownership is configured.
- Jobs from a prior payload contract that lack tenant binding cannot be
  executed as customer mutations; interrupted operations may still enter their
  recovery path. The durable worker protocol version advanced from 2 to 3 so
  an older worker cannot accept the new job contract.
- Regression tests transfer both a member and site between tenants after
  enqueue, and verify that site-operation and Node-deployment workers refuse
  the old job while the authorization helper accepts a newly queued job for
  the new tenant.
- Verification on this worktree: focused authorization/site-operation/Node
  tests passed; focused race tests passed; `go vet ./...` passed; full `make
  check` passed, including API artifact checks, shell contracts, vet, and all
  Go packages. This is local evidence only; hosted exact-source CI is pending.
- Remaining M0i work includes binding the other durable job kinds and
  installed-host tests proving detached package/build descendants remain
  contained by their transient systemd cgroups.

## Exact-source CI, KVM crash recovery, and N-1 upgrade results — 2026-11-20

- Exact-source CI integration [44665400225](https://github.com/cyberducttape/StePanel/actions/runs/44665400225) passed 2/2 jobs on `8556e9fe`: full Go tests and coverage floors, race tests, repository recovery and tmpfs ENOSPC drills, MySQL restore/ENOSPC/server-kill checks, PostgreSQL restore privilege checks, security/static scans, container/Helm/Terraform validation, and browser E2E. Browser E2E passed 24 tests with one skipped test; the seeded customer journey exercised WordPress form startup, inventory retry, delayed cross-site editor response, and refusal to write to the newly selected site. CodeQL passed on the same source.
- Reviewed the abrupt-loss artifact for [KVM run 44662674624](https://github.com/cyberducttape/StePanel/actions/runs/44662674624), source `64281ca`. One guest kill/reboot passed for file restore, database restore, Git deploy, backup retention, and control-plane writes; the install phase passed 19 smoke/matrix checks. Database content remained at its pre-restore state immediately after restart, then the retry completed using the first safety backup and matched all eight tables. See [`2026-11-20-kvm-abrupt-loss-operations-64281ca.md`](docs/lab-results/2026-11-20-kvm-abrupt-loss-operations-64281ca.md). QEMU used `writeback` caching, so this is not physical host power-loss evidence; the one-off checks do not satisfy the repeat requirement or cover ENOSPC during database restore/import/publication.
- N-1 upgrade run [44663143624](https://github.com/cyberducttape/StePanel/actions/runs/44663143624) passed 6/6 jobs for v0.6.0 and v0.7.0: containers and Ubuntu 24.04 QEMU/Apache guests, with and without verification interruption, followed by documented downgrade and repeat upgrade. Product code and upgrade scripts match `8556e9fe`; the tested commit differs in docs and the CI manual-dispatch line. See [`2026-11-20-upgrade-matrix-3f2580b.md`](docs/lab-results/2026-11-20-upgrade-matrix-3f2580b.md). During the broken-candidate scenario, automatic installer rollback reported host recovery incomplete; the successful result required the documented operator downgrade procedure. This does not certify automatic rollback on populated customer hosts or other distributions.
- At this update, the 20-repeat six-host recovery cohort [44662607070](https://github.com/cyberducttape/StePanel/actions/runs/44662607070) was still running (2 planning jobs passed, six host shards active, no failed shard), as was the Rocky Linux 9 KVM capacity profile [44663139216](https://github.com/cyberducttape/StePanel/actions/runs/44663139216). No partial cohort result is counted. Release remains **NO-GO**.

## Reviewed six-host recovery iteration — 2026-11-19

- Downloaded and inspected all six host logs from exact-source installation run [44516615547](https://github.com/cyberducttape/StePanel/actions/runs/44516615547). Each reported `recovery matrix passed (1 iteration(s))`, survived worker interruption during import/backup/restore/database restore/termination, passed broker recovery checks, and completed the installed WordPress database-backed HTTP proof.
- Recorded host-specific measured WordPress proof RTOs (4.756–7.106 seconds) and the systemd-container/test boundary in [`2026-11-19-six-host-recovery-64281ca.md`](docs/lab-results/2026-11-19-six-host-recovery-64281ca.md). Updated the M1/M2 plan and authoritative P0 gate evidence date. This is one pass per host, not the required 20–50 repeated cohort or physical-host certification.
- CI integration on the same code tree (run [44662602622](https://github.com/cyberducttape/StePanel/actions/runs/44662602622)) had passed UI E2E and was still running the race suite at this update. The 20-repeat cohort [44662607070](https://github.com/cyberducttape/StePanel/actions/runs/44662607070), abrupt-loss KVM drills [44662674624](https://github.com/cyberducttape/StePanel/actions/runs/44662674624), 500-site capacity profile [44663139216](https://github.com/cyberducttape/StePanel/actions/runs/44663139216), and N-1 upgrade matrix [44663143624](https://github.com/cyberducttape/StePanel/actions/runs/44663143624) had not completed; no result is counted yet.
- Local `make check`, documentation link/reference validation, and the exact-source initial CodeQL/documentation checks passed. Production remains **NO-GO** pending reviewed results and the other open P0 gates.

## Exact-source main validation and recovery certification — 2026-11-19

- Fast-forwarded the clean main worktree to `64281ca87391c54038547b8b9dc002701b2ae125`; the change includes typed executors for all queued site operations, backup naming/layout and encryption package extraction, and accessibility fixes across every site-workspace tab. The customer workspace and file-manager context fixes from M0y are included in this ancestry.
- Local `make check` passed on `64281ca` (API artifact freshness, shell contracts, `go vet ./...`, and serialized `go test -p 1 -timeout 10m ./...`).
- Exact-source installation smoke [44516615547](https://github.com/cyberducttape/StePanel/actions/runs/44516615547) passed all six Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 Apache/Caddy combinations (7/7 jobs). This is an install smoke, not the full repeated recovery cohort.
- Exact-source CodeQL and documentation checks passed; release evidence [44521384715](https://github.com/cyberducttape/StePanel/actions/runs/44521384715) records 4/21 P0 requirements complete and a **NO-GO** decision. The report correctly does not count older host-cohort or quota runs as equivalent source evidence.
- Started exact-source CI integration [44662602622](https://github.com/cyberducttape/StePanel/actions/runs/44662602622), the 20-repeat six-host installed recovery cohort [44662607070](https://github.com/cyberducttape/StePanel/actions/runs/44662607070), and KVM abrupt-loss drills [44662674624](https://github.com/cyberducttape/StePanel/actions/runs/44662674624). These were still running/queued at this update; no result is counted. Guest termination does not establish physical host power-loss durability.
- Next: inspect each exact-source run and artifact, fix any reproduced defect before continuing, then update the release-gate table only with reviewed evidence. Physical power loss, ENOSPC during database restore/publication/import, live alert delivery, and measured capacity remain open unless the corresponding evidence proves otherwise.

## cPanel migration destination plan and recovery evidence — 2026-10-31

- Added a separate `STEPANEL_CPMOVE_MAX_UPLOAD_BYTES` setting (default 40 GiB,
  maximum 40 GiB) without increasing ordinary restore uploads. Archive expanded
  size, entry count, filesystem metadata overhead, database growth, and free
  space checks remain independently enforced.
- Added an authenticated destination-plan API and dashboard step. It resolves
  imported domain names against current site placements/filesystem paths,
  checks target ownership, existing domain routes, MySQL compatibility, and
  capacity, then signs the reviewed archive/target/database/mapping state.
  Import rejects stale/missing previews and the worker repeats capacity and
  conflict checks after the site mutation lock. The CLI now follows the same
  inspect → plan → import contract; generated SDK and completions were updated.
- After import, the migration report fetches per-site recovery status and shows
  the last verified backup, measured rehearsal duration, observed recovery
  point age, recurring rehearsal/application-proof configuration, and
  confidence. The normal site recovery panel also labels observed RPO age and
  measured RTO clearly.
- Verified boundaries: import still does not update public DNS or prove the
  destination's public TLS/HTTP route. The WordPress proof is a disposable
  application rehearsal, and not a pre-activation customer-facing staging
  site. The report and operations docs state these limits instead of implying
  cutover is verified. A site needs a backup schedule and enabled application
  proof for recurring application-level recovery drills.
- Focused destination-plan/config tests and JavaScript syntax checks passed.
  The first full `make check` exposed a legacy no-upload-ceiling fixture after
  the new cPanel limit was introduced; that case was corrected so an explicit
  zero limit still rejects chunked uploads before reading. Final validation:
  `make check` passed (API artifact freshness, shell contract, vet, full Go
  suite), and focused `go test -race` passed for destination plan/token and
  upload admission. `npm run test:unit` passed (17/17), and both changed
  browser modules passed `node --check`. Browser E2E and installed-host 40
  GiB, WordPress, DNS/TLS, and scheduled recovery drills were not run here.

## HTTP 503 incident correlation — 2026-10-31

- Extended the site systemd resource snapshot with active/substate/result and
  cgroup v2 `memory.events` OOM/OOM-kill counters. The helper validates the
  systemd-provided cgroup path before reading it; unsupported/unreadable
  counters remain explicitly unavailable.
- Added a compact 503 evidence panel to the site incident view. It summarizes
  the bounded access-log sample and relates it to current PHP-FPM counters,
  systemd/cgroup state, host and WordPress database probes, quota headroom,
  recent web-server errors, TLS status, and the most recent deployment.
  Cumulative counters and independent sample times are called out; the view
  does not claim causation or render request paths/client addresses in the
  summary.
- Added a status-parsing regression and extended the browser journey with 503,
  saturated FPM, OOM-counter, and deployment evidence fixtures.
- Initial local checks passed: `bash -n deploy/integrations/stepanel-appctl`,
  `node --check web/static/sites.js`, `git diff --check`, focused Go tests,
  full `make check`, and all 17 JavaScript unit tests. The Playwright journey
  was discovered but skipped because this environment has no seeded E2E server
  and credentials. Exact-source CI remains pending. Cgroup/systemd behavior
  still needs installed-host checks on supported distributions.

## Risky configuration change plans and after-state proof — 2026-10-31

- Expanded the shared preview contract used by PHP runtime/resources, route
  apply/delete, certificate issuance, Caddy `.htaccess` translation, WordPress
  staging promotion, and Git release rollback. Confirmation dialogs show
  structured before/proposed data, effects, validation commands with their
  automated/manual status, recoverability, expected interruption outcomes,
  and the proof expected after completion. Preview content is rendered as
  text, with a 64 KiB display bound.
- PHP profile and resource UI messages now report returned applied/pending
  state and the applied values. Route and Caddy mutation responses include
  verification timestamps/checks. Git rollback reports active and retained
  releases. Certificate jobs reread the installed lineage and only complete
  successfully when the leaf certificate matches the requested hostname and
  is unexpired; results include the lineage digest and verification time.
- Added change-plan, Git rollback-plan, and certificate evidence regressions
  plus browser E2E coverage for rendering the review dialog. Focused Go tests,
  JavaScript syntax checks, all 17 JavaScript unit tests, and final-tree
  `make check` passed. Playwright discovered the new E2E case but skipped its
  execution because this environment has no authenticated seeded E2E server.
- Preview gating is limited to the listed workflows. Other risky mutations
  that lack an expiring preview remain open work; expected interruption
  behavior shown in a preview describes the implemented subsystem contract
  and does not constitute host-level crash certification.

## cPanel migration plan, TLS inventory, and rollback evidence — 2026-10-31

- Extended the cpmove preflight and downloadable JSON/Markdown reports with
  bounded public TLS certificate metadata: hostnames, expiry, validity, and
  SHA-256 fingerprint. Certificate PEM and private-key contents are never
  included, and the report states that destination TLS was not installed or
  verified.
- Added explicit DNS source/destination status and preserved the cutover
  checklist. The archive scan detects zone files, but destination provider
  records are not queried; reports identify this as `not_compared` and do not
  claim a record-level diff or DNS verification.
- Import results now expose recovery transaction IDs and checkpoints for
  archive validation, prior-site snapshot, filesystem activation, database
  restore, mailbox staging, route changes, application rehearsal, and rollback
  boundary. Failure compensation checkpoints now survive function return, and
  the job response no longer replaces actual rollback evidence with a generic
  `not_reached` state.
- Existing migration planning continues to include domain/document-root maps,
  source/destination database version checks, optional read-only PHP extension
  comparison, mailbox inventory/validation, per-component dispositions, and
  staged-vs-unsupported behavior.
- Validation passed: focused cpmove Go tests, `node --check` for the changed UI
  and E2E spec, all 17 JavaScript unit tests, and `make check` (API artifacts,
  shell/cgroup checks, `go vet ./...`, and full serialized `go test ./...`).
  Authenticated Playwright E2E was not run in this environment. DNS record
  value comparison and live destination TLS verification remain explicit
  evidence gaps; exact-source hosted CI is pending.

## Per-site evidence-based recovery confidence — 2026-10-31

- The recovery API now reports a confidence level and five explicit checks:
  archive verification, database restore, application boot/HTTP probe,
  synthetic database transaction, and offsite restore. Each successful check
  carries the exact backup identity and timestamp; restore checks also carry
  measured elapsed time. Remote upload alone remains a warning, not proof of
  offsite recoverability.
- Archive-only rehearsals now remain `unverified`; they do not promote the
  database, application, or synthetic-transaction stages. Application proof
  is counted only when it covers the newest backup. Configured offsite storage
  must have a recorded restore for the newest backup before full confidence is
  reported.
- Built-in WordPress recovery proof now writes a cryptographically random
  option value with WP-CLI, reads it back, and requires an exact match. The
  database belongs to the disposable proof site and is removed during teardown.
  Broker action validation, mutation fencing, and timeout classifications are
  covered by regression tests.
- The site lifecycle view surfaces the confidence and evidence level. The
  Backups view shows the full evidence ladder, latest successful test time,
  exact tested recovery point, measured rehearsal duration, and remote restore
  time.
- Validation passed: focused recovery and broker Go tests, focused recovery
  HTTP tests, `go test -race ./internal/recovery ./internal/rootbroker`, all
  17 JavaScript unit tests, and `make check` (API artifact check, shell/cgroup
  contracts, `go vet ./...`, and full serialized `go test ./...`). The two
  changed Playwright recovery tests were discovered but skipped because this
  environment has no authenticated seeded E2E server. Exact-source hosted CI
  and authenticated browser E2E remain pending. This feature does not close
  the installed-host recovery certification gates.

## Separate 1.0 release scope from competitive hosting roadmap — 2026-10-31

- Kept 1.0 focused on the documented operator-managed single-host contract and
  its existing production gates. Repeated installed-host application recovery
  remains a 1.0 blocker; file-manager parity, first-party mail, reseller
  billing, DNSSEC, identity integrations, PITR, and clustering do not become
  implicit release blockers.
- Added post-1.0 M7/M8 acceptance criteria for migration fidelity and
  dispositions, target-side migration proof, the full WordPress lifecycle,
  customer file-manager parity, DNS zone/DNSSEC support, a mail ownership ADR,
  provider functions, identity, PITR, clustering, and stronger tenant
  isolation. Ordered product work starts with migration and WordPress
  confidence, then panel parity, then enterprise/distributed capabilities.
- Clarified that the current importer, WordPress beta workflows, offsite
  backups, and tenant controls are foundations; partial or beta capabilities
  are not certified end-to-end customer lifecycle support.
- Documentation link and code-reference checks passed for all 90 current
  Markdown documents; `git diff --check` passed. No product code or feature
  behavior changed in this planning update.

## Node deployment authorization, recovery, and typed execution — 2026-10-31

- Node deployment jobs now persist whether admission used an API token and its
  token ID. Worker execution revalidates token status, expiry, account
  ownership, and current `site:deploy` scope. Worker-owned checkpoint fields
  supplied in the enqueue request are discarded.
- Before changing state, the worker persists a bounded snapshot of `.nvmrc`,
  app manifests, rollback manifest, and the proxy configuration. It records
  each step as applying/applied. A retry first compensates from that snapshot
  under the new durable job lease and site fencing token. Compensation uses a
  bounded context that survives operation cancellation only while ownership is
  still valid; after lease loss it refuses host mutations and leaves recovery
  state for the next claimant. Invalid prior manifests fail closed, and
  rollback handles both Apache and Caddy proxy configuration.
- Removed Node deployment's synthetic HTTP handler replay. The API handlers
  and durable worker now call shared typed services for Node selection, app
  activation, and proxy deployment, passing the typed authenticated principal;
  each service enforces the current deploy-scope policy. This closes the
  Node-specific portion of the handler-replay debt; the six other listed
  `site.operation` routes remain tracked under M0s/M0e.
- Added regressions for token revocation before worker execution, forged
  checkpoint fields, recovery at each step boundary, and refusal to roll back
  after durable lease loss. Focused tests and `go vet ./...` pass; focused
  Node deployment tests also pass under the race detector. The first full
  check after introducing typed principals exposed three app-deploy unit tests
  that bypassed the authentication middleware; their fixtures now provide the
  authenticated administrator identity. Focused app/Node regressions and the
  corrected full `make check` pass, including vet and the full serialized Go
  suite (root package completed in 87.7 seconds). Focused app/Node regressions
  also pass under `go test -race` on the final typed-principal form.
- This does not certify host crash recovery or prove every multi-resource
  failure sequence. Interrupted Node deployments are compensated and retried
  from a durable snapshot; an unavailable/lost lease leaves explicit recovery
  state rather than authorizing a stale worker to mutate host state.

## Lease-recovery contract and durable queue measurements — 2026-10-31

- Clarified M0l's recovery contract: lease fencing only prevents stale durable
  job commits; it cannot undo a filesystem, database, service, or provider side
  effect. Before production certification, each mutating job/broker/provider
  action must be inventoried by operation ID, with idempotency or reconciliation
  rules, journal transitions around each effect, and injected kills immediately
  before/after effects. Recovery must inspect actual external state and end in
  completed, compensated, or operator-intervention state.
- Source inspection found the legacy root-broker site-create journal uses
  `site-create-<site>` as its journal ID instead of receiving the durable job
  ID. This is now an explicit open identity-contract gap in
  `DEVELOPMENT_PLAN.md`; no claim is made that all broker mutations use a
  unique operation ID or have complete crash certification.
- Refreshed queue evidence on production source `ec88c85` with 30 claims per
  queue-depth sample at 1k/10k/100k rows, `EXPLAIN QUERY PLAN`, and three runs
  each of target and saturated dashboard/two-worker contention. Direct claim
  p95/p99 measurements are now emitted by the multi-worker benchmark and the
  report is in
  [`2026-10-31-queue-claim-benchmark-ec88c85.md`](docs/lab-results/2026-10-31-queue-claim-benchmark-ec88c85.md).
- At 100k queued rows, production indexes measured 211.1 ms mean, 218.9 ms
  p95, and 220.8 ms p99; a temporary ordered index was slower (223.1 ms mean,
  260.4 ms p95, 271.7 ms p99), so no production query/index change was made.
  Under saturated two-worker/two-poller load, claim p99 ranged 62.7–85.1 ms;
  no claim, busy, re-arm, or session errors occurred. The 19–20 target-profile
  claim samples per run are too few for stable tail estimates.
- This is local synthetic evidence, not queue capacity certification. Real
  backup/restore/migration load, supported-host measurements, long-duration
  WAL pressure, and crash/restart validation remain open under M0u and the
  release gates. No destructive host tests were run. `go vet ./...` and
  `make check` pass on this change. A custom race-instrumented contention
  benchmark exceeded 90 seconds under race overhead and was stopped; it is not
  counted as a passing race result.

## Recovery proof isolation, worker compatibility, and login limiter capacity — 2026-10-31

- Recovery-proof commands now receive a minimal environment containing only a
  fixed PATH/locale and the requested site/stage. They are refused when the
  panel process is root, and bounded child output is discarded rather than
  copied into durable errors. A regression sets known session, encryption,
  and audit secrets in the parent and checks the child cannot read them; it
  also verifies child stderr is not preserved.
- Worker heartbeats now persist a durable job-protocol version. Readiness
  requires matching protocol, release version, and commit. External mode
  persists the panel-required contract in SQLite; workers verify it before
  publishing a heartbeat, and all durable job admission fails closed without
  a compatible live worker. Migration, worker-startup, mismatch, and
  admission regressions are added.
- Login limiter capacity now uses an O(1) LRU list: a full bounded table
  admits a new key while preserving recently active keys. A capacity test
  verifies the recent key remains throttled after eviction.
- Validation passed: `go vet ./...`, `go test -race ./internal/auth`,
  `go test ./internal/...`, focused root-package regressions for proof
  environment isolation and worker compatibility/admission, and the complete
  serialized suite `GOMAXPROCS=2 go test -p 1 -timeout 10m ./...` (latest root
  package run completed in 72 seconds). Focused root race tests for the worker
  contract and proof process also pass. Hosted exact-source CI has not yet
  been run.
- Remaining work in this finding set: six site-operation routes still replay
  HTTP handlers. M0s in `DEVELOPMENT_PLAN.md` tracks typed-executor extraction
  and its authorization/recovery acceptance criteria.

## Exact-source CI coverage correction — 2026-10-31

- CI run `41261054648` passed formatting, release checks, vet, the full Go
  suite, disk-exhaustion drills, and UI E2E, but failed the 95% audit-package
  coverage gate at 94.8%. CodeQL and documentation checks passed on the same
  source; installation smoke was still running at the time of this update.
- Added audit regressions for a symlinked log path, an audit-path inspection
  failure, and a dedicated checkpoint key with a missing chain key. The audit
  package now measures 95.0%, and its 95% gate passes locally. Full hosted CI
  for the correction is pending.

## Independent audit-checkpoint signing key — 2026-10-31

- Production config validation now refuses an audit checkpoint destination
  without `STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY`. The existing key-length and
  independence checks remain active; fallback to the chain key is available
  only in nonproduction compatibility use.
- Added production configuration regression coverage for missing and dedicated
  keys. Focused config and audit checkpoint tests and `make check` pass.
- This protects key separation in StePanel configuration; the checkpoint key
  remains process-accessible and should be moved to a separate KMS/HSM identity
  for stronger compromise isolation.

## Audit-chain append integrity — 2026-10-31

- The signed audit-state cache now records the validated log size and nanosecond
  modification time. An append fully checks every event HMAC and adjacent chain
  link when either changes, and performs a full check at least every 15 minutes
  while writes continue. Old cache records safely fall back to full checking.
- Added a regression that edits one byte in a middle event, keeps the JSONL
  length unchanged, and verifies the next append fails without extending the
  tampered log. `go test ./internal/audit -count=1`, `go test -race
  ./internal/audit -count=1`, and `make check` pass.
- Limitation: periodic append-triggered checks do not establish off-host
  immutability or prove resistance to an attacker able to rewrite both file
  contents and filesystem metadata. Independently administered signed
  checkpoints remain necessary for that threat model.

## Bounded cPanel archive resources — 2026-10-31

- Inspection and extraction independently enforce 50,000 tar headers, 64 MiB
  of retained path bookkeeping, and 128 path components. Capacity reservations
  include 4 KiB per unique extracted path object in addition to expanded bytes.
- Added regressions using a gzip archive with 50,001 empty files, independent
  extraction entry/path limits, metadata-budget rejection, and per-object
  capacity accounting. `go vet ./...`, focused regressions, the full
  `GOMAXPROCS=2 go test -p 1 -timeout 10m ./...` suite, and `make check` pass.
- Limitation: the per-object filesystem-overhead allowance is an admission
  estimate. It does not replace a quota-controlled staging filesystem or
  establish host inode-exhaustion behavior.

## Bounded tenant file listings — 2026-10-31

- Removed the root-owned full-list temporary file from `stepanel-filectl`.
  Directory enumeration now runs as the site account through an `O_NOFOLLOW`
  directory descriptor, reads no more than 5,001 entries, streams output under
  a 2 MiB broker cap, and has a 10-second deadline. The broker serializes
  read-only file requests per tenant across sites.
- Added parser coverage for explicit truncated-source markers and an installed
  host smoke that lists a 10,000-entry directory, asserting the 5,001-record
  output bound and 10-second deadline. Local Go package tests, Bash syntax,
  ShellCheck, Python syntax, a 10,000-entry bounded-output fixture, and docs
  reference checks pass. The installed-host smoke has not yet run locally.

## PostgreSQL restore interpreter boundary — 2026-10-31

- PostgreSQL backups are now emitted as custom-format `pg_dump` archives and
  stored as `databases/<name>.dump`. The privileged restore helper uses
  `pg_restore` under `stepanel_dbload` with the database-owner role and rejects
  plain SQL scripts, preventing psql client commands from being interpreted.
  A root-owned persistent `flock` now serializes PostgreSQL restore and
  interrupted-restore role grant/revoke sequences across helper processes.
  The PostgreSQL installed-server smoke now attempts `\!`, `\copy`, and
  `\o` payloads and attempts cross-tenant `SET ROLE` during a blocked
  concurrent restore. It verifies marker files are absent, the other tenant's
  role cannot be assumed, and the original database remains unchanged.
- The updated helper and PostgreSQL smoke pass `bash -n`, ShellCheck, and
  `git diff --check`; documentation references also validate. Live PostgreSQL
  behavior remains unrun locally and needs the hosted integration result.
- `GOMAXPROCS=2 go test -p 1 -timeout 10m ./...`, `go vet ./...`, Go
  formatting, Bash syntax, ShellCheck, documentation reference/link checks,
  and `git diff --check` passed locally. This workstation has no PostgreSQL
  client/server, so the live PostgreSQL smoke remains dependent on CI.
  Existing PostgreSQL plain-SQL backup archives are intentionally not accepted
  for database restore; the source must be available to create a custom-format
  backup. Exact-source hosted CI is pending for these changes.

## Current validation update — 2026-10-31

- Current code source is `77c355a1f04771cce316284d9323754d1cab9480` (the
  working tree is clean; no product code changed during this validation).
  Focused production-authentication and database-backed TOTP replay tests pass.
  `go test -race ./internal/rootbroker/... ./internal/operations/...` passes
  (Go reported cached results). A fresh 60-second `FuzzNormalizeName` run
  passed 449,099 executions and found no failure. `go vet ./...` also passed
  in the earlier `make check` invocation; that full invocation timed out in
  `TestGitStageQuotaIsSkippedOnlyInDevelopment`, whose isolated rerun passed.
  The complete repeated Go suite is being run by the hosted reliability
  workflow below, rather than duplicated during the installed-host run.
- The exact-source hosted 20-repeat recovery cohort
  [41191938794](https://github.com/cyberducttape/StePanel/actions/runs/41191938794)
  completed repetition 1/20 with all six Ubuntu 24.04, AlmaLinux 9, and Rocky
  Linux 9 Apache/Caddy combinations passing. Repetition 2/20 was in progress
  with no failed shards at the time of this update. The cohort remains open;
  one repetition is not certification.
- A local Rocky Linux 9 KVM full-matrix attempt used source `77c355a`, QEMU
  10.2.1, a 4 GiB/four-vCPU guest, and `cache=none`. It ran for 12,637 seconds
  before the host OOM killer terminated QEMU; the guest console also recorded
  a CPU soft-lockup warning. Confirmed checkpoints through
  `terminate:services` passed. The `terminate:site-state` scenario had reached
  its setup/backup/restore stages, but its termination recovery result and
  later checkpoints were not recorded. This attempt is **interrupted and not
  counted** toward any recovery gate. The result and host-resource limitation
  are documented in
  [`2026-10-31-rocky9-kvm-oom-interrupted.md`](docs/lab-results/2026-10-31-rocky9-kvm-oom-interrupted.md).
- Production status remains **NO-GO**. No product source changed during this
  validation; this entry records test evidence and limitations only.

## Independent audit-checkpoint key rotation — 2026-10-31

- Exact current commit `950837d19fd87760182c6ffe3907fb9d400b9c38` passes local
  `go fmt ./...`, `go vet ./...`, the full Go suite three times, and the full
  race suite. `FuzzNormalizeName` passed a 60-second run (819,437 executions);
  focused race runs for `internal/rootbroker` and `internal/operations` also
  pass. The exact-source release-evidence artifact is
  [41192195978](https://github.com/cyberducttape/StePanel/actions/runs/41192195978).
  The 20-repeat installed-host cohort
  [41191938794](https://github.com/cyberducttape/StePanel/actions/runs/41191938794)
  is now running its first six-host repetition; it has not completed and does
  not yet count toward the repeated-recovery gate.

- After the single-host smoke below, the scheduled exact-code six-host matrix
  [41189484718](https://github.com/cyberducttape/StePanel/actions/runs/41189484718)
  passed all Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 combinations with
  Apache and Caddy. Every host passed one interrupted-workflow matrix and an
  installed WordPress restore probe (5.269–7.308 s RTO). Reviewed diagnostics
  and limitations are in
  [`2026-10-31-six-host-install-matrix-1e6db18.md`](docs/lab-results/2026-10-31-six-host-install-matrix-1e6db18.md).
  This is one iteration per combination, not the required 20–50 repeats or
  power-loss certification.

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
  and an AlmaLinux 9/Caddy/MariaDB installed-host smoke
  [41187605802](https://github.com/cyberducttape/StePanel/actions/runs/41187605802).
  The subsequent six-host matrix completed one recovery iteration and a
  WordPress restore proof on every supported OS/web-server pair. Neither run
  configured a checkpoint remote or exercised remote publication. See the
  [single-host result](docs/lab-results/2026-10-31-almalinux9-install-1e6db18.md)
  and [six-host results](docs/lab-results/2026-10-31-six-host-install-matrix-1e6db18.md).
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

## Descriptor-rooted file-manager operations — 2026-10-31

- Replaced pathname preflight checks followed by pathname-based file-manager
  operations with a single embedded Python helper that opens the site's
  document root and traverses each component using directory descriptors and
  `O_NOFOLLOW`. Reads, listing, atomic upload publication, directory creation,
  no-replace rename, and recursive deletion stay bound to opened descriptors.
- Extended the installed-host smoke with a `runuser` interposer that swaps a
  checked directory for a symlink immediately before the site-user operation.
  It exercises list/read/write/mkdir/rename/delete and verifies sibling files
  remain unchanged. The smoke cannot run on this development host because it
  requires the installed root broker, account-management helper, and managed
  system account.
- Local validation passed: Bash syntax, ShellCheck, embedded Python compile,
  `git diff --check`, and a temporary-directory runtime check covering normal
  operations, symlink confinement, and setgid group inheritance. The
  installed-host smoke exposed a setgid regression in new directories. An
  initial chmod-based correction still failed for users outside the web
  group, so directory creation now relies on kernel setgid inheritance under
  a bounded temporary umask. The rerun for this final correction is pending;
  no host certification is claimed from either failed run.

## Current continuation checkpoint — 2026-11-04

- Exact checked-out source: `633da3d6` (`main`, equal to `origin/main` at
  inspection); worktree was clean before this documentation update.
- `make check` passed on this source: generated API artifacts, shell syntax
  and cgroup/Node runtime contracts, `go vet ./...`, and the full serialized
  Go test suite (`GOMAXPROCS=2`, `-p 1`, 10-minute package timeout).
- Exact-source evidence generated from commit
  `d462aae7450ae8cfc92de33d45def715109325ef` found that the code content is
  equivalent to passing CodeQL and CI integration runs on `633da3d`, plus a
  7/7 installation-smoke run and ext4 quota smoke. Documentation has no
  equivalent run because this checkpoint changed `PROGRESS.md`. The report
  counted 4 executable checks passed and 5 incomplete, with 17 of 21 P0 gates
  incomplete. It is recorded in the command output, not checked in as a
  release artifact.
- The latest repeated recovery cohort is not equivalent to this source. Its
  run `41191938794` had one failed host job in repeat 2, AlmaLinux 9/Apache/
  MariaDB; GitHub returned 403 for unauthenticated job logs and 401 for its
  diagnostics artifact, so the failure cause remains unknown and must not be
  guessed or marked resolved.
- The remaining local typed-operation milestone is M0s in
  `DEVELOPMENT_PLAN.md`: six durable site-operation routes still replay HTTP
  handlers. No route was changed in this checkpoint; the staging and restore
  handlers each own multi-resource rollback logic, so their extraction needs
  dedicated service-boundary work and regression tests rather than a dispatch
  shim.
- Installed-host recovery certification was not run. The local KVM harness
  requires a 4 GiB guest plus 2 GiB host reserve; available host memory was
  below that preflight. No KVM certification process was active at inspection.
  A local `gh auth status` also reported an invalid GitHub token, so current
  hosted-run status could not be queried through `gh`; the unauthenticated
  exact-source evidence generator did retrieve the public workflow summary.
  Neither limitation affects the passing local `make check` result.
- Production remains **NO-GO**. This software check does not close repeated
  installed-host recovery, physical power-loss/ENOSPC, current-source
  application recovery, live alert delivery, capacity, upgrade, or
  independent security-review gates.

### Next actions

1. Continue M0s by extracting one operation into a typed service with explicit
   principal, lock, audit, and recovery contracts; add direct worker and HTTP
   adapter regression coverage before converting another route.
2. Run the corrected descriptor-rooted file-manager smoke on an installed
   host and reconcile its outstanding setgid regression evidence.
3. Run current-source installed-host recovery and repeated failure matrices
   on a runner that satisfies the KVM resource preflight; retain the exact
   source SHA and artifacts.

## Race-gate timeout alignment — 2026-11-04

- The first local `make test-race` run hit the Makefile's inherited 10-minute
  package timeout in the root package. The test process was still CPU-active;
  the timeout stack was in the site-operation test fixture opening and
  migrating SQLite databases, not in a reported race.
- `Makefile` now has an independent `RACE_TIMEOUT` default of 15 minutes,
  matching `.github/workflows/ci-integration.yml` and
  `scripts/production-gate.sh`. The ordinary unit-test timeout remains 10
  minutes.
- `make test-race` then passed on this checkout across all packages. The root
  package completed in 664.618 seconds; internal packages passed afterward.
  This is local evidence and does not replace hosted CI or installed-host
  certification.
- After the Makefile change, `make check` also passed on the updated tree:
  generated API artifacts, shell contracts, vet, and all Go packages (root
  package 92.370 seconds). `git diff --check` passed before commit.
- Exact-source evidence for `63e84358c2fa08843053859c185429baa91be628`
  reports 0 of 9 hosted checks as equivalent because this source changed the
  Makefile; their latest results are context only. Hosted CI must run against
  this code before its CI status is considered current. The P0 table remains
  at 17 incomplete gates and production remains **NO-GO**.

## Continuation — broker restart recovery and customer portal context — 2026-11-15

- Worktree: `/home/stephanl/StePanel-next`, branch `next-work`, based on
  `99876a2d8dd8cb1cfcaf1c50f762881a4c92d703`; no changes were pushed or
  merged.
- Addressed the installed-host broker restart failure: the unprivileged broker
  client retries only `ENOENT`/`ECONNREFUSED` connection attempts before any
  request bytes are sent. It never retries an ambiguous mutation after
  connecting. Creation recovery journals distinguish retryable broker
  unavailability from operator intervention and retry rollback after restart.
- The updated Rocky Linux 9.8 installed host passed the focused broker
  interruption smoke at 0.2, 1, and 3 second kill points. Each creation
  completed after broker restart, each site terminated cleanly, and the smoke
  reported `root broker interruption smoke passed`. This is targeted evidence;
  the enclosing installed-host run was interrupted and its overall matrix is
  not a pass. The prior round-2 failure remains a reproduced scenario now
  covered by this focused rerun, not a completed repeated-host certification.
- Customer UI changes guard optional WordPress creation controls and add the
  controls to the customer form; bind file editor reads/saves, folder actions,
  and uploads to their originating site/path; clear rows and disable the
  toolbar while a new directory loads; preserve last-known inventory and show
  retry on overview failure; and keep route navigation separate from the
  operator scroll spy. Updated docs correct WordPress, CLI, file-manager, and
  single-host network descriptions. M0y in `DEVELOPMENT_PLAN.md` tracks the
  objective browser acceptance contract.
- Local verification: `make check` passed (`go vet ./...` and all Go packages;
  root package 99.382s), frontend unit tests passed 17/17, browser test
  discovery listed 23 tests, JavaScript syntax checks passed, and
  `git diff --check` passed. `npm run test:e2e` collected all 23 tests but
  skipped them because `STEPANEL_E2E_BASE_URL` and credentials are
  unavailable; no browser workflow is counted as passed.
- Remaining blockers: hosted exact-source CI, browser execution of M0y, full
  installed-host failure matrix, physical power-loss/ENOSPC/database-outage
  repeats, repeated WordPress application recovery, upgrade certification,
  capacity measurements, live notification delivery evidence, and independent
  security review remain open. Production remains **NO-GO**.

## Merge-source verification — 2026-11-15

- Merged `next-work` into the latest `origin/main` (which advanced to
  `7da02b21` after the feature branch was created) with no conflicts. Merge
  source is `e6602f25af31d1e6ee43386b747b68f547832135`.
- `make check` passed on the merged source after downloading the updated
  dependencies: generated API artifact validation, shell/cgroup/runtime
  contracts, `go vet ./...`, and all Go tests. Root package completed in
  99.575s; rootbroker package passed in 6.014s.
- The customer Playwright flow remains unverified at runtime: the suite
  collected 23 tests and skipped all 23 because no disposable seeded instance
  credentials were configured. Browser test discovery and JS syntax checks
  alone are not acceptance evidence for the UI behavior.

## Customer browser regression correction — 2026-11-15

- The first exact-source UI run on `e1ccfdec` failed because the test's first
  `/api/sites/overview` request belonged to the operator site grid, not the
  customer panel. The failed job's Playwright trace confirmed the panel itself
  had loaded normally; the failure was in test fault targeting.
- Changed the fixture to let the site grid request succeed and inject a 503
  into the following customer overview request. The retry then returns the
  real mocked inventory. This specifically exercises “unavailable” versus
  “empty” behavior.
- Source `cdbb2519` passed the local disposable UI run: 22 browser tests passed,
  one customer-credential isolation test was expectedly skipped. The targeted
  customer workflow passed, including the 503/retry case, WordPress form
  initialization, delayed cross-site file read, stale-row clearing, and
  site-bound save.
- The exact-source GitHub `ui-e2e` job passed on `cdbb2519` in run
  [43781918565](https://github.com/cyberducttape/StePanel/actions/runs/43781918565).
  The Go test/coverage job passed formatting, release check, vet, full tests,
  ENOSPC, benchmarks, and coverage floors. The complete integration run then
  passed race tests, recovery drills, real tmpfs ENOSPC recovery, MySQL 8.0
  restore and restore-ENOSPC checks, PostgreSQL restore privilege checks,
  static analysis, and the container/infrastructure checks. CodeQL passed in
  [43781918626](https://github.com/cyberducttape/StePanel/actions/runs/43781918626).
  The earlier UI failure on `e1ccfdec` was cancelled after the fixture fix;
  the `cdbb2519` run is green. The separate install smoke on `e1ccfdec` is
  still running and must be reviewed independently; the `cdbb2519` change
  after it is test-fixture-only.

## Current-source browser verification — 2026-11-20

- Source: `1a62c4201775e19fb305d58911488543ff6e9b3e`.
- Built a fresh binary from this checkout with `go build -o
  /tmp/stepanel-current ./cmd/stepanel`, then ran
  `scripts/e2e-ui-test.sh /tmp/stepanel-current` against its disposable
  control plane. Playwright and axe passed 24 tests; one credential-isolation
  test was skipped because its optional customer credentials were not set.
  The account-owner UI flow passed, including WordPress creation controls,
  page-error detection, inventory failure/retry, delayed file reads across a
  site switch, stale-row clearing, and site-bound writes. Every operator site
  workspace tab passed axe with both unavailable and empty inventory data.
- An initial local attempt used the repository's pre-existing `./stepanel`
  binary (mtime 2026-11-01) and failed several checks. That run is excluded:
  it did not represent current source. Rebuilding from the exact checkout
  removed those failures.
- Hosted CI `44703898628` ran the same source; its `ui-e2e` job passed, while
  the full Go test job was still running at the time of this update. Installed
  recovery cohort `44702646288` is also still running; no host repetitions
  from that cohort are counted until its results and artifacts are reviewed.
- This UI evidence closes no host-level production gates. Production remains
  **NO-GO**.
