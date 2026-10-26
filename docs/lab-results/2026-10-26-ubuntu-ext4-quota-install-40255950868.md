# Ubuntu ext4 quota-enabled install smoke — 2026-10-26

**Result:** PASS for one disposable Ubuntu guest run. This is partial quota and
installed-host evidence, not production certification.

| Item | Value |
| --- | --- |
| GitHub Actions run | [40255950868](https://github.com/cyberducttape/StePanel/actions/runs/40255950868) |
| Job | [quota-install, 121357732915](https://github.com/cyberducttape/StePanel/actions/runs/40255950868/job/121357732915) |
| Application source | [`a26de03ee0ce9f942e265f1101c0e377bcd09966`](https://github.com/cyberducttape/StePanel/commit/a26de03ee0ce9f942e265f1101c0e377bcd09966) |
| Started / finished | 2026-10-26 01:51:40–02:58:34 UTC |
| Guest | Ubuntu 24.04 cloud image under QEMU TCG on a GitHub-hosted Ubuntu 24.04 runner |
| Stack | Caddy, PHP-FPM, MariaDB |
| Quota filesystem | ext4; dedicated `/var/www` volume with user and project quotas; separate quota-enabled `/home` volume |

## Verified outcomes

- The guest preflight confirmed `/var/www` was mounted with `usrquota,prjquota`
  and reported both user and project quota state as on.
- The Git release-staging quota test reached the hard limit and received the
  expected `Disk quota exceeded` write error.
- Customer-site provisioning and file-manager smoke passed for a site placed
  under `/home/cismokecust`, with its starter quota enforced.
- SFTP-only OpenSSH authentication, FTPS upload/download with SSH password
  isolation and revocation, site lifecycle, deployment rollback, and the
  standard installed recovery-matrix smoke passed.
- Installed WordPress restore completed its HTTPS/application proof in
  83.017 seconds. The run also confirmed the expected degraded response during
  database outage and recovery after the database and root broker returned.
- After `SIGKILL` of the QEMU process and reboot of the same guest disks, panel
  and worker readiness recovered.
- On a separate 96 MiB ext4 volume, `TestBackupRecoversFromRealENOSPC` and
  `TestRestoreFilesRecoversFromRealENOSPC` both passed.
- Rootless runner permission and release-pipeline smoke passed.

## Scope and limitations

- This is one Ubuntu 24.04/ext4/Caddy/MariaDB guest run on source `a26de03`,
  before the final PR set was merged to `main`. It supports the placement and
  quota behavior exercised here but is not an exact-current-main certification
  run.
- It does not test XFS, quota behavior on AlmaLinux or Rocky Linux, or repeated
  full recovery cohorts. It does not simulate physical host power loss; killing
  QEMU validates abrupt guest termination with persistent virtual disks.
- The run covers two specific real-ENOSPC test cases, not every write path or
  aggregate host-capacity behavior.
- The captured guest console includes an early failed `quotaon.service` start.
  The workflow subsequently prepared and mounted the quota filesystem, then
  explicitly verified active user/project enforcement before installation.
  This run establishes effective enforcement during the smoke; it does not
  establish that the early systemd unit activation succeeded or that quota
  state was rechecked after the simulated reboot. The quota workflow now adds
  an explicit post-reboot quota-state assertion; that change still needs a
  hosted run.

This result closes neither the supported-filesystem matrix nor the broader
host recovery gate. Production remains **NO-GO**; see
[`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md).
