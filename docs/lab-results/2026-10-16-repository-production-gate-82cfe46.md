# Repository production gate — 2026-10-16

| Field | Value |
|---|---|
| Tested source commit | `82cfe464757829dbbc15b92e92bbf91051df3811` |
| Worktree | Clean at gate start |
| Command | `bash scripts/production-gate.sh` |
| Result | PASS |
| Host | Linux amd64; AMD Ryzen 7 7735U; Go 1.26.7 |
| Completed | 2026-10-16 00:11 UTC |

## Results

- `gofmt`, `go vet ./...`, the full Go suite, and all configured coverage
  thresholds passed. Overall statement coverage was 56.2%; root package
  coverage was 52.6%, `internal/rootbroker` 72.2%, and `internal/importer`
  67.2%.
- The serialized full race suite passed; the root package completed in
  439.449 seconds.
- Repository recovery drills passed: partial SQL import cleanup, interrupted
  transaction recovery, configuration rollback, and pending runtime
  reconciliation (2 seconds each).
- One-iteration control-plane benchmarks passed: site mutations at 10/100/500
  sites with 10 mutators took 0.795/0.813/0.932 ms/op; panel/worker contention
  0.820 ms/op; process contention 0.777 ms/op; 50 backup/restore jobs
  61.622 ms/batch; audit-heavy mutations 1.030 ms/op.
- Synthetic mixed-load smoke passed: 200 sites, 180 requests, four readers,
  and 20 backup jobs; average 3.6 ms, p95 10.675 ms, maximum 32.1 ms.
- Documentation links and references (88 documents), GitHub Action SHA
  pinning, embedded assets, and shell syntax checks passed.

## Scope and limits

This establishes a repository-level gate pass on the tested source commit.
The benchmarks are one-iteration microbenchmarks, and the HTTP run is a short
synthetic smoke; neither certifies production capacity or customer-traffic
SLOs. Repository recovery drills do not establish installed-host recovery,
physical power-loss behavior, publication/import/database-restore ENOSPC
behavior, or provider outage handling. The separate real-host recovery matrix
was interrupted and is not counted as a pass. These results do not close the
external gates in [V1_PRODUCTION_GATES.md](../V1_PRODUCTION_GATES.md).
