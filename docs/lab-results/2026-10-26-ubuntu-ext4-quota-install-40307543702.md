# Ubuntu ext4 quota-enabled install smoke — 2026-10-26

**Result:** PASS for one disposable Ubuntu guest run. This is partial quota and
installed-host evidence, not production certification.

| Item | Value |
| --- | --- |
| GitHub Actions run | [40307543702](https://github.com/cyberducttape/StePanel/actions/runs/40307543702) |
| Job | [quota-install, 121520281379](https://github.com/cyberducttape/StePanel/actions/runs/40307543702/job/121520281379) |
| Application source | [`ab6cde808faef6e31d8a66eacf044d28cb901cd9`](https://github.com/cyberducttape/StePanel/commit/ab6cde808faef6e31d8a66eacf044d28cb901cd9) |
| Started / finished | 2026-10-26 10:27:24–11:34:48 UTC |
| Guest | Ubuntu 24.04 cloud image under QEMU TCG on a GitHub-hosted Ubuntu 24.04 runner |
| Stack | Caddy, PHP-FPM, MariaDB |
| Quota filesystems | ext4 `/var/www` with user and project quotas; separate quota-enabled `/home` |

## Verified outcomes

- The install workflow verified user and project quotas on `/var/www` before
  installation, then checked quota mount options and enforcement again after
  abruptly killing and rebooting the QEMU guest with persistent disks.
- A Git release-staging write reached the project quota and failed with the
  expected `Disk quota exceeded` error. Customer-site provisioning also passed
  with the configured starter quota.
- SFTP-only OpenSSH authentication, FTPS upload/download with SSH password
  isolation and revocation, file management, scheduled-task safeguards, and
  site lifecycle smokes passed.
- Cpmove import and recovery, database lock coordination, backup and restore
  recovery, termination recovery, app lifecycle, and deploy rollback smokes
  passed.
- Installed WordPress restore completed its HTTPS/application proof in 90.545
  seconds. The run also verified recovery after database and root-broker
  outages.
- After abrupt QEMU guest termination and restart, systemd and `/readyz`
  recovered, and the workflow's post-reboot user/project quota assertions
  passed on `/var/www` and user-quota assertions passed on `/home`.
- On a separate 96 MiB ext4 volume, the real-ENOSPC backup and file-restore
  recovery tests passed.
- Rootless runner and release-pipeline smokes passed.

## Scope and limitations

- This is one Ubuntu 24.04/ext4/Caddy/MariaDB guest run on source `ab6cde8`.
  It does not establish XFS behavior or quota certification across the
  supported distribution/filesystem matrix.
- Killing QEMU validates abrupt guest termination with persistent virtual
  disks. It does not simulate physical host power loss or loss of the storage
  device's volatile write cache.
- The two real-ENOSPC tests cover backup creation and file restoration on a
  dedicated test volume. They do not cover every publication, import, database
  restore, or host-wide exhaustion boundary.
- The run does not satisfy the required 20–50-repeat full installed-host
  recovery cohort or establish a certified capacity envelope.

This result closes neither the broader host recovery gate nor the full quota
matrix. Production remains **NO-GO**; see
[`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md).
