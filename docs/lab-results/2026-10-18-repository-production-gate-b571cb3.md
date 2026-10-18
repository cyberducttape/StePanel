# Repository production gate — 2026-10-18 (`b571cb3`)

Exact source: `b571cb32d647615536eb607fb5b0245e35f40e79`.

Command: `bash scripts/production-gate.sh`.

## Result

**PASS** for repository-level checks.

- `go vet ./...` passed.
- Full serialized Go tests passed. Aggregate coverage was 56.9%; all declared
  package floors passed, including root package 53.5% and root broker 72.6%.
- Full race suite passed; the root package completed in 476.115 seconds.
- Repository recovery drills passed: partial SQL import cleanup (25s),
  interrupted transaction recovery, configuration rollback, and pending runtime
  reconciliation.
- Control-plane benchmarks passed. The 500-site/10-mutator sample was
  852,772 ns/op; 50 backup/restore jobs were processed in 50,408,043 ns.
- Mixed HTTP load passed: 200 sites, 180 requests, four readers, and 20 backup
  jobs; average 4.1 ms, p95 11.092 ms, maximum 39.1 ms.
- Documentation links and code references, workflow bounds, Grafana dashboard,
  quota fixtures, pinned actions, embedded assets, and shell syntax checks all
  passed.

## Limits

This is repository-level evidence, not host certification. The tests do not
prove physical power-loss recovery, all real-host ENOSPC boundaries, repeated
installed-host recovery, or the supported capacity envelope. Git cloning now
has an enforced per-file `RLIMIT_FSIZE`, but aggregate staging-space enforcement
remains an explicit P0 release blocker. Production remains **NO-GO**.

The gate was run on the exact source tree represented by `b571cb3` before its
commit was created; the recorded source hash identifies that tree. Subsequent
documentation-only edits are not represented by this run.
