# Production-readiness progress

**Updated:** 2026-10-29  
**Published base:** `ffb9e02419dd8c8ae3218bde60cbdd993349bb6c`  
**Verified local fix commit:** `2d2e2cda02022b49aad0fbb44b89fdc6a6585fb8`  
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

- `tests/e2e/critical-navigation.spec.js`: expect the actual upper-bound quota
  copy (`up to … estimated bytes remain`).
- `CHANGELOG.md`: record the corrected browser regression assertion.
- `DEVELOPMENT_PLAN.md`: prioritized milestones and objective acceptance
  criteria for all open production gates.
- `PROGRESS.md`: this evidence and next-step record.

The E2E fix is committed locally as `2d2e2cd`; the plan/progress documents are
also committed on the local branch. Nothing has been pushed or dispatched
remotely. The E2E run used a binary rebuilt from this worktree.

## Release blockers and evidence gaps

The authoritative P0 table still marks installed-host power loss and broad
ENOSPC testing, 20–50 repeated recovery runs, current-source application
restore/runtime certification, live notification delivery, capacity
certification, upgrade/security lifecycle evidence, and release-evidence
reconciliation as partial or open. Existing passing code tests and historical
QEMU runs do not close those gates.

The documented Rocky Linux KVM lab cannot run on this workstation: `/dev/kvm`
is not accessible to the current user, and available memory is below the lab's
4 GiB guest plus 2 GiB host-reserve requirement. No host certification was
claimed or attempted.

## Next steps

1. Commit the verified plan/progress documents locally.
2. Obtain approval before pushing or dispatching remote workflows. Then require
   exact-source CI, CodeQL, documentation, and release-evidence success.
3. Obtain a suitably provisioned disposable KVM host/runner; execute the
   remaining Gate 5 matrix and retain reviewed evidence.
4. Continue with application restore, operational notification, capacity,
   upgrade, and independent security gates in `DEVELOPMENT_PLAN.md`.

