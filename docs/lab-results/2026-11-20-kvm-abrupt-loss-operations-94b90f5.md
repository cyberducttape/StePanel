# KVM abrupt-loss operation drill

**Run:** [GitHub Actions 44731756198](https://github.com/cyberducttape/StePanel/actions/runs/44731756198)  
**Source:** `94b90f5e7c24a5345e365076da387e962f4b917a`  
**Completed:** 2026-11-20 UTC  
**Guest:** Rocky Linux 9.9, kernel `5.14.0-741.22.1.el9_9.x86_64`, QEMU/KVM  
**Disk cache:** `writeback`

Boot and installation passed. The guest completed one injected guest-loss
scenario for each operation below. The full result table and per-phase logs
are in the workflow artifact `abrupt-loss-operations-94b90f5e7c24a5345e365076da387e962f4b917a`.

| Scenario | Result | Observed recovery |
|---|---:|---|
| File restore | PASS, 203 s | The interrupted restore job completed after restart; state A remained intact and a fresh restore served state A. |
| Database restore | PASS, 245 s | The database was unchanged immediately after the kill; retry reused the first safety backup and all eight tables matched it. |
| Git deployment | PASS, 200 s | The interrupted job reached dead-letter while the original release remained served; a fresh deployment completed. |
| Backup retention | PASS, 497 s | Retention converged to one whole backup with no retired backup left. |
| Control-plane write | PASS, 115 s | Recovery retained the last acknowledged schedule write and passed control-plane integrity/readiness checks. |

This is one injected guest kill and reboot per scenario. QEMU used
`writeback`, so host page cache may preserve writes; this does not simulate
physical host power loss or prove device-level durability. The install phase
reported 20 smoke/matrix checks, not 20 repetitions of these abrupt-loss
scenarios. ENOSPC during database restore, archive import, and backup
publication was not tested. The workflow ran on source `94b90f5e`, not the
current release candidate; subsequent differences include the capacity
diagnostic harness, the file-read limiter, and workflow configuration. No
exact-source certification claim is made.
