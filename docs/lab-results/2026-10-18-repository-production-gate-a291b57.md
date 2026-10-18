# Repository production gate — 2026-10-18

**Checkout:** `a291b57dff7a406e30c95b637716c4ea0d7e94f7`  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed.

## Checks and measurements

- `go vet ./...`, the full test suite, and every configured coverage floor
  passed. Aggregate statement coverage was 56.9% against the 50% floor;
  every per-package target passed. The root package reported 53.3%, root broker
  72.6%, importer 67.3%, and internal/jobs 100%.
- `go test -p 1 -race -timeout 10m ./...` passed. The root package took
  478.187 seconds; all other packages passed.
- Repository recovery drills passed: partial SQL import cleanup, interrupted
  transaction recovery, configuration rollback, and pending runtime
  reconciliation. These use synthetic temporary data, not disposable-host
  recovery.
- Control-plane one-iteration benchmarks: site mutations at 10/100/500 sites
  were 1.284/1.291/1.782 ms per operation; panel/worker contention 1.236 ms;
  separate-process contention 1.216 ms; a 50-job backup/restore batch 87.614
  ms; audit-heavy mutations 1.538 ms.
- Synthetic mixed HTTP load passed: 200 sites, 180 requests, four readers, and
  20 backup jobs; average 6.4 ms, p95 15.118 ms, maximum 79.9 ms.
- Documentation links and references (88 documents), Grafana dashboard, quota
  fixture capacity, immutable action pins, embedded assets, and shell syntax
  passed.

At capture, exact-head [CI](https://github.com/cyberducttape/StePanel/actions/runs/39169580830)
and [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39169580916)
were queued; standard and quota-enabled install runs
([standard](https://github.com/cyberducttape/StePanel/actions/runs/39169580810),
[quota](https://github.com/cyberducttape/StePanel/actions/runs/39169580816))
were pending without jobs. None are counted as passes. The 20-repeat
installed-host cohort remains incomplete.

## What this does not certify

Repository tests and synthetic benchmarks do not prove physical-host power
loss recovery, all real-host ENOSPC boundaries, installed-host WordPress
recovery across the supported matrix, notification outage and retry,
installed-host upgrade/rollback, production capacity, broader runtime support,
or independent security review. Git release staging also remains a production
blocker: its configured size check occurs after clone writes. The production
decision remains **NO-GO**.
