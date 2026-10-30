# Production-readiness progress

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

1. Obtain approval before pushing or dispatching remote workflows. Then require
   exact-source CI, CodeQL, documentation, and release-evidence success.
2. Obtain a suitably provisioned disposable KVM host/runner; execute the
   remaining Gate 5 matrix and retain reviewed evidence.
3. Continue with application restore, operational notification, capacity,
   upgrade, and independent security gates in `DEVELOPMENT_PLAN.md`.

