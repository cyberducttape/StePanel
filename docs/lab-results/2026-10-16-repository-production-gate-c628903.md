# Repository production gate — 2026-10-16

## Source and environment

| Item | Result |
| --- | --- |
| Source commit | `c6289039b3e1b35fd2561325bc5e68a5f99286f0` |
| Source tree | Clean at gate start; generated binary is ignored |
| Command | `bash scripts/production-gate.sh` |
| Go toolchain | `go1.26.7 linux/amd64` |
| CPU | AMD Ryzen 7 7735U with Radeon Graphics |
| Overall result | PASS |

## Results

- `go vet ./...`: passed.
- Full Go coverage suite and configured floors: passed; overall coverage was
  56.2%, exceeding the 50% repository floor. Root-package coverage was 52.6%;
  root broker 72.2%, importer 67.2%, helper 92.5%, and durable jobs 100%.
- `go test -p 1 -race -timeout 10m ./...`: passed; root package completed in
  448.918 seconds and all subpackages passed.
- Repository recovery drills: passed partial SQL import cleanup, interrupted
  transaction recovery, configuration rollback, and pending runtime
  reconciliation. Each grouped scenario completed in two seconds; see the
  [drill report](2026-10-16-recovery-drills.md).
- One-iteration control-plane benchmarks passed: 10-site mutations 860,176
  ns/op; 100-site mutations 674,815 ns/op; 500-site mutations 769,312 ns/op;
  panel/worker contention 897,821 ns/op; process contention 689,552 ns/op with
  a 5,000 ms SQLite busy timeout; 50 backup/restore jobs 46,029,977 ns/batch;
  and audit-heavy mutations 886,856 ns/op. These are smoke benchmarks, not
  statistically certified capacity limits.
- Synthetic mixed HTTP load passed with 200 sites, 180 requests, four readers,
  and 20 backup jobs: average latency 3.3 ms, p95 10.186 ms, maximum 28.591 ms.
  This does not represent a production workload or certify capacity.
- Documentation links and source references (88 documents), action SHA pins,
  embedded assets, and shell syntax all passed.

## Scope limits

This is repository-level synthetic evidence. It does not prove installed-host
behavior, physical power-loss recovery, database-server outage recovery, every
ENOSPC publication/import/database-restore boundary, live SMTP/webhook delivery
and outage recovery, supported-distro breadth, or a production capacity
envelope. The repeated installed-host cohort and these release gates remain
open; this result is not production approval.
