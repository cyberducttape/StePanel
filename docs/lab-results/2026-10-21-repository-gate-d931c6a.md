# Repository production-gate results — `d931c6a`

Source commit: `d931c6aa9f50194ec8430ac7262853b7900eb1e6`  
Run date: 2026-10-21  
Toolchain: Go 1.26.10  
Host: Linux 7.0.0-38-generic x86_64, AMD Ryzen 7 7735U

## Results

The repository production-gate commands passed on the `d931c6a` source tree.
The first `scripts/production-gate.sh` invocation stopped at the documentation
reference check because `CLAUDE.md` still named a removed staging API. That
reference was corrected in the working tree; the documentation
reference check and every remaining gate command were then rerun successfully.
The report is transparent about this split run rather than claiming one
uninterrupted script invocation exited successfully.

- `go vet ./...`: passed.
- Full Go tests with coverage: passed; aggregate coverage 57.2%, and every
  configured package threshold passed.
- Full `go test -race ./...`: passed; root package 480.659 seconds, with all
  remaining packages passing.
- Repository recovery drills: all four scenarios passed: partial SQL import
  cleanup (141s), interrupted transaction recovery (1s), configuration rollback
  (1s), and pending runtime reconciliation (2s). The generated report is
  [2026-10-21-recovery-drills.md](./2026-10-21-recovery-drills.md).
- Control-plane benchmarks: passed. The 50-job batch completed in 52.0 ms;
  measured site-mutation cases (10/100/500 sites) were 0.77–0.87 ms per batch
  operation under this local synthetic benchmark.
- Mixed HTTP workload: passed with 200 sites, 180 requests, 4 readers, and 20
  concurrent backup jobs; p95 was 14.6 ms and maximum latency 34.9 ms. This is
  a synthetic local baseline, not a certified capacity envelope.
- Documentation links and references, bounded installer calls, Grafana
  dashboard metrics, quota fixture sizing, action pinning, embedded assets,
  shell syntax, and release metadata checks: passed.

## Hosted checks at capture

- Exact-head CodeQL for `d931c6a`: passed
  ([run](https://github.com/cyberducttape/StePanel/actions/runs/39538628057)).
- Exact-head integration CI: in progress at capture
  ([run](https://github.com/cyberducttape/StePanel/actions/runs/39538628072));
  the UI E2E job had passed, while the Go/integration job was still running.
- Exact-head installed-host matrix: in progress across Ubuntu 24.04, Rocky 9,
  and AlmaLinux 9 with Caddy/Apache combinations
  ([run](https://github.com/cyberducttape/StePanel/actions/runs/39538628104)).
- Exact-head quota-enabled install smoke: queued at capture
  ([run](https://github.com/cyberducttape/StePanel/actions/runs/39538628063)).

## Scope and remaining release gates

These repository checks do not establish physical power-loss recovery, repeated
installed-host recovery, XFS quota enforcement, a production capacity envelope,
application-level WordPress restoration on every supported web server, live
alert outage/retry behavior, upgrade/rollback certification, or independent
security assessment. Production remains **NO-GO** until the authoritative
requirements in `V1_PRODUCTION_GATES.md` have current, exact-source evidence.
