# Production-readiness progress

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

The documented Rocky Linux KVM lab cannot run on this workstation: `/dev/kvm`
is `root:kvm` mode `0660` and the current user is not in the `kvm` group;
noninteractive sudo is unavailable. Memory currently meets the guest plus
reserve requirement, but there is no cached guest image and Docker/Podman are
not installed. No host certification was claimed or attempted.

## Next steps

1. Commit and push this recovery-evidence change, then require exact-source CI,
   CodeQL, documentation, and release-evidence success.
2. Obtain a suitably provisioned disposable KVM host/runner; execute the
   remaining Gate 5 matrix and retain reviewed evidence.
3. Continue with application restore, operational notification, capacity,
   upgrade, and independent security gates in `DEVELOPMENT_PLAN.md`.

