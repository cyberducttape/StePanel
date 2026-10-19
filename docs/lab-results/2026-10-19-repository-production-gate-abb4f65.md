# Repository production gate — 2026-10-19

**Checkout:** `abb4f6530038dc98d674d9cd59423a16ce4e26ce`  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed.

## Checks and results

- `go vet ./...` passed.
- Full tests passed. Aggregate statement coverage was 56.9%; all configured
  package floors passed. Root-package coverage was 53.6%, operations 86.4%,
  root broker 71.4%, importer 67.3%, and internal/jobs 100%.
- `go test -p 1 -race -timeout 10m ./...` passed. The root package took
  474.732 seconds; every other package passed.
- Repository recovery drills passed: partial SQL import cleanup (25 s),
  interrupted transaction recovery (1 s), configuration rollback (2 s), and
  pending runtime reconciliation (1 s). These use synthetic temporary data;
  they do not prove host power-loss or ENOSPC behavior.
- Control-plane benchmarks (one iteration): site mutations at 10/100/500
  sites were 0.788/0.929/0.791 ms per operation; panel/worker contention was
  0.770 ms; separate-process contention was 0.995 ms; a 50-job backup/restore
  batch was 48.363 ms; audit-heavy mutations were 1.064 ms.
- Synthetic mixed HTTP load passed with 200 sites, 180 requests, four readers,
  and 20 backup jobs: average 3.6 ms, p95 14.355 ms, maximum 37.1 ms.
- Documentation links and source references, dashboard metrics, quota fixture
  capacity, immutable action pins, embedded assets, and shell syntax passed.

At capture, exact-head [CI](https://github.com/cyberducttape/StePanel/actions/runs/39183832805)
and [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39183832755)
were queued. Standard [installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39183832754)
and [quota-enabled installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39183832767)
were pending. These results are not counted as passes.

## What this does not certify

The repository gate does not prove physical-host power-loss recovery, all
real-host ENOSPC boundaries, installed-host behavior of aggregate Git staging
quotas (including XFS), external notification delivery and outage recovery,
production capacity, untested runtime combinations, or an independent
security assessment. The production decision remains **NO-GO**.
