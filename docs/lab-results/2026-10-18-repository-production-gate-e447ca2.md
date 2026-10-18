# Repository production gate — 2026-10-18

**Checkout:** `e447ca2a28e352aa23190bc2917699d86cee5fda`  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed.

## Checks and measurements

- `go vet ./...`, the full test suite, and every configured coverage floor
  passed. Aggregate statement coverage was 56.9% against the 50% floor;
  every per-package target passed. The root package reported 53.3%, root broker
  72.6%, importer 67.3%, and internal/jobs 100%.
- `go test -p 1 -race -timeout 10m ./...` passed. The root package took
  470.922 seconds; all other packages passed.
- Repository recovery drills passed: partial SQL import cleanup, interrupted
  transaction recovery, configuration rollback, and pending runtime
  reconciliation (2 seconds each). These use synthetic temporary data and are
  repository-level checks, not disposable-host drills.
- Control-plane one-iteration benchmarks: site mutations at 10/100/500 sites
  were 0.861/0.901/0.822 ms per operation; panel/worker contention 0.739 ms;
  separate-process contention 0.775 ms; a 50-job backup/restore batch 51.500
  ms; audit-heavy mutations 1.369 ms.
- Synthetic mixed HTTP load passed: 200 sites, 180 requests, four readers, and
  20 backup jobs; average 3.9 ms, p95 14.993 ms, maximum 38.0 ms.
- Documentation links and references (88 documents), Grafana dashboard, quota
  fixture capacity, immutable action pins, embedded assets, and shell syntax
  passed.

At capture, exact-head hosted CI and CodeQL were queued; standard and
quota-enabled installation smokes were pending without jobs
([CI](https://github.com/cyberducttape/StePanel/actions/runs/39165927053),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39165927052),
[install](https://github.com/cyberducttape/StePanel/actions/runs/39165927042),
[quota install](https://github.com/cyberducttape/StePanel/actions/runs/39165927031)).
These are not passing evidence. The separate 20-repeat installed-host cohort
on `633c013` remains incomplete.

## What this does not certify

Repository tests and synthetic benchmarks do not prove physical-host power
loss recovery, all real-host ENOSPC boundaries, installed-host WordPress
recovery across the supported matrix, notification outage and retry,
installed-host upgrade/rollback, production capacity, broader runtime support,
or independent security review. The production decision remains **NO-GO**.
