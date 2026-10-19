# Repository production gate — 2026-10-19 (`c291614`)

**Source:** `c2916141df8553bccd9b181243170639bad59db0`  
**Executed:** 2026-10-19, completed at approximately 03:37 UTC  
**Command:** `bash scripts/production-gate.sh`  
**Result:** PASS for repository-level checks; production host certification remains NO-GO.

## Results

- Formatting and `go vet ./...` passed.
- Full `go test ./...` with atomic coverage passed: 57.1% overall. All package
  floors passed; root package 53.7%, broker 71.4%, importer 67.3%, and state
  package 98.7%.
- Full race suite passed. Root package completed in 473.323 seconds.
- Repository recovery drills passed: partial SQL-import cleanup (24s),
  interrupted transaction recovery (1s), configuration rollback (1s), and
  pending runtime reconciliation (2s). The generated result file was
  `/tmp/stepanel-recovery-drills.md` on the execution host and is not a
  committed artifact.
- Control-plane benchmarks passed. Single-run synthetic measurements on an
  AMD Ryzen 7 7735U: site mutation at 10/100/500 sites took 0.959/0.928/0.778
  ms per operation; a 50-backup/restore batch took 48.3 ms. These are not a
  certified capacity envelope.
- Mixed HTTP load passed: 200 simulated sites, 180 requests, four readers, and
  20 backup jobs; average latency 3.9 ms, p95 13.560 ms, maximum 36.2 ms.
- Documentation links/references, smoke-workflow bounds, Grafana dashboard,
  quota fixture syntax, action pins, embedded assets, and shell syntax passed.

## Hosted checks at capture

Exact-head GitHub CI run `39194673966` and CodeQL run `39194673912` were queued
without started jobs. Standard installation smoke `39194673900` and
quota-enabled installation smoke `39194673947` were pending without assigned
jobs. These hosted checks are not counted as passes.

## Explicitly not established

This repository gate uses tests and synthetic temporary data. It does not
establish installed-host aggregate quota enforcement (including XFS), physical
power-loss recovery, broad real-host ENOSPC behavior, repeated installed-host
recovery, or production capacity. The documented 20-repeat installed-host
recovery cohort, notification outage/retry evidence, supported-platform and
upgrade policy, and independent security review remain open. Production
deployment remains **NO-GO**.
