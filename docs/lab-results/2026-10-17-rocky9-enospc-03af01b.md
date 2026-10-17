# Focused Rocky Linux 9 ext4 ENOSPC drill — 2026-10-17

**Source:** `03af01be8c4ce7d201e43fa7fb68dbf2c38939fb` (documentation-only
changes from application source `a27beccc9b5be33a7e30d57662d027c74e6a6cbe`)  
**Result:** PASS for backup and file-restore ENOSPC tests only.

| Item | Value |
| --- | --- |
| Guest | Rocky Linux 9.8 GenericCloud |
| Guest image SHA-256 | `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8` |
| Host QEMU | 10.2.1 |
| Guest kernel | `5.14.0-687.10.1.el9_8.0.1.x86_64` |
| Guest memory / CPUs | 3072 MiB / 2 |
| `/var/www` | Dedicated ext4 volume, mounted with `usrquota`; user quota verified active |
| ENOSPC volume | Dedicated 96 MiB ext4 filesystem |
| Started / finished | 2026-10-17 02:56:11–02:59:59 UTC |

The disposable guest booted and cloud-init completed in 175 seconds. The
focused tests then passed on the dedicated constrained ext4 volume:

- `TestBackupRecoversFromRealENOSPC` — PASS (2.54 s)
- `TestRestoreFilesRecoversFromRealENOSPC` — PASS (2.74 s)

This drill does **not** cover site publication, archive import, database
restore, whole-host ENOSPC, physical host power loss, installed-host
certification, or repeated recovery-matrix requirements. It closes only the
two focused test cases named above. Full raw phase output and checksummed
environment metadata remain in `/var/tmp/stepanel-kvm-enospc-a27becc` on the
test workstation; this report contains the essential reproducibility data.
