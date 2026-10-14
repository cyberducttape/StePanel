# Repository production gate — 2026-10-14, installer-lock hardening

| Field | Value |
|---|---|
| Tested source commit | `26ec161fa7cecc7053f2456b72241ebc16bf4fef` |
| Worktree | Clean at gate start |
| Command | `bash scripts/production-gate.sh` |
| Result | PASS |
| Host | Linux x86_64, kernel 7.0.0-38-generic; AMD Ryzen 7 7735U; Go 1.26.7 |
| Completed | 2026-10-14 09:31 UTC |

## Results

- `gofmt` and `go vet ./...`: passed.
- Complete Go suite with atomic coverage: passed; repository coverage 56.0%.
  Every configured package threshold passed, including root package 52.5%,
  root broker 72.0%, and archive importer 65.9%.
- Complete serial race suite: passed; root package 447.457 seconds and
  `internal/sites` 1.075 seconds.
- Repository recovery drills: passed. The detailed output was written to
  `/tmp/stepanel-recovery-drills.md` on the test host.
- Control-plane microbenchmarks (one iteration): 500-site/10-mutator site
  mutation 0.753 ms/op; panel/worker contention 0.679 ms/op; process
  contention 0.682 ms/op; 50 backup/restore jobs 49.634 ms/batch.
- Synthetic mixed-load smoke passed: 200 sites, 180 requests, four readers,
  and 20 backup jobs; average 3.5 ms, p95 10.495 ms, maximum 28.6 ms.
- Documentation links and references (88 documents), GitHub Action SHA
  pinning, embedded assets, and shell syntax checks passed.

## Scope and limits

This proves that the repository-level gate passed on this development host at
the tested source commit. The benchmarks use one iteration and the HTTP load
run is a short synthetic smoke; neither certifies capacity or customer-traffic
SLOs. Repository recovery drills do not establish installed-host recovery,
physical power-loss behavior, publication/import/database-restore ENOSPC
behavior, or provider outage handling. These results do not close the external
gates in [V1_PRODUCTION_GATES.md](../V1_PRODUCTION_GATES.md).
