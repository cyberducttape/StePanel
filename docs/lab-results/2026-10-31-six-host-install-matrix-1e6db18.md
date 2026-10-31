# Exact-code six-host install and recovery matrix — 2026-10-31

## Result

GitHub Actions scheduled run
[41189484718](https://github.com/cyberducttape/StePanel/actions/runs/41189484718)
completed successfully on source
`1e6db185e5fc77e034280e9182d30e47c562c190`. All six disposable systemd
hosts passed installation and one recovery-matrix iteration. The run covered
Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9, each with Apache and Caddy and
MariaDB.

Each host passed the installed WordPress restore proof against its database
and HTTP probe. Measured RTOs were:

| OS and web server | WordPress recovery RTO |
| --- | ---: |
| Ubuntu 24.04 / Apache | 5.764 s |
| Ubuntu 24.04 / Caddy | 7.022 s |
| AlmaLinux 9 / Apache | 5.269 s |
| AlmaLinux 9 / Caddy | 7.308 s |
| Rocky Linux 9 / Apache | 6.342 s |
| Rocky Linux 9 / Caddy | 5.936 s |

All six diagnostics reported successful cPanel import, worker interruption
recovery during import, backup verification, restore commit, and site
termination, plus panel interruption recovery during account suspension.
Each site remained unchanged when Git deployment was refused because project
quotas were unavailable. The attached run artifact contains the per-host
logs: `stepanel-install-diagnostics-default-<host>/install-smoke-output.txt`.

## Limits

This is one iteration on each of six disposable OS/web-server combinations.
It is current-code evidence, not production certification. It does not meet
the 20–50 repeat requirement, establish physical host power-loss durability,
test ENOSPC through every database/publication/import boundary, or certify
capacity. Quota-unavailable Git deployment was verified to fail closed; this
run does not establish aggregate project-quota enforcement on each host.
The release decision remains **NO-GO**.
