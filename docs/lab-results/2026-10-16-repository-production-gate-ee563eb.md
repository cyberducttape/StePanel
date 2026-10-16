# Repository production gate — `ee563eb`

**Date:** 2026-10-16  
**Scope:** repository-level checks on the exact pushed source commit.  
**Result:** passed.

The run used `scripts/production-gate.sh` on Linux/amd64 with Go 1.26.7 and an
AMD Ryzen 7 7735U host. It completed:

- `go vet ./...`;
- `go test ./...` with coverage: 56.1% repository-wide, with every configured
  package threshold met (including 52.6% for the root package and 72.2% for
  `internal/rootbroker`);
- `go test -race ./...` (root package: 451.649 seconds; all packages passed);
- repository recovery drills: partial SQL import cleanup (26s), interrupted
  transaction recovery (1s), configuration rollback (1s), and pending runtime
  reconciliation (2s), all passed;
- one-iteration control-plane benchmarks, mixed HTTP load, documentation-link
  and reference checks, pinned GitHub Action checks, embedded asset validation,
  and Bash syntax checks.

The mixed-load check used 200 sites, 180 requests, four readers, and 20 backup
jobs. It passed with 3.5 ms average and 13.451 ms p95 request latency; the
slowest observed endpoint maximum was `/api/health` at 35.824 ms. These are
synthetic local measurements, not a sustained capacity certification or a
real PHP/database workload.

The repository recovery drills use synthetic temporary data. This run does
not establish installed-host behavior, physical power-loss recovery, all
ENOSPC boundaries, repeated 20–50 host recovery runs, SMTP/webhook outage
recovery, supported-platform certification, or a production capacity
envelope. Prometheus alert-rule tests were not run because `promtool` was not
installed in the environment.
