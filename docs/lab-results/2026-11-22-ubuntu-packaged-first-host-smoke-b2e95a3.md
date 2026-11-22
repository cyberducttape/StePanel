# Ubuntu packaged first-install and WordPress recovery smoke — 2026-11-22

## Result

Passed on exact source commit
[`b2e95a3a749b5e0d4b7aa6a6015ea930000516e4`](https://github.com/cyberducttape/StePanel/commit/b2e95a3a749b5e0d4b7aa6a6015ea930000516e4).

- [Installed-host workflow run 45009421543](https://github.com/cyberducttape/StePanel/actions/runs/45009421543)
- [Integration CI run 45008851534](https://github.com/cyberducttape/StePanel/actions/runs/45008851534)
- Host artifact: `stepanel-install-diagnostics-first-run-b2e95a3a-ubuntu-24.04-caddy-mariadb/install-smoke-output.txt`

The workflow built a GoReleaser snapshot from the exact source, extracted and
validated the packaged installer tree, then installed it on a disposable
Ubuntu 24.04 systemd container using Caddy and MariaDB. The installer reported
`post_install_status=complete` and printed the hostname, HTTPS, sign-in, and
first-run checklist steps. The smoke also verified the installed service stack.

The test provisioned WordPress, created a backup, restored it into the
recovery proof environment, imported the database, and served a database-backed
canary over HTTPS. Application-level recovery passed in **7,565 ms** for
backup `20261122-100932.128530295-ci-wp-proof`. During an injected MariaDB
outage, the site returned HTTP 500; it recovered after the database returned.
An injected broker outage degraded readiness while preserving liveness, and
the panel recovered after the broker returned.

The same host run passed one iteration of the recovery matrix, including
cPanel import, backup, restore, termination, suspension, and deployment
interruption checks. Additional installed-host checks covered file management,
tenant boundaries, SFTP/FTPS, PHP-FPM status, scheduled-task safeguards, and
root-broker interruption.

## Limits

This is one disposable-container run on Ubuntu 24.04 + Caddy + MariaDB using an
exact-source snapshot archive. It is not a published tagged release or
repeated certification. The HTTPS proof used the disposable host's local test
trust setup; it does not establish public DNS or certificate issuance. The run
does not establish physical power-loss durability, broad ENOSPC behavior,
filesystem quota enforcement, a certified capacity envelope, or production
readiness. Those release gates remain open.
