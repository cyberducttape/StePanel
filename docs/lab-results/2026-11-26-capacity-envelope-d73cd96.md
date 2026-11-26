# Full-envelope capacity profile, repeat — 2026-11-26

**Run:** [GitHub Actions 45795590563](https://github.com/cyberducttape/StePanel/actions/runs/45795590563)  
**Source:** `d73cd963` (includes skipping unchanged slice and PHP-FPM pool reloads, `051e7157`)  
**Guest:** Rocky Linux 9.9 KVM on a GitHub-hosted runner, installed MariaDB and Caddy  
**Profile:** `large`, 600 s measured, 8 site clients, 4 customer probes, two 512 MiB backups at 150 s, scheduled backups for 5 sites every 5 minutes, panel restart under load

The second passing run of the full envelope with scheduled jobs and a
restart under load, after [45774333339](./2026-11-26-capacity-envelope-f471efc.md).

| Measure | This run | Previous pass (`f471efc3`) |
| --- | ---: | ---: |
| Setup (500 sites, 2,000 domains, 100 accounts) | 5,978 s | 6,674 s |
| Customer account creation | 8.5 to 10 s each | 13 to 16 s each |
| Site pages p95 / p99, baseline | 85.3 / 104.1 ms | 88.0 / 107.3 ms |
| Site pages p95 / p99, during backups | 104.5 / 137.0 ms | 97.2 / 119.5 ms |
| Site page errors | 0 | 0 |
| Ready after restart | 2.1 s | 1.7 s |
| Minimum available memory | 6,509 MiB | 6,547 MiB |
| Control-plane database-lock errors | 0 | 0 |

Admin and customer errors occurred only inside the deliberate restart
window. Five scheduled backups ran with no problems; tenant isolation
probes found no listing differences. One baseline site request took 8.0 s
(p99 104 ms); it is a single outlier and the report records no transport
failures.

Account creation is faster because resource application no longer reloads
systemd or PHP-FPM for unchanged slices and pools.

## Limits

Two runs on one runner VM type; not a certified limit. Real
WordPress/database traffic, migrations, worker interruption, backup restore
under load, quota behavior and the recovery time and point measurement
remain outside these runs.
