# Repository production gate — 2026-10-19

**Checkout:** `2ebd28dce1379e9e5a596adfe6ba1c732dde668d`  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed.

## Checks and results

- `go vet ./...` passed.
- Full tests passed. Aggregate statement coverage was 56.9%; all configured
  package floors passed. Root-package coverage was 53.6%, root broker 71.2%,
  importer 67.3%, and internal/jobs 100%.
- `go test -p 1 -race -timeout 10m ./...` passed. The root package took
  475.918 seconds; every other package passed.
- Repository recovery drills passed: partial SQL import cleanup (24 s),
  interrupted transaction recovery (1 s), configuration rollback (1 s), and
  pending runtime reconciliation (2 s). These use synthetic temporary data;
  they do not prove host power-loss or ENOSPC behavior.
- Control-plane benchmarks (one iteration): site mutations at 10/100/500
  sites were 0.958/0.899/0.809 ms per operation; panel/worker contention was
  0.990 ms; separate-process contention was 0.758 ms; a 50-job backup/restore
  batch was 52.550 ms; audit-heavy mutations were 1.151 ms.
- Synthetic mixed HTTP load passed with 200 sites, 180 requests, four readers,
  and 20 backup jobs: average 4.1 ms, p95 14.132 ms, maximum 42.2 ms.
- Documentation links and source references, dashboard metrics, quota fixture
  capacity, immutable action pins, embedded assets, and shell syntax passed.

At capture, exact-head [CI](https://github.com/cyberducttape/StePanel/actions/runs/39182377301)
and [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39182377230)
were queued. Standard [installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39182377225)
and [quota-enabled installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39182377202)
were pending. These results are not counted as passes.

## What this does not certify

The repository gate does not prove physical-host power-loss recovery, all
real-host ENOSPC boundaries, installed-host behavior of aggregate Git staging
quotas (including XFS), external notification delivery and outage recovery,
production capacity, untested runtime combinations, or an independent
security assessment. The production decision remains **NO-GO**.
