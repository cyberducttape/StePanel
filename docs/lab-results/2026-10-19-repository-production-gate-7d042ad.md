# Repository production gate — 2026-10-19 (`7d042ad`)

**Source:** `7d042ad879d4489380ba3ae00e17f36f5f19bedc`  
**Executed:** 2026-10-19, completed at 04:06 UTC  
**Command:** `bash scripts/production-gate.sh`  
**Result:** PASS for repository-level checks; production host certification remains NO-GO.

## Results

- Formatting and `go vet ./...` passed.
- Full `go test ./...` with atomic coverage passed: 57.0% overall. All package
  floors passed; root package 53.7%, broker 71.4%, importer 67.3%, and state
  package 98.7%.
- Full race suite passed. Root package completed in 469.151 seconds.
- Repository recovery drills passed: partial SQL-import cleanup (23s),
  interrupted transaction recovery (2s), configuration rollback (1s), and
  pending runtime reconciliation (1s). The generated result file
  `/tmp/stepanel-recovery-drills.md` is on the execution host, not a committed
  artifact.
- Control-plane benchmarks passed. Single-run synthetic measurements on an
  AMD Ryzen 7 7735U: site mutations at 10/100/500 sites took 0.826/0.943/0.813
  ms per operation; panel/worker contention took 0.787 ms, process contention
  0.742 ms, and a 50-backup/restore batch 49.4 ms. These are not a certified
  capacity envelope.
- Mixed HTTP load passed: 200 simulated sites, 180 requests, four readers, and
  20 backup jobs; average latency 4.0 ms, p95 13.561 ms, maximum 38.3 ms.
- Documentation links/references, smoke-workflow bounds, Grafana dashboard,
  quota fixtures, action pins, embedded assets, and shell syntax passed.

## Hosted checks at capture

Exact-head GitHub CI run `39196728669` and CodeQL run `39196728689` were queued
without started jobs. Standard installation smoke `39196728705` and
quota-enabled installation smoke `39196728693` were pending without assigned
jobs. These hosted checks are not counted as passes. Older `633c013` recovery
shards remain in progress at “Start disposable systemd host”; they do not
certify this source.

## Explicitly not established

This repository gate uses tests and synthetic temporary data. It does not
establish installed-host aggregate quota enforcement (including XFS), physical
power-loss recovery, broad real-host ENOSPC behavior, repeated installed-host
recovery, or production capacity. Notification outage/retry evidence,
supported-platform and upgrade policy, and independent security review remain
open. Production deployment remains **NO-GO**.
