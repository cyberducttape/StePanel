# Full-envelope capacity profile with scheduled jobs and restart — 2026-11-26

**Run:** [GitHub Actions 45774333339](https://github.com/cyberducttape/StePanel/actions/runs/45774333339)  
**Source:** `f471efc3`  
**Guest:** Rocky Linux 9.9 KVM on a GitHub-hosted runner, installed MariaDB and Caddy  
**Profile:** `large`, 600 s measured, 8 site clients, 4 logged-in customer probes, two 512 MiB backups started at 150 s, scheduled backups for 5 sites every 5 minutes, and a panel restart under load

This is the first run that adds the scheduled-job load and a restart under
load to the structural targets of the
[proposed envelope](../CERTIFIED_ENVELOPE.md): 500 live PHP sites, 2,000
served domain routes (every one checked to answer before measuring), and
100 customer accounts owning five sites each. Setup took 6,674 s; customer
accounts took 13 to 16 s each to create.

## Results

| Traffic | Requests | p95 ms | p99 ms | Errors |
| --- | ---: | ---: | ---: | ---: |
| Site pages, baseline | 70,660 | 88.0 | 107.3 | 0 |
| Site pages, during backups | 5,129 | 97.2 | 119.5 | 0 |
| `/api/sites/overview` (admin), baseline / during backups | 1,030 / 77 | 19.6 / 21.1 | 33.4 / 29.5 | 2 / 0 |
| `/api/accounts` (admin), baseline | 1,030 | 22.8 | 38.0 | 2 |
| Customer `/api/account/hosting`, baseline | 2,134 | 21.9 | 33.1 | 4 |
| Customer `/api/sites/overview`, baseline | 2,134 | 67.0 | 91.6 | 4 |

Every admin and customer error falls inside the deliberate restart window
(450.5 to 451.6 s): connections refused while the panel restarted, then one
`/readyz` 503 while the offsite probe re-ran. The panel was ready again
after 1.7 s.

- Scheduled jobs: 5 scheduled backups ran during measurement, 0 problems.
- Both heavy backups completed (queue wait 0.3 s; 22.0 s and 43.2 s).
- Tenant isolation: 4 customer probes, 0 listings differing from the
  account's own sites.
- Host CPU peaked at 99.7% (mean 44.1%); minimum available memory 6,547 MiB;
  summed PHP-FPM RSS 9.0 GiB peak; panel and worker RSS 106 MiB peak.
- PHP-FPM: 504 of 504 pools sampled, no saturated pool or listen queue,
  master descriptors peaked at 1,546 of 65,536, no fork or descriptor
  warnings.
- Control-plane database-lock errors in the panel and worker journals: 0.
- Static file probe: 0 of 565 requests failed.

## The preceding failed run

Run [45749666185](https://github.com/cyberducttape/StePanel/actions/runs/45749666185)
on `56f5359f` failed while creating customer account 72 with a sanitized
503, after account creation had slowed to about 22 s per account (this run:
13 to 16 s). Its journal was not collected, so the cause is not proven.
The leading explanation is a resource apply (a slice daemon-reload or a
PHP-FPM reload, each inside a 30-second broker timeout) running long on a
slower runner. Since then resource application skips unchanged slices and
pools (`051e7157`), and a failed capacity run keeps the panel and broker
journals and a daemon-reload timing.

## Limits

One run on one runner VM type; not a certified limit. It did not include
real WordPress/database traffic, migrations, worker interruption, backup
restore under load, or quota behavior, and the envelope's recovery time and
recovery point measurement is still open.
