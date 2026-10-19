# Repository production gate — 2026-10-19

**Exact code source:** `6e8e28057f6e454cfa8e9f22c466c9e5f4684c62`  
**Result:** Repository gate passed; host-level production certification remains **NO-GO**.

## Changes under test

Root-broker admission now verifies each fencing token with the configured lease
watchdog safety margin before dispatch. A lease already near expiry is rejected
before the privileged helper starts, avoiding a window where the first
interval-delayed watchdog check could occur after lease takeover. Regression
coverage verifies that the helper does not start for such a lease. The
authoritative release gate records this as repository evidence only; killing
the broker during installed-host mutations and proving takeover/reconciliation
remain open.

## Repository verification

- `go vet ./...` passed.
- `go test -p 1 -timeout 10m ./... -coverprofile=... -covermode=atomic` passed;
  aggregate statement coverage was 57.1% and all configured package floors
  passed. The root broker package measured 71.4%.
- `go test -p 1 -race -timeout 10m ./...` passed; the root package completed
  in 474.346 seconds.
- The targeted fencing watchdog regressions passed 10 repetitions; the full
  root-broker race package also passed independently.
- Repository recovery drills passed: partial SQL-import cleanup, interrupted
  transaction recovery, configuration rollback, and pending runtime
  reconciliation.
- Control-plane benchmarks (one iteration): 10/100/500-site mutation
  0.903/0.951/0.847 ms/op; panel/worker contention 0.742 ms/op; process
  contention 0.721 ms/op; 50 backup/restore jobs 49.966 ms/batch; audit-heavy
  mutations 1.403 ms/op.
- Mixed HTTP load passed: 200 sites, 180 requests, four readers, and 20 backup
  jobs; mean 4.0 ms, p95 13.384 ms, maximum 35.6 ms.
- Documentation links/references, workflow bounds, Grafana dashboard, quota
  fixtures, pinned actions, embedded assets, shell syntax, and vet checks passed.

The recovery drill output was captured at 2026-10-19 05:09 UTC on the test
host; it is repository-level synthetic evidence, not installed-host proof.

## Gates still open

At 2026-10-19 05:10 UTC, exact-head hosted CI and CodeQL were queued, and both
installation smokes were pending without assigned jobs. Three older-source
`633c013` recovery runs remained in progress and do not certify current source.
Repository checks do not establish installed-host or disaster-recovery
certification. Physical power-loss testing, broader real-host ENOSPC (including
database restore/publication and XFS), repeated installed-host recovery,
notification outage/retry, certified capacity, supported-platform/upgrade
policy, and independent security review remain outstanding. Production remains
**NO-GO**.
