# Current-source Rocky Linux 9 KVM ext4 ENOSPC drill — 2026-10-24

**Source:** `9e70d4ec6ce83fcc8811a22e81069b70f5d09139` (clean tree; Go
application source is unchanged from `dc25ddc`; latest installed-host smoke
source is `a48aafc2bfed8072e4be47878aaa98e5a835a22b`)  
**Result:** PASS for guest boot, quota mount-state verification, backup ENOSPC,
and file-restore ENOSPC only.

| Item | Value |
| --- | --- |
| Guest | Rocky Linux 9 GenericCloud |
| Guest image SHA-256 | `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8` |
| Host QEMU | 10.2.1 (`Debian 1:10.2.1+ds-1ubuntu3.2`) |
| Host kernel | `7.0.0-38-generic` |
| Guest kernel | `5.14.0-687.10.1.el9_8.0.1.x86_64` |
| Guest memory / CPUs | 4096 MiB / 4 |
| Guest disk cache | `writeback` |
| `/var/www` | Dedicated ext4 volume; user and project quotas enabled and verified |
| ENOSPC volume | Dedicated 96 MiB ext4 filesystem |
| Started / finished | 2026-10-24 08:57:43–08:59:52 UTC |

The disposable guest booted in 85 seconds. The focused tests passed against
the dedicated constrained ext4 filesystem:

- `TestBackupRecoversFromRealENOSPC` — PASS (2.51 s)
- `TestRestoreFilesRecoversFromRealENOSPC` — PASS (2.77 s)

Raw output, environment metadata, and the generated guest artifacts are in
`/var/tmp/stepanel-kvm-cert-20261024` on the test workstation. The source
archive and test binary were built from the commit above with a clean tree.

This run does **not** test site publication, archive import, database-restore
ENOSPC, full installation, whole-host ENOSPC, abrupt power loss, installed-host
recovery, competing customer writes, or repeated recovery-matrix requirements.
The quota mount state was verified, but this focused drill does not certify
quota enforcement under a multi-process workload. It closes only the two
named ENOSPC tests and boot-time quota-state check; production remains **NO-GO**.
