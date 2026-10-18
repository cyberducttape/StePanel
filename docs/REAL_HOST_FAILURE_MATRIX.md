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
| Managed database restore (`database` and `offsite-database` modes) | Unchanged. The dump loads into a shadow database; the live database is replaced only by the swap after a successful load | Shadow database dropped and its `pending-restore` record removed. The full site and database safety backup taken before the restore is kept, and the job error names it | Free space on the database filesystem and retry. The shadow needs room for a second copy of the database |

The managed database restore loads into a shadow database and swaps it in.
The swap is the commit point:

- A failure or kill before it leaves the live database unchanged. The helper
  reports `the live database was not changed`, and startup `reconcile` drops
  a shadow whose helper was killed.
- After it, every step rolls forward. The next `dump`, `restore-dump` or
  startup `reconcile` of the database finishes an interrupted swap, so a
  safety backup or a retry never sees a half-swapped database.

MySQL/MariaDB move every base table in one `RENAME TABLE`, which is atomic.
On MySQL 8.0 and MariaDB 10.6 or later it is also crash-safe for InnoDB.
Views, triggers, routines and events cannot change schema, so they are
dropped and recreated from definitions exported before the swap; queries
that use them can fail for that moment. A long transaction on the live
database holds the swap for up to 60 seconds per attempt, and a stopped swap
is retried twice. PostgreSQL renames both databases in one transaction after
ending the database's sessions, so the site reconnects once.

A `database-restore-<job>.json` marker in the recovery root records each
attempt's safety backup. A retried job reuses that backup instead of taking a
new one, because the interrupted swap may already have finished and a new
backup would then hold the restored data.

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
