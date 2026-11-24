# Rocky Linux 9 KVM abrupt-loss operation drills — 2026-11-24

## Result

The current-product-source KVM workflow
[45332417894](https://github.com/cyberducttape/StePanel/actions/runs/45332417894)
completed successfully on source
[`17b62c6afb99e4427d707f149be9948f16021978`](https://github.com/cyberducttape/StePanel/commit/17b62c6afb99e4427d707f149be9948f16021978).
It booted a Rocky Linux 9 guest, installed StePanel, killed the whole guest
during selected operations with QEMU disk cache disabled, rebooted it, and
verified the resulting application/control-plane state.

| Scenario | Result | Recorded evidence |
|---|---:|---|
| Guest boot and install | Pass | Rocky kernel `5.14.0-741.22.1.el9_9.x86_64`; install and smoke/matrix phase completed with 23 checks. |
| File restore interruption | Pass | Interrupted restore converged with state A intact; a fresh restore served state A. |
| Database restore interruption | Pass | Database was untouched after guest loss; retry reused the original safety backup and all 8 tables matched it. |
| Git deployment interruption | Pass | Job reached dead-letter while the original release remained served; a fresh deploy completed. |
| Backup retention interruption | Pass | Retention converged to one complete backup with no retired backup remaining. |
| Control-plane acknowledged write | Pass | Write 672 survived after 671 writes were acknowledged and 672 attempted; integrity and readiness recovered. |
| Backup publication interruption | Pass | Two verified backups remained; the earlier backup restored state A and a fresh backup completed. |
| Database filesystem ENOSPC | Pass | Restore admission/watchdog preserved the live database, removed shadow state, and allowed a subsequent restore. |
| Operator alert delivery | Pass | STARTTLS delivery, outage visibility, and retry after recovery passed. |
| Installed WordPress application proof | Pass | HTTPS and database-backed probe passed; measured RTO was **9,138 ms**. |

The full installed-host smoke also passed the cPanel import and interrupted
cPanel recovery drills, database client-command safety checks, cross-process
database lock overlap, worker kill/recovery during backup, file restore,
database restore, termination, Git activation, root broker interruption,
account suspension, and customer starter quota enforcement.

The retained GitHub artifact is
[`abrupt-loss-operations-17b62c6afb99e4427d707f149be9948f16021978`](https://github.com/cyberducttape/StePanel/actions/runs/45332417894)
(artifact ID `14895707158`). The downloaded `results.tsv` had SHA-256
`f6a8389b1860b4cd6635d9363ea4afeb15f3d72ad2ced06826cb47b0076a867d`.

## Limits and follow-up

This is one QEMU guest-loss iteration with `cache=none`. It is not physical
host/storage-controller power-loss evidence, and it does not satisfy the
20–50-repeat requirement. It includes database-filesystem ENOSPC only; the
separate backup/file-restore ENOSPC phase was added to a follow-up run and is
not represented here. Import ENOSPC and broader host exhaustion remain open.

The run artifact did not include the generated `environment.txt` manifest, so
the runner QEMU version, exact downloaded image digest, guest resource sizing,
and run environment record were not retained with this result. The workflow
now prints and uploads that manifest for subsequent runs.

The guest console reports that `quotaon.service` failed during boot. The
`/var/www` and `/home` ext4 mounts did report journalled quota mode; the Git
staging over-limit write failed with `Disk quota exceeded`, and the customer
starter quota smoke passed. The discrepancy between effective per-mount quota
enforcement and the generic service failure remains a lab follow-up and is not
counted as a clean quota-service result.

The tested commit is current product code, but its workflow predates the
follow-up addition of backup/file-restore ENOSPC and environment-manifest
retention. Current `main` differs in test harness and documentation changes;
no production source changes are implied by this result. See
[`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md) for the open release
criteria. Production remains **NO-GO**.
