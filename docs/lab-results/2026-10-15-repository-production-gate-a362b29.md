# Current-head repository production gate — 2026-10-15

| Field | Value |
|---|---|
| Tested source commit | `a362b2931041333d6707500163c1bdd128339553` |
| Worktree | Clean before gate |
| Command | `scripts/production-gate.sh` |
| Result | PASS |
| Host/toolchain | Linux amd64; AMD Ryzen 7 7735U; Go 1.26.7 |
| Completed | 2026-10-15 05:58 UTC |

## Repository results

- `go vet ./...`, all Go tests, and repository-wide race tests passed.
  Statement coverage was 56.2%; every configured package floor passed,
  including root package 52.6%, root broker 72.2%, and importer 67.2%.
  The root package race suite took 455.548 seconds.
- The repository recovery drills passed: partial SQL import cleanup (23s),
  interrupted transaction recovery (1s), configuration rollback (2s), and
  pending runtime reconciliation (1s).
- One-iteration control-plane benchmarks passed: 10/100/500-site mutation
  cases at 0.712/0.858/0.785 ms/op; panel/worker contention 0.681 ms/op;
  process contention 0.733 ms/op; 50 backup/restore jobs 47.410 ms/batch;
  audit-heavy mutations 0.958 ms/op.
- Synthetic mixed HTTP load passed with 200 sites, 180 requests, four readers,
  and 20 backup jobs: average 3.2 ms, p95 11.299 ms, maximum 28.3 ms.
- Documentation links/references (88 documents), immutable action pins,
  embedded assets, and Bash syntax checks passed.

## Hosted checks on this source

- [CI run 38688623120](https://github.com/cyberducttape/StePanel/actions/runs/38688623120) passed, including UI E2E/accessibility, full tests, race and recovery checks, tmpfs ENOSPC, container checks, and deployment validation.
- [CodeQL run 38688623127](https://github.com/cyberducttape/StePanel/actions/runs/38688623127) passed.
- [N−1 upgrade smoke 38688628029](https://github.com/cyberducttape/StePanel/actions/runs/38688628029) passed: upgrade from v0.6.0 to the candidate and rollback after a deliberately broken candidate.
- Six-host installation smoke run 38688623197 and quota-enabled installation run 38688623248 were still pending when this report was written; neither is counted as a pass here.

## Scope and limitations

These results establish repository-level checks and one N−1 upgrade/rollback
workflow. The benchmarks and HTTP load are short synthetic measurements, not a
certified capacity envelope. The separate 20-repeat installed-host workflow
38670189826 was still running against source `1fb3879`, not this source commit;
it is not current-head certification. Physical host power loss, broader
publication/import/database-restore ENOSPC, repeated installed-host recovery,
live notification outage/retry, and the remaining distribution/runtime matrix
gates remain open. This report does not authorize production deployment.
