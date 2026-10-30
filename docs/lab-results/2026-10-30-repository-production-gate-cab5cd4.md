# Repository production gate — 2026-10-30

**Source:** `cab5cd41b861cd90681a04752ad260cfe8ed1419`
**Host:** Linux x86_64, AMD Ryzen 7 7735U
**Command:** `make production-gate`
**Result:** PASS

## Results

- `go vet ./...` passed.
- Full Go suite with atomic coverage passed: **57.9%** overall; all configured
  package thresholds passed, including root broker at 67.4% (65% required).
- Full race suite passed. The root package took 411.314 seconds; root broker
  took 4.065 seconds.
- Repository recovery drills passed: partial SQL import cleanup (31 s),
  interrupted transaction recovery (2 s), configuration rollback (1 s), and
  pending runtime reconciliation (1 s). The generated details are
  `/tmp/stepanel-recovery-drills.md` on the test host.
- Control-plane one-iteration benchmarks passed. The 50-backup/restore-job
  batch measured 51.3 ms; 500-site/10-mutator control-plane mutations measured
  1.20 ms/op in this synthetic benchmark.
- Mixed HTTP load passed with 200 sites, 180 requests, four readers, and 20
  backup jobs: average 4.5 ms, p95 25.1 ms, maximum 34.9 ms. `/metrics` maximum
  latency was 25.2 ms in this run.
- Documentation links/references, bounded smoke-workflow contracts, Grafana
  dashboard validation, quota-state contract fixtures, action-SHA pin checks,
  asset validation, and shell syntax checks passed.

## Limits

This is repository-level evidence for the exact source above. The quota check
uses mocked filesystem command responses to verify that disabled enforcement
and unsupported filesystems are rejected; it does not certify quota enforcement
on a mounted ext4 or XFS filesystem. The mixed-load run is synthetic and does
not include real PHP/database traffic or a sustained soak.

This result does not close installed-host ENOSPC, physical power loss,
20–50-repeat recovery, clean-host application restoration, live alert delivery,
supported-platform, or capacity-certification gates. The production verdict
remains **NO-GO** until those authoritative gates have their required evidence.
