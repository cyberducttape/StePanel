# Ubuntu 24.04 ext4 quota install smoke — 2026-11-23

## Result

The quota-enabled install workflow [45203221241](https://github.com/cyberducttape/StePanel/actions/runs/45203221241) passed on product source `5da789bb51e6da28e5c94667ebc8cf2f27049462`. It booted a disposable Ubuntu 24.04 QEMU guest using TCG, installed the release-shaped StePanel tree, and completed the installed-host smoke.

Before install, the workflow asserted that `/var/www` had active ext4 user and project quotas and `/home` had active user quotas. The install smoke exercised Git staging: a write beyond the configured project hard limit failed with `Disk quota exceeded`. A customer site was created and the smoke reported its starter quota enforced. The installed WordPress HTTPS/database recovery proof passed with measured RTO **188,757 ms** in this emulated guest. This latency is a result for this TCG run, not a production RTO target.

The workflow then killed the QEMU process with SIGKILL, rebooted the guest from the same disks, waited for systemd services and `/readyz`, and rechecked active quota accounting/enforcement and mount options on `/var/www` and `/home`. The job passed those checks and the separate backup/file-restore ENOSPC tests. This is one abrupt-guest-loss iteration, not physical host power-loss evidence or repeated certification.

## Caveats

- The run covers Ubuntu 24.04 and ext4 only; XFS and real production mounts remain to be verified.
- The guest console records a failed generic `quotaon.service` activation during boot. The workflow's explicit `quotaon -p` checks passed before and after reboot, and the quota-limit write was rejected as expected. Investigate the service-level diagnostic so operators are not left with a misleading failed unit.
- QEMU TCG is slow; the measured WordPress recovery time includes this test environment and must not be presented as a customer RTO guarantee.
- This supplements the versioned container candidate smoke; it does not publish a release or close repeated recovery, broad ENOSPC, physical power-loss, or capacity gates.

The retained install and guest-console diagnostics are attached to the workflow run.
