# Repository production gate — 2026-10-14, retention hardening

| Field | Value |
|---|---|
| Tested source commit | `db7cd6d049330dd86eecc7dc1079741f2fe91ad5` |
| Worktree | Clean at gate start |
| Command | `make production-gate` |
| Result | PASS |
| Host | Linux x86_64; AMD Ryzen 7 7735U; Go 1.26.7 |

## Results

- Formatting and `go vet ./...`: passed.
- Complete Go suite with atomic coverage: passed; repository coverage 55.8%.
  Every package target passed, including root package 52.3%, root broker 72.0%,
  and archive importer 65.9%.
- Complete serial race suite: passed; root package 446.533 seconds and
  `internal/sites` 1.092 seconds.
- Repository recovery drills: passed (partial SQL import cleanup, interrupted
  transaction recovery, configuration rollback, and pending runtime
  reconciliation). Detailed output: `/tmp/stepanel-recovery-drills.md` on the
  runner.
- Control-plane microbenchmarks (single iteration): 500-site/10-mutator
  synthetic site mutation 1.063 ms/op; panel/worker contention 0.961 ms/op;
  process contention 0.699 ms/op; 50 backup/restore jobs 48.0 ms/batch.
- Synthetic mixed-load smoke passed: 200 sites, 180 requests, four readers,
  and 20 backup jobs; average 9.7 ms, p95 90.6 ms, maximum 109.9 ms.
- Documentation links and references (88 documents), action SHA pinning,
  embedded assets, and shell syntax checks passed.

## Scope and limits

These results establish that repository checks passed on this development
host. The benchmarks use one iteration and the mixed-load run is a short
synthetic smoke; they do not certify a hosting capacity envelope or
customer-traffic SLO. Repository recovery drills do not exercise a real
systemd host, physical power loss, publication/import/database-restore
ENOSPC, or provider outages. They do not close installed-host release gates
in [V1_PRODUCTION_GATES.md](../V1_PRODUCTION_GATES.md).
