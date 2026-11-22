# Ubuntu first-install and WordPress recovery smoke — 2026-11-22

## Result

Passed once on exact code source `e04235f838834f32b6a54d40cc57e50648c00339`.

- [Installed-host run 44990700250](https://github.com/cyberducttape/StePanel/actions/runs/44990700250)
- [Matching integration CI run 44990700232](https://github.com/cyberducttape/StePanel/actions/runs/44990700232)
- Host artifact: `stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb/install-smoke-output.txt`

The workflow built and extracted an exact-source GoReleaser snapshot archive,
then installed it into a disposable Ubuntu 24.04 systemd container with Caddy
and MariaDB. Pinned WordPress and WP-CLI fixtures were fetched and SHA-256
verified on the CI runner, then mounted read-only into the host before
installation. This avoids depending on external downloads during host setup
and recovery.

The host created a WordPress site and database, served a database-backed
canary post over HTTPS, created a backup, restored it, and passed the
application recovery proof. Measured recovery time was **4,351 ms** for
backup `20261122-060202.003421905-ci-wp-proof`.

The same run verified that a MariaDB outage produced an HTTP failure and
recovered after the database returned; a root-broker outage degraded
readiness while preserving liveness and recovered after broker restart. The
smoke also passed cPanel import and interrupted-import recovery, database
locking, backup and file/database restore interruption recovery, site
termination recovery, account suspension recovery, Git deployment refusal
without project quotas, root-broker interruption recovery, and one recovery
matrix iteration.

## Limits

This is one disposable systemd-container run for Ubuntu 24.04 + Caddy +
MariaDB. The release-shaped snapshot is not an official tagged release. The
single recovery-matrix iteration does not meet the project's repeated
certification requirement. The container is not a physical host; this run
does not establish physical power-loss durability, broad ENOSPC behavior,
filesystem quota enforcement, a certified capacity envelope, or production
readiness. The recovery proof establishes the tested WordPress application
path only and does not certify mail, DNS cutover, or every supported runtime
combination.
