# Ubuntu packaged first-host and recovery smoke — 2026-11-23

**Source:** [`df838fa9c5fe1e7b625924a84daccc4f7f0d9555`](https://github.com/cyberducttape/StePanel/commit/df838fa9c5fe1e7b625924a84daccc4f7f0d9555)

**Workflow:** [Installation smoke run 45100771925](https://github.com/cyberducttape/StePanel/actions/runs/45100771925)

**Host profile:** Ubuntu 24.04, Caddy, MariaDB

**Result:** Passed; one recovery-matrix iteration.

The workflow built and extracted a GoReleaser snapshot from this exact source,
installed it in a disposable systemd container, and completed installer
cleanup. The installed-host smoke verified that `/usr/local/bin/stepanelctl`
exists, is root-owned, and runs its `help` command successfully. The
WordPress database and HTTPS recovery proof passed; measured recovery time was
**4,435 ms**. One recovery-matrix iteration passed.

This is one disposable systemd-container run. It does not establish project
quota enforcement, physical power-loss recovery, external DNS/TLS issuance,
offsite restoration onto an independent host, repeated recovery certification,
or a production capacity envelope. It is not a tagged-release installation or
production certification.

The full install diagnostics are retained as the
`stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb` artifact in
run 45100771925.
