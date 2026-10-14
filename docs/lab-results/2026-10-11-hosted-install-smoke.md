# Hosted installation smoke — 2026-10-11

The production-shaped installation smoke passed on both supported EL9
container hosts for commit `a0c0585aff7323d0d9898133f75d8e117e554e10`.
This adds current installation and default recovery-matrix evidence; it does
not close the VM-level failure-recovery gate.

## Environment and source

| Item | Value |
| --- | --- |
| Repository commit | `a0c0585aff7323d0d9898133f75d8e117e554e10` |
| Rocky job | [Installation smoke run 38142442028](https://github.com/cyberducttape/StePanel/actions/runs/38142442028), job `114478939898` |
| AlmaLinux job | [Installation smoke run 38142442028](https://github.com/cyberducttape/StePanel/actions/runs/38142442028), job `114478940255` |
| CI | [Run 38142442011](https://github.com/cyberducttape/StePanel/actions/runs/38142442011) |
| CodeQL | [Run 38142442001](https://github.com/cyberducttape/StePanel/actions/runs/38142442001) |
| Hosts | Rocky Linux 9 and AlmaLinux 9, Caddy and MariaDB, systemd-enabled privileged containers on Ubuntu 24.04 runners |
| Repetitions | One default recovery-matrix iteration per OS |

## Results

Both installation jobs completed successfully. Their logs show successful
fresh and replacement cPanel imports into the same site, SFTP client/server
authentication checks, database lock overlap, cPanel recovery, backup and
file/database restore recovery, termination, account-suspension recovery,
deployment recovery, and app lifecycle smoke. The default recovery matrix
passed on both distributions.

CI (including Go 1.26.9 tests and UI end-to-end tests) and CodeQL completed
successfully for this commit. The CodeQL alert inventory had no open alerts at
the time it was checked.

## Limits

These are privileged systemd-container tests, not clean-VM certification.
They do not establish physical power-loss behavior, ENOSPC during database
restore or outside backup creation, repeated full-matrix reliability, or
application-level WordPress recovery. At the time of this run, the quota-
enabled Ubuntu installation was still running and was not part of these
results; it later completed successfully with the distinct evidence and limits
recorded in the [2026-10-14 quota-install result](./2026-10-14-quota-install-smoke.md).
