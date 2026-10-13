# Repository production gate — 2026-10-13

| Field | Value |
|---|---|
| Commit | `b29856f97572ab549055d5064462e05a860d4e53` |
| Worktree | Clean at gate start |
| Command | `make production-gate` |
| Result | PASS |
| Host | Linux `7.0.0-38-generic`, x86_64; AMD Ryzen 7 7735U; Go 1.26.7 |

## Results

- `go vet ./...`: passed.
- Complete Go suite with coverage: passed. Coverage ratchet reported 55.2%
  repository-wide; all configured package targets passed, including root broker
  72.0%, archive importer 65.9%, and root package 51.5%.
- Complete Go race suite: passed (root package 439.870 seconds).
- Repository recovery drills: passed (partial SQL import cleanup, interrupted
  transaction recovery, configuration rollback, and pending runtime
  reconciliation). The generated detail report was
  `/tmp/stepanel-recovery-drills.md` on the runner.
- Control-plane microbenchmarks (single iteration): 500-site/10-mutator
  synthetic site mutation 0.942 ms/op; panel/worker contention 0.743 ms/op;
  process contention 0.655 ms/op; 50 backup/restore jobs 46.1 ms/batch.
- Synthetic mixed-load smoke: 200 sites, 180 requests, four readers, and 20
  backup jobs passed; measured average 9.2 ms, p95 83.8 ms, maximum 105.3 ms.
- Documentation links and references (87 documents), action SHA pinning,
  embedded assets, and shell syntax checks: passed.

## Scope and limits

These results establish that the repository gate passed on this development
host. The microbenchmarks use one iteration and the mixed-load run is a short
synthetic smoke; neither certifies a hosting capacity envelope or customer
traffic SLO. Repository recovery drills do not exercise a real systemd host,
physical power loss, full-filesystem publication/database-restore paths,
or provider outages. They do not close the outstanding installed-host release
gates in [V1_PRODUCTION_GATES.md](../V1_PRODUCTION_GATES.md).
