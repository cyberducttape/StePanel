# Repository production gate — 2026-10-16

| Field | Value |
|---|---|
| Tested checkout | `3927b386838586e2102fd18c50b15035ead915da` |
| Worktree before gate | Clean |
| Command | `bash scripts/production-gate.sh` |
| Result | PASS |
| Host/toolchain | Linux amd64; Ryzen 7 7735U; Go 1.26.7 |
| Completed | 2026-10-16 02:56 UTC |

## Results

- `go vet ./...`, all Go tests, and the full race suite passed. Overall statement
  coverage was 56.2%, and every configured package floor passed: root package
  52.6%, root broker 72.2%, importer 67.2%. The root package test suite took
  62.881 seconds and its race suite 429.444 seconds.
- Repository recovery drills passed: partial SQL import cleanup (21s),
  interrupted transaction recovery (1s), configuration rollback (1s), and
  pending runtime reconciliation (2s).
- One-iteration control-plane benchmarks passed: 10/100/500-site mutation
  cases at 0.884/0.683/0.767 ms/op; panel/worker contention 0.983 ms/op;
  process contention 0.699 ms/op; 50 backup/restore jobs 46.026 ms/batch;
  audit-heavy mutations 0.940 ms/op.
- Synthetic mixed HTTP load passed with 200 sites, 180 requests, four readers,
  and 20 backup jobs: average 3.4 ms, p95 11.421 ms, maximum 29.8 ms.
- Documentation links/references (88 documents), pinned GitHub Actions,
  embedded assets, and shell syntax validation passed.

## Scope

This is repository-level evidence, not installed-host certification. The short
synthetic benchmarks do not establish a supported capacity envelope. Physical
host power loss, repeated installed-host recovery, ENOSPC during publication,
import, or database restore, live notification outage/retry, and the remaining
runtime matrix remain open. Passing this gate does not authorize production
deployment.
