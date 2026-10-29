# Repository production gate — 2026-10-29

**Source:** [`0feaa06e2197fe4544cc0e171a472f240ca7648e`](https://github.com/cyberducttape/StePanel/commit/0feaa06e2197fe4544cc0e171a472f240ca7648e)
**Run date:** 2026-10-29 (UTC)
**Environment:** Go 1.26.10, linux/amd64, Ubuntu kernel 7.0.0-38-generic, AMD Ryzen 7 7735U

The checked-in non-Markdown source tree matches the tested source. The full
`bash scripts/production-gate.sh` command completed successfully.

## Results

| Gate | Result | Evidence |
| --- | --- | --- |
| Formatting and `go vet ./...` | PASS | Production-gate run |
| Full Go tests with atomic coverage | PASS | Root package 54.5%; total 57.6% |
| Per-package coverage floors | PASS | All 13 configured package thresholds met; root broker 68.2%, importer 70.0%, jobs 100.0% |
| Full Go race suite | PASS | Root package 404.002s; all other packages passed or had no tests |
| Recovery drills | PASS | Partial SQL import cleanup 3s; interrupted transaction recovery 2s; configuration rollback 2s; pending runtime reconciliation 1s |
| Control-plane benchmarks | PASS | 500-site/10-mutator benchmark 0.90ms/op; 50 backup/restore jobs 48.3ms/batch; process contention 1.49ms/op |
| Mixed HTTP load | PASS | 200 sites, 180 requests, 4 readers, 20 backup jobs; average 4.8ms, p95 25.1ms, max 41.8ms |
| Documentation links and references | PASS | 93 documents checked |
| Smoke bounds, Grafana dashboard, quota fixtures/state contracts | PASS | Dashboard 12 panels/11 metrics; ext4/XFS contract fixture checks passed |
| GitHub Action pin checks and tests | PASS | Production workflow pins and positive/negative checker cases |
| Embedded asset validation and shell syntax | PASS | Operator page, customer page, partials and static assets verified |

The control-plane benchmarks are single-iteration local measurements, and the
mixed-load run is synthetic. They do not establish a supported capacity
envelope or production SLO.

## Scope limits

This repository-level run does not demonstrate real-host ENOSPC behavior,
physical power-loss recovery, installed-host upgrade or rollback, distro and
web-server compatibility, multi-day soak, live notification delivery, or
application restoration on a clean host. It does not close those P0 release
gates. Production remains **NO-GO** pending the evidence listed in
[`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md).
