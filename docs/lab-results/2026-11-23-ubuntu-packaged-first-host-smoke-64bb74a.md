# Ubuntu packaged first-host and recovery smoke — 2026-11-23

**Source:** [`64bb74a46e53055a69ac37f1c5adca2f73b3c315`](https://github.com/cyberducttape/StePanel/commit/64bb74a46e53055a69ac37f1c5adca2f73b3c315)

**Workflow:** [Installation smoke run 45094688912](https://github.com/cyberducttape/StePanel/actions/runs/45094688912)

**Host profile:** Ubuntu 24.04, Caddy, MariaDB

**Result:** Passed; one recovery-matrix iteration.

The workflow built a GoReleaser archive from the exact source commit and
installed it in a disposable systemd container. Installation reported
`post_install_status=complete` and
`post_install_cleanup_status=complete`. The installed WordPress database and
HTTPS recovery proof passed for backup
`20261123-014731.273602198-ci-wp-proof`; measured recovery time was
**4,407 ms**. The smoke also recovered from an installed WordPress database
outage and verified that broker outage degraded readiness while the panel
remained live, followed by successful recovery.

One recovery-matrix iteration passed, including interrupted cPanel import,
backup, restore, termination, and account-suspension recovery. The broker
interruption and application lifecycle smokes also passed.

The disposable host did not provide project quotas. Git deployment was
therefore refused, and the live site remained unchanged. This proves
fail-closed behavior, not quota enforcement. This run does not establish
physical power-loss recovery, external DNS/TLS issuance, offsite restoration,
20–50 repeated recovery runs, or a supported production capacity envelope.
It is a single systemd-container smoke, not production certification or a
published tagged-release installation.

The complete step output is retained as the workflow artifact
`stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb` in run
45094688912.
