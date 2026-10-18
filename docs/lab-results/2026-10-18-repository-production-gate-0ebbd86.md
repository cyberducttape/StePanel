# Repository production gate — 2026-10-18

**Checkout:** `0ebbd86a9c56b37081b132bc39fc10355f4bb9a6`  
**Application source:** `633c013ce1dd2480928487c0fcf0b7f3aecf4b62` (the intervening commit changed documentation only)  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed.

## Checks and results

- `go vet ./...` passed.
- Full tests passed. Aggregate statement coverage was 56.9% against the 50%
  floor; every configured package floor passed. Root-package coverage was
  53.3% from `go test` and 53.5% from the target checker; root broker 72.6%,
  archive importer 67.3%, and internal/jobs 100%.
- `go test -race ./...` passed. The root package took 475.085 seconds; every
  other package passed.
- Repository recovery drills passed: partial SQL import cleanup (140 s),
  interrupted transaction recovery (2 s), configuration rollback (1 s), and
  pending runtime reconciliation (1 s). Full output is
  [`2026-10-18-recovery-drills.md`](./2026-10-18-recovery-drills.md).
- Control-plane benchmarks (one iteration): site mutations at 10/100/500
  sites were 0.968/0.757/0.796 ms per operation; panel/worker contention was
  0.872 ms; separate-process contention was 0.743 ms; a 50-job backup/restore
  batch was 52.049 ms; audit-heavy mutations were 1.061 ms.
- Synthetic mixed HTTP load passed with 200 sites, 180 requests, four readers,
  and 20 backup jobs: average 3.9 ms, p95 14.717 ms, maximum 38.1 ms.
- Documentation links and source references, dashboard metrics, quota fixture
  capacity, immutable action pins, embedded assets, and shell syntax passed.

Exact-source hosted CI and CodeQL also passed on the docs-only checkout
[`0ebbd86`](https://github.com/cyberducttape/StePanel/commit/0ebbd86a9c56b37081b132bc39fc10355f4bb9a6)
([CI](https://github.com/cyberducttape/StePanel/actions/runs/39158880734),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39158880689)).
The same application source `633c013` passed exact-head six-host install and
quota-enabled install smokes; their evidence is in the
[hosted validation report](./2026-10-18-hosted-validation-633c013.md).

At 2026-10-18 19:47 UTC, the exact-application-source 20-repeat installed-host
cohort had three active two-iteration shards and seven queued. Do not count a
shard until all six OS/web-server jobs pass. The older `a27becc` cohort had
eight passing and two failing shards, so only 16 repetitions per host
combination passed on that older application source.

## What this does not certify

These repository tests and synthetic benchmarks do not prove physical-host
power-loss recovery, all real-host ENOSPC boundaries, service recovery after a
broker dies during an in-flight mutation, external notification delivery and
outage recovery, production capacity, untested runtime combinations, or an
independent security assessment. The production decision remains **NO-GO**.
