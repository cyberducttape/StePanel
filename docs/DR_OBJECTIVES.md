# Disaster-recovery objectives

These are the default objectives for a controlled, single-host StePanel
deployment. They are not automatic product guarantees: an operator must deploy
the required backup schedule, offsite destination, immutable retention, and
perform the drills below before claiming compliance.

| Recovery scope | RPO | RTO | Preconditions and measurement |
| --- | ---: | ---: | --- |
| Control plane, audit continuity, and runtime configuration | 24 hours | 60 minutes | A verified encrypted offsite control-plane backup, matching keys, and a blank-host restore drill. Measure from declared incident to `/readyz` plus audit verification. |
| One managed site and its registered logical database dumps | 24 hours | 4 hours | A successful scheduled verified offsite backup, sufficient staging/destination capacity, and a restore drill at representative site size. Measure to a verified, non-public staging restore or explicitly approved activation. |
| Entire host fleet member | 24 hours | 8 hours | A replacement host, release artifact, all state in [`STATE.md`](STATE.md), provider access, and a bare-host restore drill. Measure to panel readiness and one representative site verification. |

The 24-hour RPO applies only when backup schedules succeed at least daily and
the latest verified artifact is copied to the independent offsite target. The
RPO is **undefined** for a site without a successful backup in that interval.
Logical database dumps and live-file archives are classified as
`crash-consistent / logical backup`; they do not promise a transactionally
quiesced application point in time. Workloads needing a smaller RPO or
application-consistent recovery require operator-managed database PITR,
filesystem snapshots, and application quiesce hooks.

## Backup consistency classes

Each backup manifest states what its backup guarantees. `POST
/api/backups/verify` returns the same fields:

| Field | Values | Meaning |
| --- | --- | --- |
| `filesystem_consistency` | `crash-consistent` | Files are read one after another while the site keeps running. A file written during the backup can be captured mid-write, and two files can come from different moments. |
| `database_consistency` | `logical-dump`, `not-included` | Each managed database is dumped on its own: `mysqldump --single-transaction` or `pg_dump`. Each dump is a consistent point in time for InnoDB and PostgreSQL tables. MyISAM and MEMORY tables are not consistent, and separate databases come from separate moments. The database helper holds its lock for the whole dump, so a restore swap cannot interleave. Other DDL can: a schema migration running during a MySQL dump can invalidate it. |
| `application_consistency` | `web-requests-quiesced`, `not-verified` | `web-requests-quiesced` means WordPress maintenance mode held off web requests for the backup. Cron, CLI and queue workers keep running, so it is not application-quiesced. Every other workload is `not-verified`. |
| `filesystem_snapshot` | always `false` | StePanel has no LVM, ZFS or Btrfs snapshot integration, so files and databases are never captured as one atomic group. |
| `archive_verified`, `database_dump_verified` | `true`/`false` | The archive checksum and each dump were verified after writing. This proves integrity, not recoverability. |

Restore proof is separate. A backup rehearsal extracts the backup into a
disposable site and records `application_proof` when the built-in WordPress
proof ran, or `application_proof_skipped` with the reason. Only a rehearsal
with an application proof shows that the application starts from that
backup.

Choose a different method for these workloads:

- Workloads that need files and databases from one moment: use
  operator-managed filesystem snapshots, or a maintenance window with the
  application stopped.
- Workloads with schema migrations during the backup window: schedule
  backups outside it.
- Workloads on non-transactional engines: plan downtime.

## Backup size limits

These limits are enforced, not tuning defaults:

| Limit | Value | Enforced at |
| --- | --- | --- |
| One file in a site backup | 2 GiB | Backup creation fails on the first larger file |
| All entries in one site backup | 20 GiB | Backup creation fails when the running total passes it |
| Managed database dumps | Sequential, one database at a time | Backup wall-clock time grows with the sum of the site's databases |
| Database restore shadow copy | max(2 × incoming SQL dump, 1.5 × live database), plus both the helper reserve (the larger of 1 GiB and 5% of the database filesystem) and configured `STEPANEL_MIN_FREE_BYTES`; StePanel restores hold a shared SQLite capacity reservation on the configured local database filesystem | Restore is refused before loading when the estimate and applicable reserves plus outstanding StePanel reservations do not fit; unrelated host writers remain outside this ledger |

Measure tenant file and database sizes before migrating a site onto
StePanel. Sites with media libraries, artifact stores or databases near these
limits need snapshot or object-storage backups outside StePanel. Logical
dump and restore time grows with database size. Time one real restore per
large tenant, rather than inferring its RTO from smaller sites.

## Release evidence

For every release candidate, record the actual elapsed RTO and achieved RPO
for the bare-host and destructive scenarios in
[`DESTRUCTIVE_RECOVERY_LAB.md`](DESTRUCTIVE_RECOVERY_LAB.md). A result exceeding
an objective is a release exception, not a silently accepted pass. Monitor
`stepanel_backup_oldest_age_seconds`,
`stepanel_backup_schedules_without_success`, and
`stepanel_backup_consecutive_failures`; page before a scheduled backup age can
violate the declared RPO.
