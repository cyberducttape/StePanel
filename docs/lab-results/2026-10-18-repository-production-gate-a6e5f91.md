# Repository production gate — 2026-10-18

**Checkout:** `a6e5f912e742405237e591041a8375a3cbad733f`  
**Go application source:** unchanged from `633c013ce1dd2480928487c0fcf0b7f3aecf4b62`; installer and release-check changes are included  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed.

## Checks and measurements

- `go vet ./...` and the full test suite passed. Aggregate statement coverage
  was 56.9% against the 50% floor; every configured per-package floor passed.
  Root-package test coverage was 53.3%, root broker 72.6%, importer 67.3%, and
  internal/jobs 100%.
- `go test -race ./...` passed. The root package took 470.462 seconds; all
  other packages passed.
- Repository recovery drills passed: partial SQL import cleanup (24 s),
  interrupted transaction recovery (1 s), configuration rollback (1 s), and
  pending runtime reconciliation (2 s). Full results are in
  [`2026-10-18-recovery-drills-a6e5f91.md`](./2026-10-18-recovery-drills-a6e5f91.md).
- Control-plane one-iteration benchmarks: site mutations at 10/100/500 sites
  were 0.941/0.860/0.794 ms per operation; panel/worker contention 0.746 ms;
  separate-process contention 0.691 ms; a 50-job backup/restore batch 48.783
  ms; audit-heavy mutations 1.074 ms.
- Synthetic mixed HTTP load passed: 200 sites, 180 requests, four readers, 20
  backup jobs; average 4.0 ms, p95 13.599 ms, maximum 34.687 ms.
- Documentation links and references, Grafana dashboard, quota fixture
  capacity, immutable action pins, embedded assets, formatting, and shell
  syntax passed. The release metadata checker passed separately. ShellCheck
  passed for `install.sh` and `scripts/check-release.sh`.

At 2026-10-18 20:31 UTC, exact-head hosted CI and CodeQL were queued, and
standard/quota installation smokes were pending ([CI](https://github.com/cyberducttape/StePanel/actions/runs/39162892687),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39162892648),
[install](https://github.com/cyberducttape/StePanel/actions/runs/39162892657),
[quota install](https://github.com/cyberducttape/StePanel/actions/runs/39162892668)).
The 20-repeat hosted cohort was still running against `633c013`: three shards
had six active host jobs each and a fourth had started two jobs. No shard was
counted as passed. The N-1 upgrade run was queued.

## What this does not certify

Repository tests and synthetic benchmarks do not prove physical-host power
loss recovery, all real-host ENOSPC boundaries, live notification outage and
retry, installed-host upgrade/rollback, production capacity, broader runtime
support, or independent security review. The production decision remains
**NO-GO**.
