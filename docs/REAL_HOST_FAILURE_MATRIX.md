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

The GitHub installed-host workflow is sharded because a sequential 20-repeat
job exceeds the hosted runner's six-hour limit. A recovery shard accepts one or
two iterations; two-iteration shards require unique IDs so Actions concurrency
does not replace pending evidence. For a 20-iteration cohort, dispatch ten
shards against the same `main` commit. Set `repeats=50` for 25 shards:

```sh
set -euo pipefail
repeats=20
(( repeats >= 20 && repeats <= 50 && repeats % 2 == 0 ))
shard_count=$((repeats / 2))
cohort="gate5-$(date -u +%Y%m%dT%H%M%S)-$(git rev-parse --short=7 HEAD)"
for shard in $(seq -w 1 "$shard_count"); do
  gh workflow run install-smoke.yml --ref main \
    -f full_recovery_matrix=true \
    -f recovery_matrix_repeats=2 \
    -f recovery_matrix_shard="$cohort-$shard"
done
```

Record every workflow run ID and verify that all six OS/web-server jobs passed
for every shard before aggregating the cohort. All shards must use the same
source SHA; a source change starts a new cohort. A partial or timed-out shard
contributes no iteration to the release gate.

| Operation | SIGKILL | Reboot/power-loss | ENOSPC | DB failure | Broker kill |
| --- | --- | --- | --- | --- | --- |
| Site creation | repository/hosted | lab follow-up | lab follow-up | lab follow-up | lab follow-up |
| cpmove restore | hosted/KVM | lab follow-up | lab follow-up | hosted/partial | lab follow-up |
| WordPress import | repository | lab follow-up | lab follow-up | lab follow-up | lab follow-up |
| Git deploy | hosted | lab follow-up | lab follow-up | N/A | lab follow-up |
| Backup | repository/hosted/KVM | KVM guest kill | KVM | lab follow-up | lab follow-up |
| Restore | repository/hosted/KVM | lab follow-up | hosted/partial/KVM* | hosted/partial | lab follow-up |
| Site delete | repository/hosted/KVM | lab follow-up | lab follow-up | lab follow-up | lab follow-up |

## Disk exhaustion outcome contract

The table above records evidence. This section defines the outcome each
storage-heavy operation must produce when its filesystem fills, so ENOSPC
lab runs have an invariant to check. A full disk fails the job attempt.
Durable jobs retry with exponential backoff up to their attempt limit (three
for backups and imports, two for restores) and are then dead-lettered.
Retries usually hit the same full disk, so the operator frees space and
resubmits.

| Operation | Live data after ENOSPC | Partial work | Operator action |
| --- | --- | --- | --- |
| Backup creation | Unchanged; earlier backups intact | Staging directory removed; nothing published (publish is a rename after the manifest is written) | Free space on the backup filesystem and retry |
| File restore | Unchanged; existing site files are kept | Import stage and SiteManager staging discarded; the recovery transaction rolls back | Free space and retry |
| Offsite publication | Local backup unaffected | Remote staging prefix purged, or removed by the periodic staging sweep; no completion marker written | Free remote capacity and retry |
| cPanel, WordPress and archive import | No existing site or database is touched: imports create a new site and a freshly provisioned database | The site recovery transaction rolls back; the helper drops the new database and its ownership record | Free space and re-run the import |
| Git deploy | Live release unchanged until activation, which is the commit point | Release staging discarded; its capacity reservation released | Free space and redeploy |
| Managed database restore (`database` and `offsite-database` modes) | **Partially restored.** The dump is loaded into the live database in place, and MySQL/MariaDB DDL auto-commits, so tables already processed are replaced and the rest are old or incomplete. The site stays online. | Restore stage removed. A full site and database safety backup taken immediately before the restore is kept, and the job error names it | Free space, then restore the safety backup named in the error. A failure once the load has started is never retried automatically. A job re-run after a crash or lost lease refuses to start, and its error names the first attempt's safety backup |

The managed database restore is the one operation where a mid-operation
failure leaves live customer data inconsistent. This is an open P0 item in
[V1_PRODUCTION_GATES.md](V1_PRODUCTION_GATES.md). Closing it means restoring
into a shadow database and swapping it in atomically, with admission for the
second copy. The same exposure applies to any failure during the load, not
only ENOSPC: a database server error, a killed helper or a lost lease. Until
then, a `database-restore-<job>.json` marker in the recovery root records the
start of each load. It keeps a retry from taking a new safety backup of the
partial database and naming that backup in its error. The repository kill
drill `restore:database` fires before this marker, so it still retries and
completes.

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
`abrupt-loss-operation-drill.sh` adds mid-operation guest kills for file
restore, database restore, Git activation, and control-plane writes, with
post-boot checks of customer data and HTTP. It has no recorded runs yet, so
those Reboot/power-loss cells remain open.
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
