# Current-code six-host installation smoke — 2026-11-20

**Source:** `059ba72e05fd5c0c8ea2b560e0a21a07506b5a65`

**Code equivalence:** The non-Markdown source tree matches current `main`
(`672eae2d`) exactly. The documentation commits after the tested source do not
change executable content.

**Workflow:** [Installation smoke, run 44729127788](https://github.com/cyberducttape/StePanel/actions/runs/44729127788)

**Result:** All six host jobs and the host-selection job passed. I reviewed the
uploaded `install-smoke-output.txt` artifact from each host. Every host log
records successful installed file-manager and detached-package-process
cgroup smoke checks, an installed database-backed WordPress HTTP recovery
proof, and one recovery-matrix iteration.

The file-manager check created 10,000 entries and required the listing helper
to return exactly the 5,001-record cap, under 2 MiB, within 10 seconds. It
interposed a symlink replacement before helper execution for read, write,
mkdir, delete, rename, and list, and verified those operations did not affect
an account-owned sibling directory. It also checked that the site account
could not read `/etc/shadow`. The package cgroup check ran a detached child
under a transient service and verified it stopped with the service unit.

| Host OS | Web server | Database | File manager | Package cgroup | Recovery matrix | WordPress proof RTO |
| --- | --- | --- | --- | --- | --- | ---: |
| Ubuntu 24.04 | Apache | MariaDB | Passed | Passed | Passed, 1 iteration | 5.854 s |
| Ubuntu 24.04 | Caddy | MariaDB | Passed | Passed | Passed, 1 iteration | 6.866 s |
| AlmaLinux 9 | Apache | MariaDB | Passed | Passed | Passed, 1 iteration | 4.588 s |
| AlmaLinux 9 | Caddy | MariaDB | Passed | Passed | Passed, 1 iteration | 5.084 s |
| Rocky Linux 9 | Apache | MariaDB | Passed | Passed | Passed, 1 iteration | 6.166 s |
| Rocky Linux 9 | Caddy | MariaDB | Passed | Passed | Passed, 1 iteration | 5.131 s |

Host logs are available from the [workflow artifacts](https://github.com/cyberducttape/StePanel/actions/runs/44729127788): [Ubuntu/Apache](https://github.com/cyberducttape/StePanel/actions/runs/44729127788/job/136264095386), [Ubuntu/Caddy](https://github.com/cyberducttape/StePanel/actions/runs/44729127788/job/136264095585), [AlmaLinux/Apache](https://github.com/cyberducttape/StePanel/actions/runs/44729127788/job/136264095517), [AlmaLinux/Caddy](https://github.com/cyberducttape/StePanel/actions/runs/44729127788/job/136264095411), [Rocky Linux/Apache](https://github.com/cyberducttape/StePanel/actions/runs/44729127788/job/136264095492), and [Rocky Linux/Caddy](https://github.com/cyberducttape/StePanel/actions/runs/44729127788/job/136264095317).

## Limits

This is one pass per combination in disposable systemd-enabled containers on
Ubuntu runners. It is not a physical-host or independently booted VM test and
does not establish real power-loss durability, broad ENOSPC behavior,
production quota enforcement, or recovery at every irreversible mutation
boundary. It does not close the separate 20-repeat cohort. The results support
the tested OS/web-server/MariaDB combinations as operator-beta smoke coverage;
they do not certify a production capacity envelope or mutually hostile tenant
isolation.
