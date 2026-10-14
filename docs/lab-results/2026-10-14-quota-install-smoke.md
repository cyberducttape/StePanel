# Quota-enabled Ubuntu install and recovery smoke — 2026-10-14

| Field | Result |
|---|---|
| Tested commit | `d553838427c63f122a2b9f7b1027a42f28b7ccca` |
| Hosted run | [Quota-enabled production install smoke](https://github.com/cyberducttape/StePanel/actions/runs/38508198669/job/115670741540) — PASS |
| Guest | Ubuntu 24.04 cloud image; QEMU TCG, 2 vCPU, 4 GiB RAM |
| Filesystems | 12 GiB guest root; dedicated 2 GiB ext4 `/var/www` with user quotas; dedicated 96 MiB ext4 ENOSPC volume |
| Scope | One disposable-guest run; not repeated certification |

The run verified installation against an actual quota-enabled `/var/www`, then
exercised the installed panel and services. The log records successful SFTP,
cpmove import and interrupted-import recovery, overlapping independent
database locks, interrupted backup/restore/database-restore/termination,
WordPress application recovery, route-deletion replay, account-suspension
recovery, deployment rollback, app lifecycle, rootless runner permissions,
and release-pipeline build/activation. The populated WordPress recovery proof
reported `rto_ms=81004`.

After these checks, the harness sent `SIGKILL` to the QEMU process while keeping
the guest disks, restarted the same guest, and verified service activity and
the panel readiness endpoint. It then passed both
`TestBackupRecoversFromRealENOSPC` (12.27 s) and
`TestRestoreFilesRecoversFromRealENOSPC` (16.93 s) against the dedicated ext4
volume.

This is evidence for recovery after abrupt loss of a disposable guest process,
not physical host power loss or loss of the host's storage cache. The ENOSPC
coverage is limited to backup creation and file-restore extraction; publication,
import, and database-restore ENOSPC remain untested. This is one run, not the
required 20–50 repetitions. It does not certify capacity or a production
support envelope.
