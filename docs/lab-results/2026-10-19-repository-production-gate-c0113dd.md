# Repository production gate — 2026-10-19 (`c0113dd`)

**Result: PASS for repository-level checks; production release remains NO-GO.**

Source commit: `c0113dd4f7da75c3d3624811d813686a6a8589f5` (`Reserve capacity before offsite archive downloads`). The gate ran on that exact checkout with `bash scripts/production-gate.sh` and exited successfully.

## Evidence

- `go vet ./...` and `go test -p 1 -timeout 10m ./...` passed.
- Aggregate statement coverage was **57.1%** (required 50%); all package-specific coverage floors passed.
- `go test -p 1 -race -timeout 10m ./...` passed; the root package completed in **472.072 seconds**.
- All repository recovery drills passed: partial SQL import cleanup (23s), interrupted transaction recovery (1s), configuration rollback (1s), and pending runtime reconciliation (2s).
- Control-plane benchmark samples: site mutations at 10/100/500 sites were 0.829/0.898/1.122 ms per operation; panel-worker and process contention samples were 0.731/0.733 ms; 50 backup/restore jobs were 48.9 ms per batch; audit-heavy mutation was 1.022 ms. These are single-iteration repository benchmarks, not capacity certification.
- Mixed HTTP test passed with 200 sites, 180 requests, four readers, and 20 backup jobs: average 4.1 ms, p95 13.3 ms, maximum 35.9 ms.
- Documentation links/references, smoke workflow bounds, Grafana dashboard, quota fixtures, action pins, asset model, and shell syntax checks passed.

## Recovery drill detail

The generated repository drill report is timestamped `2026-10-19T05:26:18Z`. These tests use synthetic temporary data and do not substitute for disposable-host or physical failure tests.

## Hosted checks at capture

At 2026-10-19 05:27 UTC, exact-head CI and CodeQL were queued; standard and quota-enabled installation smokes were pending. None count as passing evidence:

- [CI run 39203702895](https://github.com/cyberducttape/StePanel/actions/runs/39203702895)
- [CodeQL run 39203702910](https://github.com/cyberducttape/StePanel/actions/runs/39203702910)
- [Installation smoke run 39203703063](https://github.com/cyberducttape/StePanel/actions/runs/39203703063)
- [Quota-enabled installation smoke run 39203702833](https://github.com/cyberducttape/StePanel/actions/runs/39203702833)

## Limits and release decision

This run does not establish real-host ENOSPC behavior, physical power-loss recovery, installed-host aggregate quota enforcement (including XFS), repeated installed-host recovery, installed WordPress proof across the supported web-server matrix, live notification delivery/retry, upgrade/rollback certification, a capacity envelope, or independent security review. The repository production gate is green; the product is **not certified for customer production hosting**. See [`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md) for authoritative open gates.
