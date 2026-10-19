# Repository production gate — 2026-10-19

**Verified source:** `77f079064edeb6b8417aebda328ab7646232d60a`  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed. The gate ran with the
same tracked source content later committed as the verified source above.

## Checks and results

- `go vet ./...` passed.
- Full tests passed. Aggregate statement coverage was 57.0%; root-package
  coverage was 53.5%, and all configured package floors passed. The
  `internal/state` package reached 98.7% after adding direct bounded-reader
  tests.
- `go test -p 1 -race -timeout 10m ./...` passed. The root package took
  476.969 seconds; all other packages passed.
- Repository recovery drills passed: partial SQL import cleanup (29 s),
  interrupted transaction recovery (2 s), configuration rollback (1 s), and
  pending runtime reconciliation (2 s). These use synthetic temporary data.
- Control-plane benchmarks (one iteration): site mutations at 10/100/500
  sites were 0.945/0.958/1.468 ms per operation; panel/worker contention was
  0.982 ms; separate-process contention was 0.824 ms; a 50-job backup/restore
  batch was 52.380 ms; audit-heavy mutations were 1.172 ms.
- Synthetic mixed HTTP load passed with 200 sites, 180 requests, four readers,
  and 20 backup jobs: average 3.8 ms, p95 12.224 ms, maximum 40.3 ms.
- Documentation links and source references, dashboard metrics, quota-fixture
  capacity and syntax, immutable action pins, embedded assets, and shell
  syntax passed.

At capture, exact-source [CI](https://github.com/cyberducttape/StePanel/actions/runs/39191293219)
and [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39191293280)
were queued; standard
[installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39191293271)
and [quota-enabled installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39191293307)
were pending without assigned jobs. None are counted as passes.

## What this does not certify

The repository gate does not prove physical-host power-loss recovery, all
real-host ENOSPC boundaries, installed-host behavior of aggregate Git staging
quotas (including XFS), external notification delivery and outage recovery,
production capacity, untested runtime combinations, or an independent
security assessment. The production decision remains **NO-GO**.
