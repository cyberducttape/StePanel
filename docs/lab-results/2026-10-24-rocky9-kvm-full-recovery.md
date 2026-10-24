# Rocky Linux 9 KVM installed-host and abrupt-loss recovery — 2026-10-24

**Application source:** `b20c60989cdeabf04b796038eafd0e70ef263392`  
**Result:** PASS for one installed-host recovery-matrix iteration and the
explicit scenarios below. This is evidence for a single Rocky Linux 9,
Caddy, PHP-FPM, and MariaDB host configuration; it is not production
certification.

| Item | Value |
| --- | --- |
| Guest | Rocky Linux 9.8 GenericCloud |
| Guest image SHA-256 | `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8` |
| Host QEMU | 10.2.1 (`Debian 1:10.2.1+ds-1ubuntu3.2`) |
| Host kernel | `7.0.0-38-generic` |
| Guest kernel | `5.14.0-687.10.1.el9_8.0.1.x86_64` |
| Guest memory / CPUs | 4096 MiB / 4 |
| Guest disk cache | `none` |
| SELinux | Enforcing |
| Filesystem | ext4 with user and project quotas on `/var/www` and `/home` |
| Services | StePanel, separate worker, root broker, Caddy, PHP-FPM, MariaDB |
| Recovery-matrix repetitions | 1 |
| Run directory | `/var/tmp/stepanel-kvm-cert-20261024-b20c609` |
| Started / finished | 2026-10-24 11:30–16:51 UTC |

## Results

- Installed-host recovery matrix: PASS, one iteration (15,370 seconds from
  guest boot, including host setup and package installation).
- Rootless runner and release pipeline: PASS.
- Whole-guest QEMU `SIGKILL` while idle: PASS; services and readiness recovered.
- Real ext4 ENOSPC during backup and file restore: PASS on a dedicated 96 MiB
  volume.
- Whole-guest `SIGKILL` during backup: PASS; job recovered, the published
  backup verified, no staging directory remained, and a fresh backup passed.
- Whole-guest `SIGKILL` during file restore: PASS; the site returned to the
  original complete file state, served it over HTTP, and passed a fresh
  restore.
- Whole-guest `SIGKILL` during MariaDB restore: PASS; all eight tables matched
  the backup after replay, and the original safety backup was retained.
- Whole-guest `SIGKILL` during Git activation: PASS; the original release
  remained intact and served over HTTP, and a subsequent Git deployment
  completed.
- Control-plane schedule writes during whole-guest `SIGKILL`: PASS; all
  acknowledged writes were retained and database integrity/readiness recovered.
- SELinux document-root labeling: PASS; account-placed PHP site returned HTTP
  200 under enforcing SELinux.

The Git interruption scenario also exercised the root broker's quota checks
inside its systemd mount namespace. It exposed mount-alias assumptions in
`quotaon` and `setquota`; the corrected helpers match and apply quota state by
filesystem device. The regression contract test and a live broker-namespace
quota operation passed. The targeted Git interruption and fresh deployment
were rerun successfully with those helper changes.

Raw logs, results, and VM artifacts are retained in the run directory above on
the test workstation. The installed application binary was built from the
application source commit listed above. The final focused Git run used the
working-tree quota helper revisions that are included with this report's
change; those helper changes still require exact-head CI after commit.

## Limits

- One Rocky Linux 9 KVM repetition does not satisfy the required 20–50
  repeated recovery cohort, nor the supported OS and web-server matrix.
- QEMU `SIGKILL` tests abrupt guest termination. It does not simulate physical
  host power loss, storage-controller failure, or loss of the host filesystem.
- ENOSPC coverage here is limited to backup creation and file restore on the
  dedicated test volume. It does not establish full-host exhaustion behavior
  during import, publication, database restore, or control-plane writes.
- This run does not certify capacity, sustained load, alert delivery, upgrade
  rollback, or hostile multi-tenant isolation.

Production remains **NO-GO**. See
[V1_PRODUCTION_GATES.md](../V1_PRODUCTION_GATES.md) for the authoritative
release contract.
