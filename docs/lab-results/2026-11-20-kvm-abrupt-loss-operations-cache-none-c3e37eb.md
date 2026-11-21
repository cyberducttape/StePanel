# KVM abrupt-loss operation drill (host page cache bypassed)

**Run:** [GitHub Actions 44735170833](https://github.com/cyberducttape/StePanel/actions/runs/44735170833)  
**Source:** `c3e37ebc47bb08f485d855449757c7fd5524d8de`  
**Completed:** 2026-11-20 13:30 UTC  
**Guest:** Rocky Linux 9.9, kernel `5.14.0-741.22.1.el9_9.x86_64`, QEMU/KVM  
**Disk cache:** `none` (`VM_DISK_CACHE: none` in `.github/workflows/abrupt-loss-operations.yml`)

Boot and installation passed (installation reported 20 smoke/matrix checks).
The guest completed one injected guest-loss scenario for each operation below.
The result table and per-phase logs are in the workflow artifact
`abrupt-loss-operations-c3e37ebc47bb08f485d855449757c7fd5524d8de`.

| Scenario | Result | Observed recovery |
|---|---:|---|
| File restore | PASS, 201 s | The interrupted restore job completed after restart; state A remained intact and a fresh restore served state A. |
| Database restore | PASS, 235 s | The database was unchanged immediately after the kill; retry reused the first safety backup and all eight tables matched it. |
| Git deployment | PASS, 182 s | The interrupted job reached dead-letter while the original release remained served; a fresh deployment completed. |
| Backup retention | PASS, 501 s | Retention converged to one whole backup with no retired backup left. |
| Control-plane write | PASS, 80 s | The schedule held write 679 after 679 acknowledged and 680 attempted writes; control-plane integrity and readiness checks passed. |

## What this adds over the `writeback` run

The earlier [writeback run](./2026-11-20-kvm-abrupt-loss-operations-94b90f5.md)
could have been helped by the runner's page cache keeping acknowledged guest
writes after QEMU was killed. With `cache=none`, QEMU opens the disk images
with `O_DIRECT`, so writes the guest acknowledged had already left the host
page cache. The same five outcomes held. The control-plane scenario kept
exactly the last acknowledged write.

## Observations

- During guest boot, systemd reported `Failed to start Enable File System
  Quotas`. Quota enforcement still worked during installation: the release
  staging smoke hit `Disk quota exceeded` as expected, and the customer site
  smoke reported `starter quota enforced`. The unit failure is not an
  operation-recovery failure. It should still be checked on a supported host
  image, because operators will see it in boot logs.

## Limits

This is one injected guest kill and reboot per scenario. `cache=none`
bypasses the host page cache, but the backing disk's own volatile write cache
and the runner's storage stack are still involved. This is not physical
host power-loss evidence, and it does not prove device-level durability.
It did not test ENOSPC during database restore, archive import or backup
publication. The run used source `c3e37ebc`. Later non-documentation changes
include the bounded site file-read limiter (`site_files.go`), the capacity
diagnostic harness and workflow-bound checks, so this run is not
exact-source certification for current `main`.
