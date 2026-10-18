# Repository production gate — 2026-10-18

**Checkout:** `e50dab701423e84fd126a5edcbe55d3b17a34139`  
**Toolchain:** Go 1.26.9, linux/amd64  
**Result:** `bash scripts/production-gate.sh` passed.

## Checks and measurements

- `go vet ./...`, the full test suite, and all configured coverage floors
  passed. Aggregate statement coverage was 56.9% against the 50% floor; every
  per-package target passed. The root package reported 53.3%, root broker
  72.6%, importer 67.3%, and internal/jobs 100%.
- `go test -p 1 -race -timeout 10m ./...` passed. The root package took
  502.760 seconds; all other packages passed.
- Repository recovery drills passed: partial SQL import cleanup (23 s),
  interrupted transaction recovery (2 s), configuration rollback (1 s), and
  pending runtime reconciliation (1 s). See
  [`2026-10-18-recovery-drills-e50dab7.md`](./2026-10-18-recovery-drills-e50dab7.md).
- Control-plane one-iteration benchmarks: site mutations at 10/100/500 sites
  were 0.777/0.962/0.800 ms per operation; panel/worker contention 0.750 ms;
  separate-process contention 0.718 ms; a 50-job backup/restore batch 50.874
  ms; audit-heavy mutations 1.048 ms.
- Synthetic mixed HTTP load passed: 200 sites, 180 requests, four readers, 20
  backup jobs; average 3.9 ms, p95 12.673 ms, maximum 35.2 ms.
- Documentation links and references, Grafana dashboard, quota fixture
  capacity, immutable action pins, embedded assets, and shell syntax passed.

At the time of this run, exact-head hosted CI and CodeQL were queued/pending,
as were the standard and quota-enabled installation smokes ([CI](https://github.com/cyberducttape/StePanel/actions/runs/39164231584),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39164231636),
[install](https://github.com/cyberducttape/StePanel/actions/runs/39164231724),
[quota install](https://github.com/cyberducttape/StePanel/actions/runs/39164231589)).
They are not counted as passing evidence here. The installed HTTPS Caddy
security smoke has therefore not yet been verified on a host.

## What this does not certify

Repository tests and synthetic benchmarks do not prove physical-host power
loss recovery, all real-host ENOSPC boundaries, live notification outage and
retry, installed-host upgrade/rollback, production capacity, broader runtime
support, or independent security review. The production decision remains
**NO-GO**.
