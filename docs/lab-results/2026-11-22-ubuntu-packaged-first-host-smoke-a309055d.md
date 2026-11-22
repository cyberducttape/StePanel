# Ubuntu packaged first-host and recovery smoke — 2026-11-22

**Source:** [`a309055dd03bf8ff8c5cb1cc7feaf97afb5f515f`](https://github.com/cyberducttape/StePanel/commit/a309055dd03bf8ff8c5cb1cc7feaf97afb5f515f)

**Workflow:** [Installation smoke run 45069529493](https://github.com/cyberducttape/StePanel/actions/runs/45069529493)

**Host profile:** Ubuntu 24.04, Caddy, MariaDB

**Result:** Passed; one recovery-matrix iteration.

The workflow built the release archive from this source, installed it on a
disposable systemd host, and completed installation with
`post_install_status=complete`. Its installed WordPress recovery proof restored
the database-backed site, verified the HTTPS application endpoint, and
recorded `rto_ms=6996` (6.996 seconds). The drill also recovered from a
simulated database outage and completed one interrupted-operation recovery
matrix iteration.

The smoke host did not provide project quotas. The Git deployment check was
therefore expected to refuse deployment and verified that the live site stayed
unchanged. This is evidence of fail-closed behavior, not project-quota
enforcement.

This is a single disposable-host installation and recovery smoke, not
production certification or the repeated host-failure evidence required by
`docs/V1_PRODUCTION_GATES.md`. It does not certify a published tagged release.

The complete step output is retained as the workflow artifact
`stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb` in run
45069529493.
