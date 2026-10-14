# Rocky Linux 9 KVM certification attempt — 2026-10-13

This attempt did not complete and closes no release gate. The guest booted and
the install/recovery smoke began, but the workstation's Linux OOM killer ended
QEMU. This was a host-capacity failure, not evidence of a StePanel guest
failure.

## Environment and outcome

| Item | Value |
| --- | --- |
| Harness source | `deploy/lab/local-kvm-certification.sh` |
| Application commit | `94e2b9d0adbfa1a02609e7dcd724149d9d6c15dd` |
| Guest image | Rocky Linux 9 GenericCloud, SHA-256 `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8` (matched the image digest recorded in the 2026-10-12 certification) |
| Host | QEMU 10.2.1/KVM, Linux `7.0.0-38-generic` |
| Guest resources | 4 vCPU, 3072 MiB RAM; quota-enabled ext4 `/var/www`; dedicated 96 MiB ENOSPC filesystem |
| Start | 2026-10-14 01:25 UTC |
| Result | INCOMPLETE — host OOM killed QEMU before the install phase completed |

The boot phase passed, including the quota mount check. During the subsequent
install smoke, SSH observations showed cPanel activation recovery, database
lock overlap, backup recovery after a worker kill, file-restore recovery after
a worker kill, and database-restore recovery after a worker kill passing. The
guest then became unreachable before those checks could be collected into the
harness result table; they are partial observations, not certified results.
Runner, whole-guest abrupt-loss, and ENOSPC phases did not run.

The host kernel log records the OOM killer terminating QEMU PID `3944264` at
2026-10-13 19:40 local time. QEMU had approximately 2.1 GiB resident memory
when killed, and host swap was exhausted. No StePanel process or guest error
was identified as the trigger.

## Harness correction

The interrupted run exposed that `guest_job` could continue polling for its
full timeout after the VM process had died. The harness now verifies that the
run-specific QEMU process is still alive on each poll and returns promptly if
it has exited. Cleanup also checks the process command line against the
run-specific PID file before signaling, avoiding a signal to an unrelated
process if the PID has been reused. Fresh runs now require configurable
`MemAvailable` headroom (`VM_MEMORY_MB` plus `VM_HOST_RESERVE_MB`, default
2048 MiB) before allocating a guest, so this workstation should reject the
same attempt before booting under its current memory pressure.

## Artifacts and remaining work

Local run directory: `/var/tmp/stepanel-kvm-cert-94e2b9d/` (not committed; it
contains only disposable guest images, test logs, and a generated SSH key).
The primary evidence is `environment.txt`, `results.tsv`, `console.log`, and
the host kernel OOM log. The guest root disk remains available for optional
read-only forensic inspection.

Retry only when the host has adequate uncommitted memory and swap headroom.
This attempt does not change the documented status of power-loss, repeated
recovery, broader ENOSPC, or installed-host release gates.
