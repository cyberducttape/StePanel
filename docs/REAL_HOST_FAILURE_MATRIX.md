# Real-host failure matrix

Repository SIGKILL and tmpfs ENOSPC tests are necessary but do not prove
recovery across a reboot or host power loss. The disposable systemd lab is the
authoritative place for those tests. Run the installed-host matrix with a
unique prefix and retain the service, journal, readiness, database, and audit
logs with the result:

```sh
RECOVERY_MATRIX_PREFIX=gate5-$(date +%s) \
RECOVERY_MATRIX_FULL=1 \
RECOVERY_MATRIX_REPEATS=20 \
bash deploy/lab/recovery-matrix-smoke.sh
```

The matrix script rejects values above 50 and gives every iteration isolated
site/account names. `RECOVERY_MATRIX_REPEATS=1` is the normal smoke default;
20–50 repetitions are intended for destructive soak runs.

| Operation | SIGKILL | Reboot/power-loss | ENOSPC | DB failure | Broker kill |
| --- | --- | --- | --- | --- | --- |
| Site creation | repository/hosted | lab follow-up | lab follow-up | lab follow-up | lab follow-up |
| cpmove restore | hosted/KVM | lab follow-up | lab follow-up | hosted/partial | lab follow-up |
| WordPress import | repository | lab follow-up | lab follow-up | lab follow-up | lab follow-up |
| Git deploy | hosted | lab follow-up | lab follow-up | N/A | lab follow-up |
| Backup | repository/hosted/KVM | KVM guest kill | KVM | lab follow-up | lab follow-up |
| Restore | repository/hosted/KVM | lab follow-up | hosted/partial/KVM* | hosted/partial | lab follow-up |
| Site delete | repository/hosted/KVM | lab follow-up | lab follow-up | lab follow-up | lab follow-up |

The installed WordPress recovery-proof smoke also injects a bounded MariaDB
service outage against its disposable application and verifies that the
database-backed HTTP probe recovers after restart. It stops the root broker
and verifies `/livez` remains available while `/readyz` reports the broker as
unavailable, then checks readiness recovery. These checks cover service
degradation and restart behavior only; they do not claim recovery from a
control-plane database outage or a broker killed during an in-flight mutation,
so the corresponding full workflow matrix cells above remain open.

The installed-host smoke includes a separate control-plane SQLite outage drill:
it stops panel, worker, and broker services; preserves the database plus any
WAL/SHM/journal sidecars; makes the configured database path unavailable; and
requires readiness not to return HTTP 200. It then restores the exact files
and requires all services and readiness to recover. This is a bounded outage
and restart test, not evidence for corruption, ENOSPC, or failure during an
in-flight host mutation.

**KVM** marks a pass in the single
[2026-10-09 Rocky Linux 9 KVM certification run](lab-results/2026-10-09-rocky9-kvm-certification.md)
(SELinux enforcing, full matrix at `RECOVERY_MATRIX_REPEATS=1`). "KVM guest
kill" is a QEMU `SIGKILL` of the whole guest mid-operation, not physical power
loss. No cell is complete until it has passed at the 20–50 repetition level.
Run the same harness with:

```sh
deploy/lab/local-kvm-certification.sh Rocky-9-GenericCloud.latest.x86_64.qcow2
```

**KVM*** records the focused 2026-10-11 file-restore ENOSPC drill on a
dedicated 85 MiB ext4 filesystem. It verifies preservation of existing site
files, cleanup of restore staging, and successful retry after space is freed;
it does not cover database restore or whole-host ENOSPC. See the
[focused drill record](lab-results/2026-10-11-real-enospc-restore.md).

Only mark a cell complete when the guest has rebooted or the fault was
injected on the real host and the post-boot invariant checks pass: no orphaned
journal, no partial published artifact, correct site ownership/configuration,
durable job state reconciled, and `/readyz` healthy.
