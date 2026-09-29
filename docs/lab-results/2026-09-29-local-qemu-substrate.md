# Local QEMU Substrate Check — 2026-09-29

This is supporting evidence for Gate 5 Phase 7 only. It is not a release
approval result and does not replace an installed StePanel workflow drill.

## Environment

- QEMU: 10.2.1
- KVM: `/dev/kvm` present
- Guest image: Rocky Linux 9 GenericCloud Base
- Image SHA-256: `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8`
- Guest kernel: `5.14.0-687.10.1.el9_8.0.1.x86_64`
- Guest root filesystem: XFS, 8.9 GiB visible, 12% used after boot

## Procedure and result

1. Created a disposable full-size qcow2 overlay and NoCloud seed.
2. Booted Rocky under QEMU/KVM with a forwarded SSH port.
3. Verified cloud-init completion, SSH access, `systemctl` availability, and
   the mounted root filesystem.
4. Sent `SIGKILL` to the QEMU process without guest shutdown.
5. Relaunched the same overlay and verified SSH access, cloud-init marker
   persistence, kernel identity, and a healthy root filesystem.

Result: PASS for guest boot and abrupt QEMU-process restart recovery.

## Explicit limits

This run did not install or execute StePanel inside the guest. It therefore
does not prove recovery of site publication, database restore, ENOSPC handling,
host power loss, 100-run determinism, or the recovery-time SLA. Those release
gates remain open.
