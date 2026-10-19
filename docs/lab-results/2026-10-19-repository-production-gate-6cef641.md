# Repository production gate — 2026-10-19

**Exact code source:** `6cef6413edd7ad97d44be376dbb978b3ae1caf6f`  
**Result:** Repository gate passed; host-level production certification remains **NO-GO**.

## Changes under test

The root broker now rejects a control-plane database at startup if its migration
ledger, resource-lock schema, or site-identity schema is absent. It validates
the declared SQLite types of fencing columns, the `resource_locks.resource_key`
primary key, and a non-partial unique index on `site_identities.username`.
The installer initializes the lock table as the unprivileged service account
before activating the broker. Regression tests cover missing databases,
unrelated SQLite files, incomplete schemas, malformed lease-column types,
missing identity uniqueness, and lock-table initialization.

## Repository verification

- `go vet ./...` passed.
- `go test -p 1 -timeout 10m ./... -coverprofile=... -covermode=atomic` passed;
  aggregate statement coverage was 57.1% and every configured package floor
  passed.
- `go test -p 1 -race -timeout 10m ./...` passed; the root package completed
  in 474.209 seconds.
- Focused fencing database and initialization tests passed, including under the
  race detector.
- Helper process-group cancellation and timeout tests passed in 10 repeated
  runs.
- Repository recovery drills passed: partial SQL-import cleanup, interrupted
  transaction recovery, configuration rollback, and pending runtime
  reconciliation.
- Control-plane benchmarks (one iteration): 10/100/500-site mutation
  0.797/0.729/1.095 ms/op; panel/worker contention 0.761 ms/op; process
  contention 0.756 ms/op; 50 backup/restore jobs 48.3 ms/batch; audit-heavy
  mutations 1.056 ms/op.
- Mixed HTTP load passed: 200 sites, 180 requests, four readers, and 20 backup
  jobs; mean 4.1 ms, p95 14.895 ms, maximum 35.6 ms.
- Documentation links/references, workflow bounds, Grafana dashboard, quota
  fixtures, pinned actions, embedded assets, and shell syntax checks passed.

The recovery drill output was written to `/tmp/stepanel-recovery-drills.md` on
the test host; it is not part of the repository.

## Gates still open

At 2026-10-19 04:51 UTC, hosted CI and CodeQL for this source were queued; both
installation smokes were pending without assigned jobs. Three older-source
recovery runs remain active at disposable systemd-host startup. These repository
checks do not establish installed-host or disaster-recovery certification.
Physical power-loss testing, broader real-host ENOSPC (including database
restore/publication and XFS), repeated installed-host recovery, notification
outage/retry, certified capacity, supported-platform/upgrade policy, and
independent security review remain outstanding. Production remains **NO-GO**.
