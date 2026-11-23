# Rocky Linux 9 KVM abrupt-loss drills — 2026-11-23

## Result

The uncushioned KVM workflow [45150861029](https://github.com/cyberducttape/StePanel/actions/runs/45150861029)
completed successfully on source
[`edd11d472dd856a4da1997118db34fc9b5950c2c`](https://github.com/cyberducttape/StePanel/commit/edd11d472dd856a4da1997118db34fc9b5950c2c).
It booted and installed on a Rocky Linux 9 KVM guest, then killed the guest
with QEMU disk cache set to `none` during the selected workflows. The workflow
recorded 23 installed-host smoke/matrix passes.

| Scenario | Result | Recovery evidence |
|---|---:|---|
| File restore | Pass | Interrupted job converged with original state A intact; a fresh restore served state A. |
| Database restore | Pass | Live database stayed untouched after kill; retry reused the first safety backup and all 8 tables matched. |
| Git deployment | Pass | Interrupted job ended dead-letter with original release served; a fresh deploy completed. |
| Backup retention | Pass | Retention converged after guest loss, leaving one complete backup and no retired backup. |
| Control-plane acknowledged write | Pass | State held write 386 after 386 acknowledged and 387 attempted; integrity/readiness recovered. |
| Interrupted backup | Pass | Earlier backup restored state A; a fresh backup completed after recovery. |
| Database restore ENOSPC | Pass | Dedicated database restore ENOSPC smoke passed. |
| Operator alerts | Pass | STARTTLS delivery, outage visibility, and retry after recovery passed. |

The report and logs are retained in artifact
[`abrupt-loss-operations-edd11d472dd856a4da1997118db34fc9b5950c2c`](https://github.com/cyberducttape/StePanel/actions/runs/45150861029/artifacts/14808189871).

## Limits and source relationship

This is one KVM guest-loss iteration, not physical host or storage-controller
power-loss evidence, and not a repeated recovery cohort. The tested source
predates the release-evidence collector change. Comparing the exact commits
shows the only non-documentation source changes are `internal/evidence/evidence.go`
and its tests; the site, database, backup, installer, and broker paths exercised
by this workflow are unchanged. An exact-current-source KVM rerun was dispatched
as [45158813904](https://github.com/cyberducttape/StePanel/actions/runs/45158813904)
and remains the current-source evidence gate until its artifact is reviewed.
