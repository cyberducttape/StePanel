# Rocky Linux 9 KVM abrupt-loss follow-up — 2026-11-23

## Result

The cache-disabled KVM workflow [45158813904](https://github.com/cyberducttape/StePanel/actions/runs/45158813904) completed successfully on source `4e9ca58a1660ab0fc3751aa1a9b0fae1cdb781f0`. Its installed-host setup recorded 23 smoke/matrix passes. The workflow then completed these guest-loss and recovery scenarios:

| Scenario | Result |
| --- | --- |
| File restore | Interrupted restore converged with original state A intact; a fresh restore served state A. |
| Database restore | Live database remained untouched after guest kill; retry reused the original safety backup and all eight tables matched. |
| Git deployment | Interrupted job ended dead-letter with the original release served; a fresh deployment completed. |
| Backup retention | Retention converged to one complete backup with no retired backup remaining. |
| Control-plane acknowledged write | Database retained the last acknowledged write, rejected the unacknowledged attempt, and passed integrity/readiness checks. |
| Interrupted backup | Earlier backup restored state A, then a fresh backup completed. |
| Database restore ENOSPC | Admission/load stopped safely and a subsequent restore succeeded. |
| Operator alerts | STARTTLS alert delivery, outage visibility, and retry after recovery passed. |

Installed WordPress HTTPS/database recovery also passed with an RTO of **8,831 ms**. The VM uses QEMU cache mode `none`; the workflow kills the guest process and boots the same disks. It is one guest-loss pass, not physical host power-loss evidence.

The retained [run artifacts](https://github.com/cyberducttape/StePanel/actions/runs/45158813904) contain `results.tsv`, install logs, scenario logs, database ENOSPC output, and alert-delivery output.

## Source relationship and limits

This source was current for the operation drill when run. The later product source `5da789bb` changed setup, installer, and quota-preflight paths (among others); its packaged install is separately covered by [candidate run 45202075337](https://github.com/cyberducttape/StePanel/actions/runs/45202075337) and the [quota-enabled ext4 run 45203221241](https://github.com/cyberducttape/StePanel/actions/runs/45203221241). A new KVM run on current `main` is tracked as [45305278135](https://github.com/cyberducttape/StePanel/actions/runs/45305278135).

This remains a single iteration. It does not satisfy repeated recovery certification, physical host power loss, XFS quota evidence, or the other open production gates.
