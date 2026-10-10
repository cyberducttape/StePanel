# Rocky Linux 9 KVM full recovery matrix — 2026-10-10

This run completed the production-shaped installation and the exhaustive
installed-host recovery matrix on a hardware-virtualized Rocky Linux 9 guest.
It is additional evidence for the journal-boundary workflows only. It is not
evidence of physical power-loss recovery, repeated matrix certification, or
ENOSPC outside the backup path.

## Environment

| Item | Value |
| --- | --- |
| Harness | `deploy/lab/local-kvm-certification.sh` |
| Guest | Rocky Linux 9 GenericCloud, SHA-256 `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8` |
| Guest kernel | `5.14.0-687.10.1.el9_8.0.1.x86_64`, SELinux enforcing |
| Guest resources | 4 vCPU, 3 GiB RAM, 20 GiB root, 3 GiB ext4 `/var/www` with user quotas |
| Stack | Caddy, MariaDB, PHP-FPM, quota-enabled production install |
| Build | `3b1a44a` (clean worktree) |
| Duration | Boot 122 s; install and matrix 10,622 s |

## Results

| Phase | Result | Evidence |
| --- | --- | --- |
| Boot and quota setup | PASS | Guest booted with `quota,usrquota` on `/var/www` |
| Install and full recovery matrix | PASS | `107 smoke/matrix passes`; recovery matrix reported one completed iteration |
| Worker health after matrix | PASS | Worker remained active at completion |

The matrix exercised the supported cpmove, backup, restore, termination, and
account-suspension journal boundaries, including worker/panel interruption
recovery. The run did not execute the harness's separate runner, abrupt-loss,
or ENOSPC phases, and it was a single iteration.

