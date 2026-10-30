# Rocky Linux 9 installed-host recovery matrix

| Run detail | Value |
| --- | --- |
| Result | PASS, one matrix iteration |
| Source | `1348611198dbb72b5a7099a86f33bf2de43e6012` (clean tree) |
| Run | 2026-10-30 05:13:37–08:37:53 UTC |
| Harness duration | 12,155 seconds |
| Guest | Rocky Linux 9, x86_64, kernel `5.14.0-687.10.1.el9_8.0.1.x86_64`, 4 vCPUs, 4 GiB RAM |
| Host | KVM, QEMU 10.2.1, kernel `7.0.0-38-generic` |
| Image SHA-256 | `92c206cc6f790c61583247eefe87890f8828420662c17cacf247cec78ab4eec8` |
| Guest disk cache | `writeback` |

The native installed-host harness completed successfully. Its result file
records boot and installation as passing, with 123 smoke/matrix passes. The
log contains 122 individual `smoke passed` records followed by the successful
matrix summary. Counts by scenario:

| Scenario | Passed |
| --- | ---: |
| cPanel archive import | 26 |
| cPanel import recovery | 3 |
| Backup recovery | 22 |
| File restore recovery | 22 |
| Database restore recovery | 12 |
| Site termination recovery | 22 |
| Staging restore rollback | 2 |
| Account suspension recovery | 4 |
| Other installed-host checks (SFTP, FTPS, app lifecycle, DB lock, file helper, scheduled tasks, customer site) | 9 |

The injected interruptions recovered or rolled back at the documented operation
checkpoints. The harness reached its final `recovery matrix passed (1
iteration(s))` result. The task-safeguards smoke also emitted
`systemctl reset-failed` warnings for a transient task unit that was already
unloaded; the smoke intentionally ignores reset errors (`|| true`) before
starting the task and completed its history and failure-count assertions.

## Evidence provenance

The runner retained the environment manifest, result table, and install log at
`/var/tmp/stepanel-kvm-install-1348611/`.

| Artifact | SHA-256 |
| --- | --- |
| `environment.txt` | `e12f5d52b7445d70f5a9fd633073feb7ba2c5eb4c10b6c828792a2ac8cb472e4` |
| `results.tsv` | `4b2a381bb189b2991389a2ddfa825eacb4021925cdf72053b1645005310c3226` |
| `install.log` | `d631e2d887534e8d65028ccea54ea48f99aeee7b95782d6f924b02d0ff52d60b` |

## Limits

This is one test iteration on an older source revision, not evidence for the
current release candidate. It tests process interruption and recovery within a
running guest. QEMU writeback caching means it does not establish durability
under physical or equivalent uncushioned host power loss. It also does not
meet the documented requirement for 20–50 repetitions or replace broad
ENOSPC, database outage, supported-distribution, or application-recovery
certification. The production gate remains open.
