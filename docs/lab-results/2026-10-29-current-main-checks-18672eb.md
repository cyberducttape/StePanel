# Current-main repository and host checks — 2026-10-29

**Source:** [`18672eb263eef02cb97dd5c9056b6911d87ccd93`](https://github.com/cyberducttape/StePanel/commit/18672eb263eef02cb97dd5c9056b6911d87ccd93)
**Local environment:** Go 1.26.10, Linux amd64, kernel 7.0.0-38-generic.
**Repository state:** `main`, clean before this evidence-only documentation update; `HEAD` matched `origin/main`.

## Results

| Check | Result | Evidence |
| --- | --- | --- |
| API artifact consistency, `go vet ./...`, and `GOMAXPROCS=2 go test -p 1 -timeout 10m ./...` (`make check`) | PASS | `make check` exited 0; root-package tests completed in 70.5 seconds and all packages passed or had no tests. |
| CI integration | PASS | Exact-source-content match, run [40869721269](https://github.com/cyberducttape/StePanel/actions/runs/40869721269), 2/2 jobs. |
| CodeQL | PASS | Exact-source-content match, run [40869721146](https://github.com/cyberducttape/StePanel/actions/runs/40869721146), 1/1 job. |
| Documentation | PASS | Exact-tree match, run [40869721278](https://github.com/cyberducttape/StePanel/actions/runs/40869721278), 1/1 job. |
| Installed-host smoke | PASS | Exact-source run [40869721311](https://github.com/cyberducttape/StePanel/actions/runs/40869721311); the AlmaLinux 9/Caddy/MariaDB installed install-and-recovery smoke job [123434558460](https://github.com/cyberducttape/StePanel/actions/runs/40869721311/job/123434558460) passed. |

The prior `TestGitStageQuotaIsSkippedOnlyInDevelopment` timeout was not
reproduced: its focused test passed, and the complete serialized `make check`
passed on this source. The full check includes API artifact verification,
`go vet`, and the Go package test suite; it does not include the separate race,
production-gate, or installed-host certification workflows.

The evidence generator found no pull-request CI run with equivalent code
content: the latest pull-request run was successful but used source `38e5f86`.
It is therefore not counted as current-source PR evidence. This report records
the passing exact-source CI integration workflow separately.

## Limits and open release gates

The installed-host result is one disposable AlmaLinux 9/Caddy/MariaDB run. It
does not establish a current-source six-host matrix, repeated recovery
reliability, physical host power-loss behavior, complete ENOSPC recovery, or a
certified capacity envelope. The authoritative decision remains **NO-GO**;
see [the production gates](../V1_PRODUCTION_GATES.md).
