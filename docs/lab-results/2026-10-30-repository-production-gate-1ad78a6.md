# Repository production gate — 2026-10-30

**Source:** `1ad78a62593ef0ee25ef19b452d366a70ba583de`

**Run date:** 2026-10-30 (UTC)

**Environment:** Go 1.26.10, linux/amd64, Ubuntu kernel 7.0.0-38-generic, AMD Ryzen 7 7735U

`make production-gate` completed successfully on this exact source.

## Results

| Gate | Result | Evidence |
| --- | --- | --- |
| Formatting and `go vet ./...` | PASS | Production-gate run |
| Full Go tests with atomic coverage | PASS | Root package 55.0%; total 57.9% |
| Per-package coverage floors | PASS | All configured thresholds met; root 54.7%, root broker 67.5%, session 91.2%, auth 93.3% |
| Full Go race suite | PASS | Root package 411.589s; all other packages passed or had no tests |
| Recovery drills | PASS | Partial SQL import cleanup 28s; interrupted transaction recovery 2s; configuration rollback 1s; pending runtime reconciliation 2s |
| Control-plane benchmarks | PASS | 500-site/10-mutator benchmark 0.943ms/op; process contention 0.917ms/op; 50 backup/restore jobs 49.5ms/batch |
| Mixed HTTP load | PASS | 200 sites, 180 reads, 20 backup jobs, 4 readers; average 4.5ms, p95 26.6ms, max 34.7ms |
| Documentation and smoke contracts | PASS | 96 documents checked; recovery-matrix input and bounded smoke checks passed |
| Quota contracts, action pins, and tests | PASS | Contract fixtures and action-pin positive/negative tests passed |
| Grafana, assets, and shell syntax | PASS | 12 dashboard panels/11 exported metrics; embedded assets and shell scripts validated |

Recovery-drill output was generated at `/tmp/stepanel-recovery-drills.md`;
its SHA-256 was `4ccb03fad035d49510ab1ed94b5bf7e308ac230718f46d1c495a7c2273914a2e`.
The quota contract test explicitly reported that project quota enforcement is
off on the local XFS mount, ext4 project quotas are not enabled, and Btrfs does
not support the tested quota mode. Passing the contract test validates its
checks, not actual quota enforcement on these filesystems.

The control-plane benchmarks are single-iteration local measurements. The
mixed-load run is synthetic and checks a bounded local workload only. These
results do not establish a production capacity envelope or SLOs.

## Scope limits

This repository-level run does not demonstrate real-host ENOSPC behavior,
physical power-loss recovery, installed-host upgrade or rollback, supported
distribution compatibility, repeated recovery cohorts, multi-day soak, live
notification delivery, or application restoration on a clean host. It does
not close those P0 release gates. Production remains **NO-GO** pending the
evidence listed in [`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md).
