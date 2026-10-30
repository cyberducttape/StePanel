# Exact-source AlmaLinux 9 installed-host smoke — 2026-10-30

## Result

The native installation and one-iteration recovery smoke passed on the exact
source commit `79e1de4ac3799ab5642488b69fd6b7b063d03d9b` in the hosted
AlmaLinux 9 / Caddy / MariaDB environment. The GitHub Actions run is
[41019722358](https://github.com/cyberducttape/StePanel/actions/runs/41019722358).
The uploaded diagnostic artifact completed successfully and was reviewed.

The installed smoke exercised startup and recovery after database outage,
OpenSSH SFTP-only access, FTPS transfer and revocation, PHP-FPM status,
scheduled-task safeguards, cPanel archive import, database lock overlap,
worker interruption during backup publication and restore, termination
recovery, broker outage readiness behavior, account suspension recovery,
vhost deletion replay, Git deployment refusal when project quotas are
unavailable, and application lifecycle operations.

The installed WordPress recovery proof restored a database-backed application
and passed its HTTP probe. The measured recovery time was 7,309 ms for backup
`20261030-073831.986582402-ci-wp-proof`. The same run confirmed that a
database outage produced an HTTP 500 during the outage and recovered
afterward. Root-broker outage degraded readiness while liveness remained
available; readiness recovered after the broker returned.

The one-iteration recovery matrix passed its cPanel activation, backup
verification, restore commit, termination routes, and account suspension
worker/panel interruption cases. The run explicitly refused Git deployment
because project quotas were unavailable and verified that the live site was
unchanged.

## Limits

This is one disposable hosted VM run on one OS/web-server/database
combination. It is useful exact-source regression evidence; it does not satisfy
the required 20–50 repeated recovery cohort. It does not establish physical
host power-loss durability, broad ENOSPC behavior during database restore or
publication, a six-combination current-source runtime matrix, sustained
capacity, or production readiness. The release decision remains **NO-GO**.
