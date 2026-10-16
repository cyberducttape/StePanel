# Repository production gate — 2026-10-16

## Source and environment

| Item | Result |
| --- | --- |
| Source commit | `1e38dfc1d201a244ef07ffb2bfd01ef03f450969` |
| Source tree | Clean at gate start; generated binary is ignored |
| Command | `bash scripts/production-gate.sh` |
| Go toolchain | `go1.26.7 linux/amd64` |
| CPU | AMD Ryzen 7 7735U with Radeon Graphics |
| Overall result | PASS |

## Results

- `go vet ./...`: passed.
- `go test -p 1 -timeout 10m ./... -coverprofile=... -covermode=atomic`:
  passed; overall coverage 56.2%, exceeding the 50% floor. All configured
  package coverage floors passed, including root broker 72.2%, importer 67.2%,
  helper 92.5%, and durable jobs 100%.
- `go test -p 1 -race -timeout 10m ./...`: passed; root package completed in
  439.671 seconds and all subpackages passed.
- Repository recovery drills: passed, including partial SQL import cleanup,
  interrupted transaction recovery, configuration rollback, and pending runtime
  reconciliation. Detailed timings are in the
  [recovery drill report](2026-10-16-recovery-drills.md).
- Control-plane one-iteration benchmarks passed: 10-site mutations 764,353
  ns/op; 100-site mutations 738,791 ns/op; 500-site mutations 801,509 ns/op;
  panel/worker contention 772,246 ns/op; process contention 715,673 ns/op with
  SQLite busy timeout 5,000 ms; 50 backup/restore jobs 45,981,324 ns/batch; and
  audit-heavy mutations 877,847 ns/op. These are smoke benchmarks, not
  statistically certified limits.
- Mixed HTTP load passed with 200 synthetic sites, 180 total reader/writer
  requests, four readers, and 20 completed backup jobs: average latency 3.6 ms,
  p95 10.914 ms, maximum 30.3 ms. This is a regression workload, not a
  representative production-capacity certification.
- Documentation links and source references (88 documents), third-party action
  SHA pins, embedded assets, and shell syntax all passed.

## Scope limits

This repository-level run used synthetic local data. It does not prove installed
host behavior, database-server failure recovery, physical power-loss recovery,
all ENOSPC publication/import/database-restore boundaries, live SMTP/webhook
delivery and outage recovery, supported-distro breadth, or a production capacity
envelope. The installed-host 20-repeat cohort and these release gates remain
open; this result is not production approval.
