# Rocky Linux 9 KVM focused ENOSPC drill — 2026-10-30

**Application source:** `1130c071f3c5f1319c004b67580c7ac1e9d538cd`

**Result:** PASS for backup creation and file restore on a filesystem that
reaches real `ENOSPC`. This is a focused repository-level KVM drill, not an
installed-host certification or a completed release gate.

| Item | Value |
| --- | --- |
| Guest image | Rocky Linux 9 GenericCloud |
| Guest image SHA-256 | `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8` |
| Host QEMU | 10.2.1 (`Debian 1:10.2.1+ds-1ubuntu3.2`) |
| Host kernel | `7.0.0-38-generic` |
| Guest kernel | `5.14.0-687.10.1.el9_8.0.1.x86_64` |
| Guest memory / CPUs | 2048 MiB / 4 |
| Guest disk cache | `writeback` |
| `/var/www` filesystem | ext4, SELinux enabled, user and project quota mount options |
| ENOSPC test filesystem | Dedicated 96 MiB ext4 volume at `/mnt/stepanel-enospc` |
| Started / finished | 2026-10-30 05:09:44–05:12:00 UTC |
| Recovery phase log directory | `/var/tmp/stepanel-kvm-enospc-70bdc34` on the test workstation |

## Results

- Rocky Linux 9 guest boot and quota mount check: PASS (83 seconds).
- `TestBackupRecoversFromRealENOSPC`: PASS (1.75 seconds).
- `TestRestoreFilesRecoversFromRealENOSPC`: PASS (1.88 seconds).
- Combined `enospc` phase: PASS (32 seconds); the harness verified cleanup
  and recovery after the dedicated volume filled.

The source tree was clean when the harness recorded the commit. The run built
the panel and root-broker binaries and compiled the repository's root-package
ENOSPC test binary from that source. Raw phase output, `results.tsv`, and
`environment.txt` are retained in the run directory above.

## Limits

- This run did not execute the native installer, systemd services, or the
  installed-host recovery matrix. The local workstation did not have the
  harness-required 4 GiB guest plus 1.5 GiB host memory reserve available, so
  the full install run was not started.
- It covers backup creation and file restore only. Database restore, cPanel or
  WordPress import, offsite publication, and control-plane writes under
  `ENOSPC` remain untested in this run.
- `writeback` preserves host page-cache writes during guest termination; this
  run did not inject a guest kill or physical host power loss.
- This single focused run does not contribute an installed-host repetition
  toward the required 20–50 repeat recovery cohort.

Production remains **NO-GO**. See
[`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md) and the
[`real-host failure matrix`](../REAL_HOST_FAILURE_MATRIX.md).
