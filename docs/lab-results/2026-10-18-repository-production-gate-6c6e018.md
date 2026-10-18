# Repository production gate — 2026-10-18

**Checkout:** `6c6e018adcce396344525d0735e88942e64679e9`  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed.

## Checks and results

- `go vet ./...` passed.
- Full tests passed. Aggregate statement coverage was 56.8% against the 50%
  floor; every configured package floor passed. Root-package coverage was
  53.4%, root broker 71.2%, archive importer 67.3%, and internal/jobs 100%.
- `go test -p 1 -race -timeout 10m ./...` passed. The root package took
  475.424 seconds; every other package passed.
- Repository recovery drills passed: partial SQL import cleanup (24 s),
  interrupted transaction recovery (1 s), configuration rollback (2 s), and
  pending runtime reconciliation (1 s). Detailed scope is in
  [`2026-10-18-recovery-drills.md`](./2026-10-18-recovery-drills.md).
- Control-plane benchmarks (one iteration): site mutations at 10/100/500
  sites were 0.937/1.072/0.767 ms per operation; panel/worker contention was
  0.756 ms; separate-process contention was 0.742 ms; a 50-job backup/restore
  batch was 48.307 ms; audit-heavy mutations were 1.071 ms.
- Synthetic mixed HTTP load passed with 200 sites, 180 requests, four readers,
  and 20 backup jobs: average 4.2 ms, p95 14.192 ms, maximum 35.9 ms.
- Documentation links and source references, dashboard metrics, quota fixture
  capacity, immutable action pins, embedded assets, and shell syntax passed.

At capture, exact-head [CI](https://github.com/cyberducttape/StePanel/actions/runs/39175837964)
and [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39175837946)
were queued. Standard [installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39175837953)
and [quota-enabled installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39175837969)
were pending without jobs. These results are not counted as passes.

## What this does not certify

The repository gate does not prove physical-host power-loss recovery, all
real-host ENOSPC boundaries, installed-host behavior of aggregate Git staging
quotas (including XFS), external notification delivery and outage recovery,
production capacity, untested runtime combinations, or an independent
security assessment. The production decision remains **NO-GO**.
