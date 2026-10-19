# Repository production gate — 2026-10-19

**Checkout:** `91f402df34c9393efe50e0c36c63882526819717`  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed on the pushed source.

## Checks and results

- `go vet ./...` passed.
- Full tests passed. Aggregate statement coverage was 56.9%; all configured
  package floors passed. Root-package coverage was 53.6%, operations 86.4%,
  root broker 71.4%, importer 67.3%, and internal/jobs 100%.
- `go test -p 1 -race -timeout 10m ./...` passed. The root package took
  476.281 seconds; every other package passed.
- Repository recovery drills passed: partial SQL import cleanup (29 s),
  interrupted transaction recovery (1 s), configuration rollback (2 s), and
  pending runtime reconciliation (1 s). These use synthetic temporary data;
  they do not prove host power-loss or ENOSPC behavior.
- Control-plane benchmarks (one iteration): site mutations at 10/100/500
  sites were 0.811/0.751/0.851 ms per operation; panel/worker contention was
  0.967 ms; separate-process contention was 0.703 ms; a 50-job backup/restore
  batch was 48.240 ms; audit-heavy mutations were 1.118 ms.
- Synthetic mixed HTTP load passed with 200 sites, 180 requests, four readers,
  and 20 backup jobs: average 4.1 ms, p95 15.530 ms, maximum 35.5 ms.
- Documentation links and source references, dashboard metrics, quota fixture
  capacity, immutable action pins, embedded assets, and shell syntax passed.

At capture, exact-head [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39185090755)
passed, [CI](https://github.com/cyberducttape/StePanel/actions/runs/39185090738)
was in progress, and standard
[installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39185090776)
and [quota-enabled installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39185090733)
were pending. The latter three results are not counted as passes.

## What this does not certify

The repository gate does not prove physical-host power-loss recovery, all
real-host ENOSPC boundaries, installed-host behavior of aggregate Git staging
quotas (including XFS), external notification delivery and outage recovery,
production capacity, untested runtime combinations, or an independent
security assessment. The production decision remains **NO-GO**.
