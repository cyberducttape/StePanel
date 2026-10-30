# Rocky Linux 9 KVM install run interrupted by host OOM — 2026-10-30

This run is incomplete and closes no release gate. The host kernel's OOM log
confirms that it killed QEMU during the install phase. This is a host-capacity
failure, not evidence that StePanel failed inside the guest.

## Environment and result

| Item | Value |
| --- | --- |
| Application source | `6a77e3848cc8fa18a1cc130a6faf555a13b28f77` (clean) |
| Harness | `deploy/lab/local-kvm-certification.sh` |
| Guest image | Rocky Linux 9 GenericCloud, SHA-256 `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8` |
| Host | QEMU 10.2.1/KVM, Linux `7.0.0-38-generic` |
| Guest allocation | 4 vCPU, 4096 MiB RAM |
| Host reserve requested | 512 MiB |
| Disk cache | `none` |
| Started | 2026-10-30 10:04:25 UTC |
| Result | Boot passed in 100 seconds; install phase terminated after 306 seconds |

The harness result table records `boot PASS` and `install FAIL`. Its install
log says the guest exited while the install job was running. The host kernel
log at 2026-10-30 10:11:23 UTC records the global OOM killer terminating
`qemu-system-x86` (PID 1172688), with approximately 2.19 GiB resident anonymous
memory. The recorded host state after the event showed 510 MiB of 511 MiB swap
in use. No guest-side StePanel failure was established.

The retained raw evidence is in
`/var/tmp/stepanel-kvm-current-6a77e38/`: `environment.txt`, `results.tsv`,
`console.log`, and the harness-generated `install.log`. The directory also
contains disposable guest disks and a generated SSH key; those artifacts are
not included in this repository report.

## Harness response

The install phase now rejects `VM_HOST_RESERVE_MB` values below 2048 MiB before
creating the run directory or booting the guest. A preflight check using the
same 4096 MiB guest and 512 MiB reserve exited with the expected explanation
and created no run directory. This prevents the known low-reserve setup from
repeating; it cannot guarantee against unrelated host memory consumers during
a long run.

## Gate status

This run provides no installed-host acceptance result. Retry only when the
workstation has the required guest-plus-reserve headroom and sufficient swap
headroom, and preserve the per-run environment and kernel OOM evidence if the
host still terminates QEMU. Power-loss, repeated recovery, broad ENOSPC, and
production capacity gates remain open.
