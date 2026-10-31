# Exact-source AlmaLinux 9 installed-host smoke — 2026-10-31

## Result

GitHub Actions run
[41187605802](https://github.com/cyberducttape/StePanel/actions/runs/41187605802)
completed successfully on the exact source
`1e6db185e5fc77e034280e9182d30e47c562c190` in a disposable AlmaLinux 9,
Caddy, MariaDB systemd host. The installed-host job ran from 09:12:05 to
09:38:37 UTC. Its diagnostic artifact,
`stepanel-install-diagnostics-default-almalinux-9-caddy-mariadb`, was
downloaded and reviewed.

The smoke passed FTPS upload/download, SSH password isolation and revocation,
PHP-FPM status, scheduled-task safeguards, cPanel import, database-lock
overlap, broker outage readiness behavior, and recovery after worker or panel
termination during cPanel activation, backup verification, restore commit,
database restore, site termination, and account suspension. The one-iteration
recovery matrix completed successfully. Git deployment was refused because
project quotas were unavailable, and the live site remained unchanged.

The installed WordPress proof restored a database-backed application and
passed its HTTP probe. Measured RTO was 7,333 ms for backup
`20261031-092701.815822506-ci-wp-proof`. The smoke also confirmed HTTP 500
during an injected database outage and recovery afterward. A root-broker
outage degraded readiness while liveness remained available; readiness
recovered when the broker returned.

## Limits

This is one disposable-host run on one OS, web server, and database
combination. It does not satisfy the 20–50 repeated recovery requirement or
establish physical host power-loss durability, broad ENOSPC coverage, a
current-source runtime matrix, capacity certification, or production
readiness. The run did not configure an audit-checkpoint remote and does not
verify provider-side append-only policy or remote outage recovery. The release
decision remains **NO-GO**.
