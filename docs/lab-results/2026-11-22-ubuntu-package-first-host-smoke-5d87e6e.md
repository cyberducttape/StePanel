# Ubuntu packaged first-install and recovery smoke — 2026-11-22

## Result

Passed once using source commit `5d87e6e86c098f467ee92b9e9992231c07121216`.

- [Installed-host workflow run 44998862460](https://github.com/cyberducttape/StePanel/actions/runs/44998862460)
- Host artifact: `stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb/install-smoke-output.txt`

The installed-host workflow built an exact-source GoReleaser snapshot archive,
validated the extracted installer tree, and installed it into a disposable
Ubuntu 24.04 systemd container with Caddy and MariaDB. Pinned WordPress and
WP-CLI fixtures were staged and checksum-verified before host mutation.

The run completed native installation and provisioned WordPress with a
database-backed canary. HTTPS, application recovery, database-outage recovery,
control-plane database outage diagnostics/recovery, and root-broker outage
readiness behavior passed. The WordPress restore rehearsal verified the
application and measured **4,627 ms** for backup
`20261122-075301.906818144-ci-wp-proof`.

The single recovery-matrix iteration passed. Additional successful drills
covered cPanel import and interrupted-import recovery, backup/restore/database
restore/termination interruption recovery, account suspension recovery,
database lock concurrency, root-broker restart during site creation, and safe
refusal of Git deployment when project quotas are unavailable.

The test host reported one non-blocking post-install action pending because
its fixture did not contain the `spamassassin.service` unit. The install
completed and its configured core services were verified.

## Limits

This is one disposable systemd-container run for Ubuntu 24.04 + Caddy +
MariaDB. It used an exact-source snapshot archive, not a published tagged
release. One recovery-matrix iteration does not meet repeated certification
requirements. This run does not establish physical power-loss durability,
broad ENOSPC behavior, quota enforcement, a certified capacity envelope, or
production readiness. It verifies the exercised WordPress recovery path and
the listed interruption cases only.
