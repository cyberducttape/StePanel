# Repository production gate — 2026-10-17

**Commit:** `862d8015f3e7c3a85d9aa7525f2e17a04d147d97`  
**Go:** `go1.26.7 linux/amd64`  
**Command:** `bash scripts/production-gate.sh`  
**Result:** PASS

The exact checked-out commit passed:

- `go vet ./...` and `go test -p 1 -timeout 10m ./...`.
- Aggregate statement coverage of 56.2% against the 50% floor, and every
  configured per-package coverage threshold.
- `go test -p 1 -race -timeout 10m ./...`; the root package completed in
  518.825 seconds.
- Repository recovery drills.
- Control-plane benchmarks: site mutation at 10/100/500 sites was
  0.935/0.732/0.840 ms per operation; 50 backup/restore jobs were
  49.600 ms per batch; process contention was 0.769 ms in this one-shot run.
- Synthetic mixed HTTP load: 200 sites, 180 requests, four readers, and 20
  backup jobs; p95 13.976 ms, maximum 37.8 ms.
- Documentation links and source references (88 documents), Grafana dashboard,
  action SHA pins, embedded asset validation, and shell syntax checks.

This is repository-level evidence, not an installed-host or capacity
certification. It does not close physical host power loss, the remaining real
ENOSPC boundaries, the 20–50-repeat installed-host matrix, live notification
outage/retry, production capacity, runtime support, upgrade policy, or
independent security review. Release approval remains **NO-GO**.
