# Rocky Linux 9 installed-host recovery checkpoint — 2026-10-24

**Application source:** `1b2e99d0ee7eb15d7cbaca40e0d114db6ddf4e27` (clean tree)
**Checkpoint result:** installed-host smokes and one WordPress application
recovery proof passed; the extended recovery-boundary matrix is still running.
**Release decision:** NO-GO. This is one Rocky Linux 9 + Caddy + MariaDB run,
not the required repeated multi-platform release certification.

## Environment

| Item | Value |
| --- | --- |
| Guest | Rocky Linux 9 GenericCloud |
| Guest image SHA-256 | `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8` |
| Host QEMU | 10.2.1 (`Debian 1:10.2.1+ds-1ubuntu3.2`) |
| Host kernel | `7.0.0-38-generic` |
| Guest kernel | `5.14.0-687.10.1.el9_8.0.1.x86_64` |
| Guest memory / CPUs | 4096 MiB / 4 |
| Guest disk cache | `none` |
| Web server / database | Caddy / MariaDB |
| `/var/www` | Dedicated ext4 with user and project quotas enabled |
| Started | 2026-10-24 18:11:50 UTC |

## Passed at this checkpoint

- Clean Rocky boot and quota mount-state verification.
- Native installation and service startup, including vsftpd.
- cPanel import, re-import, and worker-kill recovery during activation.
- SFTP-only OpenSSH client/server upload, download, denial, and revocation checks;
  FTPS upload/download, SSH password isolation, and revocation checks.
- Independent database operations overlapped under the lock smoke.
- Worker-kill recovery during backup archive creation, file restore activation,
  and database restore; worker-kill recovery during site termination.
- Account-suspension process-kill recovery, Git activation recovery, and app
  lifecycle smoke.
- WordPress was installed on the host, populated with a published
  database-backed probe post, backed up with its database, then rehearsed.
  Archive, database, and application HTTP checks passed; measured recovery
  time was **15,524 ms**.
- Stopping MariaDB made the WordPress probe fail with HTTP 500; restarting it
  restored the same database-backed probe. Stopping the root broker degraded
  management readiness while liveness remained available; readiness recovered
  when the broker restarted.
- Repeated vhost deletion succeeded when the route file was already absent.

The extended interruption matrix (`RECOVERY_MATRIX_FULL=1`, one iteration)
was running when this checkpoint was captured. Its final result, subsequent
runner and abrupt-loss phases, and ENOSPC results are not represented here as
passes. Artifacts are retained locally under
`/var/tmp/stepanel-kvm-cert-20261024-1b2e99d`.

This checkpoint does not establish physical host power-loss recovery, a
20–50-repeat cohort, broader platform coverage, multi-process quota
enforcement, a certified capacity envelope, or independent security review.
