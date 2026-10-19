# Repository production gate — 2026-10-19

**Checkout:** `d79e831e07434d195ad081c7562d02b85b14d382`  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed.

## Checks and results

- `go vet ./...` passed.
- Full tests passed. Aggregate statement coverage was 56.9%; all configured
  package floors passed. Root-package coverage was 53.6%, root broker 71.2%,
  importer 67.3%, and internal/jobs 100%.
- `go test -p 1 -race -timeout 10m ./...` passed. The root package took
  472.031 seconds; every other package passed.
- Repository recovery drills passed: partial SQL import cleanup (23 s),
  interrupted transaction recovery (2 s), configuration rollback (1 s), and
  pending runtime reconciliation (1 s). These use synthetic temporary data;
  they do not prove host power-loss or ENOSPC behavior.
- Control-plane benchmarks (one iteration): site mutations at 10/100/500
  sites were 0.912/0.746/0.785 ms per operation; panel/worker contention was
  0.810 ms; separate-process contention was 0.710 ms; a 50-job backup/restore
  batch was 48.390 ms; audit-heavy mutations were 1.318 ms.
- Synthetic mixed HTTP load passed with 200 sites, 180 requests, four readers,
  and 20 backup jobs: average 4.0 ms, p95 13.216 ms, maximum 36.5 ms.
- Documentation links and source references, dashboard metrics, quota fixture
  capacity, immutable action pins, embedded assets, and shell syntax passed.

At capture, exact-head [CI](https://github.com/cyberducttape/StePanel/actions/runs/39180552004)
was queued, [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39180552043)
and standard [installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39180551991)
were pending, and [quota-enabled installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39180551995)
was pending. These results are not counted as passes.

## What this does not certify

The repository gate does not prove physical-host power-loss recovery, all
real-host ENOSPC boundaries, installed-host behavior of aggregate Git staging
quotas (including XFS), external notification delivery and outage recovery,
production capacity, untested runtime combinations, or an independent
security assessment. The production decision remains **NO-GO**.
