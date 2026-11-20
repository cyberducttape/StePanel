# KVM abrupt-loss operation drill

**Run:** [GitHub Actions 44662674624](https://github.com/cyberducttape/StePanel/actions/runs/44662674624)
**Source:** `64281ca87391c54038547b8b9dc002701b2ae125`
**Completed:** 2026-11-20 UTC
**Guest:** Rocky Linux 9.9, kernel `5.14.0-741.22.1.el9_9.x86_64`, QEMU/KVM
**Disk cache:** `writeback` (the harness default)

The disposable guest booted, installed StePanel, passed 19 install smoke/matrix
checks, and completed one abrupt guest-loss drill for each scenario below. The
run record and per-phase logs are in the workflow artifact
`abrupt-loss-operations-64281ca87391c54038547b8b9dc002701b2ae125`.

| Scenario | Result | Observed recovery |
|---|---:|---|
| File restore | PASS, 181 s | The interrupted job completed after reboot; state A remained intact and a fresh restore served state A. |
| Database restore | PASS, 237 s | The live database was unchanged immediately after the kill; retry completed with the original safety backup, all eight tables matched, and the application probe served the restored state. |
| Git deployment | PASS, 182 s | The interrupted job entered dead-letter with the exact original release still served; a separately queued deployment completed. |
| Backup retention | PASS, 500 s | Cleanup converged to one whole retained backup with no retired backup left; readiness recovered. |
| Control-plane write | PASS, 110 s | After killing the guest during schedule writes, the stored state was between the last acknowledged and last attempted write; readiness and control-plane integrity recovered. |

This is one pass per scenario, not the required 20–50 repetition cohort. The
guest was killed and rebooted, but QEMU used `writeback` disk caching. The host
page cache can retain acknowledged guest writes, so this does not simulate
physical host power loss or establish storage-device durability. The install
phase ran with the long lease-recovery matrix disabled; its 19 smoke/matrix
checks are not 19 repetitions of the abrupt-loss scenarios. No ENOSPC was
injected during database restore, archive import, or backup publication.

This is useful single-run failure evidence on disposable infrastructure. It
does not close Gate 5 or certify a production host.
