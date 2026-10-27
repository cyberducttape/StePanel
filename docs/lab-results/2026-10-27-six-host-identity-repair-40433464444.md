# Six-host identity repair and recovery smoke — 2026-10-27

**Result: PASS on all six installed-host combinations.**

- Application source: [`3bb1e6bf2ff873def292ab2d406d5ceebd7b96f7`](https://github.com/cyberducttape/StePanel/commit/3bb1e6bf2ff873def292ab2d406d5ceebd7b96f7)
- Workflow run: [Installation smoke 40433464444](https://github.com/cyberducttape/StePanel/actions/runs/40433464444)
- Repetitions: one recovery-matrix iteration per host
- Database: MariaDB
- Runner: GitHub-hosted Ubuntu runner running disposable systemd containers; this is not a physical-host or dedicated-VM power-loss certification

The workflow exercised the actual native installer, recovery drills, a populated
WordPress restore proof, and a real legacy site-identity collision repair on
each installed host. The WordPress proof restored the database and files,
started the installed web stack, and verified the application over HTTP.
Identity repair reassigned both colliding sites to unique accounts and
completed host reconciliation on every combination.

| Host | WordPress recovery RTO | Recovery matrix | Identity repair |
| --- | ---: | --- | --- |
| Ubuntu 24.04 + Apache | 5.920 s | Passed, 1 iteration | Passed, 2 sites |
| Ubuntu 24.04 + Caddy | 16.400 s | Passed, 1 iteration | Passed, 2 sites |
| AlmaLinux 9 + Apache | 6.415 s | Passed, 1 iteration | Passed, 2 sites |
| AlmaLinux 9 + Caddy | 7.112 s | Passed, 1 iteration | Passed, 2 sites |
| Rocky Linux 9 + Apache | 6.399 s | Passed, 1 iteration | Passed, 2 sites |
| Rocky Linux 9 + Caddy | 5.419 s | Passed, 1 iteration | Passed, 2 sites |

The run follows an earlier failed matrix on source `46dc6e51`. That failure
found two production-path defects: the installer did not guarantee `pgrep`
for identity preflight, and repair omitted the reassigned username when
calling the site resource helper. Source `3bb1e6b` installs the distro's
`procps`/`procps-ng` package and passes the username. The six successful jobs
show both corrections working through the installed workflow.

This is six successful host runs, not six repeats of the complete Gate 5
cohort. The same-source 20-repeat recovery cohort was dispatched separately
as [run 40436448192](https://github.com/cyberducttape/StePanel/actions/runs/40436448192)
and was in progress with its first six-host shard running at the 2026-10-27
02:22 UTC evidence snapshot. These results do not establish
physical host power-loss behavior, broad XFS quota enforcement, or a certified
capacity envelope. Production remains **NO-GO** until all release gates pass.
