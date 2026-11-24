# Rocky Linux 9 KVM abrupt-loss operation drills — 2026-11-24

## Result

The KVM workflow [45305278135](https://github.com/cyberducttape/StePanel/actions/runs/45305278135)
completed successfully on source
[`f6c9e9ab`](https://github.com/cyberducttape/StePanel/commit/f6c9e9ab).
The run booted and installed StePanel on a Rocky Linux 9 KVM guest, then
interrupted the guest during selected operations and checked the recovered
host and application state.

| Scenario | Result | Recorded evidence |
|---|---:|---|
| Installed boot and smoke | Pass | 23 install and matrix checks passed. |
| File restore | Pass | Interrupted restore converged with state A intact; a fresh restore served state A. |
| Database restore | Pass | Live database remained untouched after the kill; retry reused the first safety backup and all 8 tables matched. |
| Git deployment | Pass | Interrupted deploy ended dead-letter with the original release served; fresh deploy completed. |
| Backup retention | Pass | Retention converged to one whole backup with no retired backup left. |
| Control-plane acknowledged write | Pass | State retained write 426 after 426 acknowledged and 426 attempted; integrity/readiness recovered. |
| Interrupted backup | Pass | Earlier backup restored state A; a fresh backup completed. |
| Database restore ENOSPC | Pass | Oversized restore was refused or stopped above the reserve; live database stayed unchanged, shadows were removed, and a later restore succeeded. |
| Operator alerts | Pass | STARTTLS delivery, outage visibility, and retry after recovery passed. |

The retained artifact
[`abrupt-loss-operations-f6c9e9ab2720b79584af41dc891281454d3ed550`](https://github.com/cyberducttape/StePanel/actions/runs/45305278135)
contains the scenario results, guest console, install log, ENOSPC log, and
alert delivery log. QEMU used a Rocky Linux 9 guest with ext4 user and project
quotas enabled.

## Limits and source relationship

This is one guest-loss iteration. It is not physical host or storage-controller
power-loss evidence, and it does not satisfy the required repeated recovery
cohort. Guest loss used QEMU; it cannot establish behavior after actual host
power removal.

The tested source is `f6c9e9ab`. Current main is `00004e75`; since the test,
main has changed privileged broker scheduling, file uploads and downloads,
installer behavior, account and site flows, durable-job error handling, and
other code. The exact-source integration workflow
[45313583538](https://github.com/cyberducttape/StePanel/actions/runs/45313583538)
passed on `47386470`, whose only difference from current main is a
documentation update. That integration result covers Go tests and race tests,
and the seeded browser E2E job passed. It does not replace a current-source
installed-host recovery run.

The earlier integration workflow
[45305811748](https://github.com/cyberducttape/StePanel/actions/runs/45305811748)
was cancelled by a newer push while running PostgreSQL restore verification;
its browser E2E job passed, but the workflow is not counted as a passing
integration result. The later `45313583538` run passed.

Production remains **NO-GO** pending exact-candidate installed-host recovery,
repeated recovery evidence, physical-loss testing, supported-platform and
capacity certification, and the other open gates in
[`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md).
