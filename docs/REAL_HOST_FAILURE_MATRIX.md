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
| cpmove restore | hosted | lab follow-up | lab follow-up | hosted/partial | lab follow-up |
| WordPress import | repository | lab follow-up | lab follow-up | lab follow-up | lab follow-up |
| Git deploy | hosted | lab follow-up | lab follow-up | N/A | lab follow-up |
| Backup | repository/hosted | lab follow-up | hosted/partial | lab follow-up | lab follow-up |
| Restore | repository/hosted | lab follow-up | hosted/partial | hosted/partial | lab follow-up |
| Site delete | repository/hosted | lab follow-up | lab follow-up | lab follow-up | lab follow-up |

Only mark a cell complete when the guest has rebooted or the fault was
injected on the real host and the post-boot invariant checks pass: no orphaned
journal, no partial published artifact, correct site ownership/configuration,
durable job state reconciled, and `/readyz` healthy.
