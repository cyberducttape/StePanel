# Rocky Linux 9 KVM recovery attempt interrupted by host OOM

**Date:** 2026-10-31
**Source:** `77c355a1f04771cce316284d9323754d1cab9480`
**Result:** Interrupted; not a completed recovery repetition
**Run directory:** `/var/tmp/stepanel-kvm-cert-20261031-77c355a`

## Environment

- Rocky Linux GenericCloud image, SHA-256
  `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8`
- QEMU 10.2.1 on host kernel `7.0.0-38-generic`
- Guest kernel `5.14.0-687.10.1.el9_8.0.1.x86_64`
- 4 GiB guest memory, four vCPUs, `cache=none`
- The installed-host fixture used ext4 user/project quotas and separate
  disposable filesystems for `/var/www`, home directories, and ENOSPC tests.

## Outcome

The harness ran for 12,637 seconds. The host OOM killer terminated
`qemu-system-x86`; its process had approximately 2.5 GiB resident memory in
the kernel OOM record. The guest console ended with a CPU soft-lockup warning.
The host no longer had enough available memory to safely relaunch the 4 GiB
guest, so the run was left interrupted with its qcow2 and log artifacts
preserved.

The guest run log confirms recovery checks through the `terminate:services`
checkpoint. The next `terminate:site-state` scenario completed its import,
backup, and restore setup, but QEMU exited while that termination job was in
progress. No completion result for that checkpoint or any later checkpoint
was recorded. `results.tsv` therefore contains only the boot pass; the install
phase aggregates the long-running matrix and correctly records failure.

The failed harness result is an environment resource interruption, not a
product recovery pass or a demonstrated product defect. It is excluded from
the 20–50 repetition release gate. The run directory contains `environment.txt`,
`results.tsv`, `install.log`, and `console.log`; the guest disk is retained for
read-only investigation or resumption when sufficient host memory is
available. No live hosting server was used.

## Follow-up

- Preserve the host OOM record and this partial evidence; do not infer success
  for the in-progress termination point.
- Resume or repeat only when the host meets the harness memory preflight with
  the full guest allocation and host reserve.
- Continue the independent hosted six-platform recovery cohort; its first
  complete repetition is reported separately and does not replace this run.
