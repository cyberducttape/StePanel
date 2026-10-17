# Repository production gate — 2026-10-17

**Commit:** `bed56d81b9b56bfd102d48c5daca453ad66ab1aa`  
**Go:** `go1.26.7 linux/amd64`  
**Command:** `bash scripts/production-gate.sh`  
**Result:** PASS

The exact checked-out commit passed:

- `go vet ./...` and `go test -p 1 -timeout 10m ./...`.
- Aggregate statement coverage of 56.2% against the 50% floor, plus all
  configured per-package thresholds.
- `go test -p 1 -race -timeout 10m ./...`; root package completed in
  459.084 seconds.
- Repository recovery drills.
- Control-plane one-shot benchmarks: site mutation at 10/100/500 sites was
  0.897/0.749/0.804 ms per operation; 50 backup/restore jobs were
  49.689 ms per batch; process contention was 0.725 ms.
- Synthetic mixed HTTP load: 200 sites, 180 requests, four readers, and 20
  backup jobs; p95 14.415 ms, maximum 36.0 ms.
- Documentation links/source references (88 documents), Grafana dashboard,
  quota-fixture capacity invariant, action SHA pins, embedded assets, and
  shell syntax checks.

This is repository-level evidence, not installed-host or capacity
certification. It does not close physical host power loss, remaining real
ENOSPC boundaries, the 20–50-repeat installed-host matrix, live notification
outage/retry, supported-runtime certification, upgrade policy, or independent
security review. Release approval remains **NO-GO**.
