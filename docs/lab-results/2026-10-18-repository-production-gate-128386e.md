# Repository production gate — 2026-10-18 (`128386e`)

Exact source: `128386e8beb40dcd965ba8c662b6da97237be7b6`.

Command: `bash scripts/production-gate.sh`.

## Result

**PASS** for repository-level checks.

- `go vet ./...` passed.
- Full serialized Go tests passed. Aggregate coverage was 56.9%; every
  declared package floor passed, including root package 53.5% and root broker
  72.6%.
- Full race suite passed; the root package completed in 470.765 seconds.
- Repository recovery drills passed: partial SQL import cleanup (21s),
  interrupted transaction recovery (1s), configuration rollback (1s), and
  pending runtime reconciliation (2s).
- Control-plane benchmarks passed. The 500-site/10-mutator sample was
  838,947 ns/op; 50 backup/restore jobs completed in 48,219,115 ns.
- Mixed HTTP load passed: 200 sites, 180 requests, four readers, and 20 backup
  jobs; average 3.7 ms, p95 13.424 ms, maximum 35.4 ms.
- Documentation links and code references, workflow bounds, Grafana dashboard,
  quota fixtures, pinned actions, embedded assets, and shell syntax checks all
  passed.
- `TestGitCloneFileSizeLimitIsEnforcedByKernel` passed. It proves only the
  per-file kernel limit, not an aggregate staging quota.

## Limits

This is repository-level evidence, not host certification. It does not prove
physical power-loss recovery, all real-host ENOSPC boundaries, repeated
installed-host recovery, or the supported capacity envelope. Aggregate Git
staging-space enforcement remains an explicit P0 release blocker. Production
remains **NO-GO**.

The gate ran against the exact committed source listed above. Hosted CI,
CodeQL, and installed-host smoke workflows for this commit were still queued or
pending when queried; no result is inferred from those states.
