# Repository production gate — 2026-10-30

**Source:** `24f8bee2524ef8dd0ee2cf7643189b2d45b78eba`
**Run date:** 2026-10-30 (UTC)
**Environment:** Go 1.26.10, linux/amd64, Ubuntu kernel 7.0.0-38-generic, AMD Ryzen 7 7735U

The full `make production-gate` command completed successfully on this source.

## Results

| Gate | Result | Evidence |
| --- | --- | --- |
| Formatting and `go vet ./...` | PASS | Production-gate run |
| Full Go tests with atomic coverage | PASS | Root package 54.5%; total 57.6% |
| Per-package coverage floors | PASS | All 13 configured package thresholds met; root broker 67.5%, importer 70.0%, jobs 100.0% |
| Full Go race suite | PASS | Root package 409.461s; all other packages passed or had no tests |
| Recovery drills | PASS | Partial SQL import cleanup 2s; interrupted transaction recovery 2s; configuration rollback 3s; pending runtime reconciliation 2s |
| Control-plane benchmarks | PASS | 500-site/10-mutator benchmark 0.90ms/op; process contention 0.88ms/op; 50 backup/restore jobs 48.3ms/batch |
| Mixed HTTP load | PASS | 200 sites, 180 reads, 20 backup jobs, 4 readers; average 4.5ms, p95 28.6ms, max 35.5ms |
| Documentation and smoke contracts | PASS | 95 documents checked; recovery input and bounded smoke checks passed |
| Quota contracts, action pins, and tests | PASS | ext4/XFS contract fixtures, action-pin positive/negative tests passed |
| Grafana, assets, and shell syntax | PASS | 12 dashboard panels/11 metrics; embedded assets and shell scripts validated |

The control-plane benchmarks are single-iteration local measurements. The
mixed-load run is synthetic and checks a bounded local workload only. These
results do not establish production capacity or SLOs.

## Scope limits

The repository-level run does not demonstrate real-host ENOSPC behavior,
physical power-loss recovery, installed-host upgrade or rollback, supported
distribution compatibility, repeated recovery cohorts, multi-day soak, live
notification delivery, or application restoration on a clean host. It does
not close those P0 release gates. Production remains **NO-GO** pending the
evidence listed in [`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md).
