# Exact-source six-host installation smoke — 2026-11-24

**Product source:** `ef0ca96de723bde69c98ba9d0149f7c2532b2fac`

**Documentation-only descendant:** `0bd5e1b9`. Executable and web-asset
sources tested by the workflow match this commit.

**Workflow:** [Installation smoke, run 45405968473](https://github.com/cyberducttape/StePanel/actions/runs/45405968473)

**Result:** All six host jobs passed. I reviewed the uploaded install log from
each host. Every host completed installation and cleanup, passed its one-pass
recovery matrix, database-outage recovery, root-broker interruption check, and
database-backed WordPress HTTPS recovery probe.

| Host OS | Web server | Database | Recovery matrix | WordPress proof RTO |
| --- | --- | --- | --- | ---: |
| Ubuntu 24.04 | Apache | MariaDB | Passed, 1 iteration | 3.574 s |
| Ubuntu 24.04 | Caddy | MariaDB | Passed, 1 iteration | 6.047 s |
| AlmaLinux 9 | Apache | MariaDB | Passed, 1 iteration | 4.610 s |
| AlmaLinux 9 | Caddy | MariaDB | Passed, 1 iteration | 8.800 s |
| Rocky Linux 9 | Apache | MariaDB | Passed, 1 iteration | 4.549 s |
| Rocky Linux 9 | Caddy | MariaDB | Passed, 1 iteration | 5.379 s |

The disposable hosts did not enforce StePanel project quotas; Git deployment
was refused as designed. Detailed logs are available from the workflow
artifacts: [Ubuntu/Apache](https://github.com/cyberducttape/StePanel/actions/runs/45405968473/job/138486750969),
[Ubuntu/Caddy](https://github.com/cyberducttape/StePanel/actions/runs/45405968473/job/138486751011),
[AlmaLinux/Apache](https://github.com/cyberducttape/StePanel/actions/runs/45405968473/job/138486751238),
[AlmaLinux/Caddy](https://github.com/cyberducttape/StePanel/actions/runs/45405968473/job/138486751166),
[Rocky Linux/Apache](https://github.com/cyberducttape/StePanel/actions/runs/45405968473/job/138486751068),
and [Rocky Linux/Caddy](https://github.com/cyberducttape/StePanel/actions/runs/45405968473/job/138486751539).

## Limits

This is one disposable systemd-container pass per combination on Ubuntu
runners. It does not certify physical-host power-loss durability, repeated
recovery, production quota enforcement, broad ENOSPC behavior, capacity, or
mutually hostile tenant isolation. The measured RTOs are single-run test
measurements, not service guarantees. These results extend Operator Beta smoke
evidence; they do not make a production certification claim.
