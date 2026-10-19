# Repository production gate — 2026-10-19

**Exact code source:** `0b5879d8e35299a0565a0292f888aa6e5e302b8f`  
**Result:** Repository gate passed; host-level production certification remains **NO-GO**.

## Changes under test

The root broker now rejects a control-plane database at startup if its migration
ledger, resource-lock schema, or site-identity schema is absent. The installer
initializes the lock table as the unprivileged service account before it
activates the broker. Regression tests cover missing databases, unrelated
SQLite files, incomplete schemas, and initialization of the fencing table.

## Repository verification

- `go vet ./...` passed.
- `go test -p 1 -timeout 10m ./... -coverprofile=... -covermode=atomic` passed;
  aggregate statement coverage was 57.1% and every configured package floor
  passed.
- `go test -p 1 -race -timeout 10m ./...` passed; the root package completed
  in 476.916 seconds.
- Focused fencing database and initialization tests passed, including under the
  race detector.
- Helper process-group cancellation and timeout tests passed in 10 repeated
  runs.
- Repository recovery drills passed: partial SQL-import cleanup, interrupted
  transaction recovery, configuration rollback, and pending runtime
  reconciliation.
- Control-plane benchmarks (one iteration): 10/100/500-site mutation
  0.769/0.925/0.769 ms/op; panel/worker contention 0.982 ms/op; process
  contention 0.772 ms/op; 50 backup/restore jobs 51.1 ms/batch; audit-heavy
  mutations 1.104 ms/op.
- Mixed HTTP load passed: 200 sites, 180 requests, four readers, and 20 backup
  jobs; mean 4.0 ms, p95 13.101 ms, maximum 37.0 ms.
- Documentation links/references, workflow bounds, Grafana dashboard, quota
  fixtures, pinned actions, embedded assets, and shell syntax checks passed.

The recovery drill output was written to `/tmp/stepanel-recovery-drills.md` on
the test host; it is not part of the repository.

## Gates still open

These repository checks do not establish installed-host or disaster-recovery
certification. Exact-source hosted CI, CodeQL, and installation smokes have not
completed. Physical power-loss testing, broader real-host ENOSPC (including
database restore/publication and XFS), repeated installed-host recovery,
notification outage/retry, certified capacity, supported-platform/upgrade
policy, and independent security review remain outstanding. Production remains
**NO-GO**.
