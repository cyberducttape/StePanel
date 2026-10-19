# Repository production gate — 2026-10-19 (`4f764d5`)

**Source:** `4f764d599f8cb4ead5a2ff71f15bc201198cc87b`  
**Executed:** 2026-10-19, completed at approximately 03:18 UTC  
**Command:** `bash scripts/production-gate.sh`  
**Result:** PASS for repository-level checks; production host certification remains NO-GO.

## Results

- `gofmt` and `go vet ./...` passed.
- Full `go test ./...` with atomic coverage passed: 57.0% overall. All package
  coverage floors passed; root package 53.7%, broker 71.4%, importer 67.3%,
  and state package 98.7%.
- Full race suite passed. Root package completed in 481.742 seconds.
- Repository recovery drills passed: partial SQL-import cleanup (24s),
  interrupted transaction recovery (1s), configuration rollback (1s), and
  pending runtime reconciliation (2s). The generated drill record is
  `/tmp/stepanel-recovery-drills.md` on the execution host and is not itself a
  committed artifact.
- Control-plane benchmark set passed. Measured once on an AMD Ryzen 7 7735U:
  site mutations at 10/100/500 sites took 1.250/0.697/0.844 ms per operation;
  the 50-backup/restore batch took 45.6 ms. These single-run synthetic numbers
  are not a certified capacity envelope.
- Mixed HTTP load passed: 200 simulated sites, 180 requests, four readers, and
  20 backup jobs; average latency 4.0 ms, p95 14.877 ms, maximum 35.3 ms.
- Documentation links/references, smoke-workflow bounds, Grafana dashboard,
  quota fixture syntax, action pins, embedded assets, and shell syntax passed.

## Hosted checks at capture

The exact-head GitHub CI run `39193098881` and CodeQL run `39193098787` were
queued without started jobs. Standard installation smoke `39193098805` and
quota-enabled installation smoke `39193098801` were pending without assigned
jobs. These hosted checks are not counted as passes.

## Explicitly not established

This repository gate uses unit/integration tests and synthetic temporary data.
It does not establish installed-host aggregate quota enforcement (including
XFS), physical power-loss recovery, broad real-host ENOSPC behavior, repeated
installed-host recovery, or production capacity. The documented 20-repeat
installed-host recovery cohort, notification outage/retry evidence, supported
platform and upgrade policy, and independent security review remain open.
Production deployment remains **NO-GO**.
