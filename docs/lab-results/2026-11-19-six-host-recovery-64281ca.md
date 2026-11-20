# Exact-source six-host recovery iteration — 2026-11-19

**Source:** `64281ca87391c54038547b8b9dc002701b2ae125`

**Workflow:** [Installation smoke, run 44516615547](https://github.com/cyberducttape/StePanel/actions/runs/44516615547)

**Result:** 7/7 jobs passed (host selection plus six host combinations).

The workflow built each candidate from the source tree, installed it in a
disposable systemd-enabled container on an Ubuntu 24.04 runner, and exercised
the full configured recovery matrix once on each host combination. The matrix
included worker interruption during cPanel import, backup, restore, database
restore, and site termination, along with broker interruption and recovery
checks. Each host also completed the database-backed WordPress recovery proof.
The uploaded `install-smoke-output.txt` files are the primary detailed records.

| Host OS | Web server | Database | Recovery-matrix result | WordPress proof RTO |
| --- | --- | --- | --- | ---: |
| Ubuntu 24.04 | Apache | MariaDB | Passed, 1 iteration | 6.420 s |
| Ubuntu 24.04 | Caddy | MariaDB | Passed, 1 iteration | 7.106 s |
| AlmaLinux 9 | Apache | MariaDB | Passed, 1 iteration | 6.487 s |
| AlmaLinux 9 | Caddy | MariaDB | Passed, 1 iteration | 5.092 s |
| Rocky Linux 9 | Apache | MariaDB | Passed, 1 iteration | 4.756 s |
| Rocky Linux 9 | Caddy | MariaDB | Passed, 1 iteration | 5.298 s |

Per-host job records: [Ubuntu/Apache](https://github.com/cyberducttape/StePanel/actions/runs/44516615547/job/135534193231), [Ubuntu/Caddy](https://github.com/cyberducttape/StePanel/actions/runs/44516615547/job/135534193347), [AlmaLinux/Apache](https://github.com/cyberducttape/StePanel/actions/runs/44516615547/job/135534193260), [AlmaLinux/Caddy](https://github.com/cyberducttape/StePanel/actions/runs/44516615547/job/135534193340), [Rocky Linux/Apache](https://github.com/cyberducttape/StePanel/actions/runs/44516615547/job/135534193319), and [Rocky Linux/Caddy](https://github.com/cyberducttape/StePanel/actions/runs/44516615547/job/135534194056).

## Limits

This is one successful iteration per combination, not the required 20–50
repeats. A systemd-enabled container is not a physical host or an independently
booted VM. This run does not establish physical power-loss durability, ENOSPC
behavior during database publication/import, project-quota enforcement, or
recovery across every broker mutation boundary. The separate exact-source
20-repeat cohort and abrupt-loss operation workflow are recorded as pending
until their full results and artifacts are reviewed.
