# Repository production gate — 2026-10-15

| Field | Value |
|---|---|
| Tested source commit | `3f1445f6fb4153f1a3999241fd83de36cfe8e6f0` |
| Worktree | Clean at gate start |
| Command | `bash scripts/production-gate.sh` |
| Result | PASS |
| Host | Linux amd64; AMD Ryzen 7 7735U; Go 1.26.7 |
| Completed | 2026-10-15 03:44 UTC |

## Results

- `gofmt`, `go vet ./...`, and the complete Go suite passed. Repository
  statement coverage was 56.1%; all configured package thresholds passed,
  including root package 52.6%, root broker 72.2%, and importer 65.9%.
- The complete serial race suite passed; the root package completed in
  449.455 seconds and `internal/sites` in 1.061 seconds.
- Repository recovery drills passed: partial SQL import cleanup (22s),
  interrupted transaction recovery (2s), configuration rollback (1s),
  and pending runtime reconciliation (1s).
- One-iteration control-plane benchmarks passed: site mutations at 10/100/500
  sites with 10 mutators took 0.979/0.821/0.822 ms/op; panel/worker contention
  0.816 ms/op; process contention 0.746 ms/op; 50 backup/restore jobs
  47.821 ms/batch; audit-heavy mutations 0.997 ms/op.
- Synthetic mixed-load smoke passed: 200 sites, 180 requests, four readers,
  and 20 backup jobs; average 3.5 ms, p95 11.949 ms, maximum 33.6 ms.
- Documentation links and references (88 documents), GitHub Action SHA
  pinning, embedded assets, and shell syntax checks passed.

## Scope and limits

This establishes a repository-level gate pass on the tested source commit. The
benchmarks are one-iteration microbenchmarks, and the HTTP run is a short
synthetic smoke; neither certifies production capacity or customer-traffic
SLOs. Repository recovery drills do not establish installed-host recovery,
physical power-loss behavior, publication/import/database-restore ENOSPC
behavior, or provider outage handling. The separate six-host repeated
recovery workflow was still running when this report was captured. These
results do not close the external gates in
[V1_PRODUCTION_GATES.md](../V1_PRODUCTION_GATES.md).
