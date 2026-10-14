# Hosted installed-host smoke — 2026-10-14

| Field | Value |
|---|---|
| Workflow | [Installation smoke run 38508198574](https://github.com/cyberducttape/StePanel/actions/runs/38508198574) |
| Source commit | `d553838427c63f122a2b9f7b1027a42f28b7ccca` |
| Database | MariaDB |
| Result | PASS — all six systemd-container installation and recovery jobs |
| Recovery matrix | One iteration per OS/web-server combination; no claim of 20–50-repeat certification |

Every job installed StePanel in a disposable systemd host, ran the default
recovery drills and one additional interruption-matrix iteration, restored a
populated WordPress site through the installed site/account/database/PHP-FPM/
web-server path, and verified the published application over HTTP(S). The
WordPress proof created a database-backed post before backup and checked that
the recovered application served it. Per-job logs reported successful
`installed WordPress recovery proof` and `recovery matrix passed (1
iteration(s))` results.

| Distribution | Web server | Job evidence | WordPress proof RTO |
|---|---|---|---:|
| Ubuntu 24.04 | Apache | [job 115670632102](https://github.com/cyberducttape/StePanel/actions/runs/38508198574/job/115670632102) | 4.5 s |
| Ubuntu 24.04 | Caddy | [job 115670632111](https://github.com/cyberducttape/StePanel/actions/runs/38508198574/job/115670632111) | 5.5 s |
| AlmaLinux 9 | Apache | [job 115670632314](https://github.com/cyberducttape/StePanel/actions/runs/38508198574/job/115670632314) | 5.4 s |
| AlmaLinux 9 | Caddy | [job 115670632452](https://github.com/cyberducttape/StePanel/actions/runs/38508198574/job/115670632452) | 5.3 s |
| Rocky Linux 9 | Apache | [job 115670632109](https://github.com/cyberducttape/StePanel/actions/runs/38508198574/job/115670632109) | 8.0 s |
| Rocky Linux 9 | Caddy | [job 115670632078](https://github.com/cyberducttape/StePanel/actions/runs/38508198574/job/115670632078) | 5.5 s |

The matrix also exercised cpmove activation, backup verification, file-restore
commit, site-termination routes, account suspension persistence, and Git
deployment activation interruption checkpoints. These are single hosted
systemd-container runs, not fresh-VM recovery certification. The six passing
jobs do not close physical host power-loss, broader ENOSPC, database/broker
outage, repeated 20–50-run recovery, or capacity-certification gates. The
hosted environment does not certify OpenLiteSpeed, alternate database engines,
PHP version ranges, or broader combinations.
