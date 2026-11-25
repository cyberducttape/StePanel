# Full-envelope capacity profile (500 sites, 2,000 domains, 100 accounts) — 2026-11-25

**Run:** [GitHub Actions 45531628244](https://github.com/cyberducttape/StePanel/actions/runs/45531628244)  
**Source:** `23980ad1` (capacity profile from `d76958ef`)  
**Guest:** Rocky Linux 9.9 KVM on a GitHub-hosted runner, 10 GiB guest memory, installed MariaDB and Caddy  
**Profile:** `large`, 600 s measured, 8 site clients, 4 logged-in customer probes, two 512 MiB backups started at 150 s

This is the first run that seeds every dimension of the
[proposed envelope](../CERTIFIED_ENVELOPE.md) except scheduled-job volume:
500 live PHP sites, 2,000 served domain routes (one primary and three
aliases per site, every one checked to answer before measuring), and 100
professional-plan customer accounts owning five sites each.

## Results

| Traffic | Requests | p95 ms | p99 ms | Errors |
| --- | ---: | ---: | ---: | ---: |
| Site pages, baseline | 66,573 | 93.4 | 114.6 | 0 |
| Site pages, during backups | 5,044 | 113.0 | 149.1 | 0 |
| `/api/sites/overview` (admin), baseline / during backups | 1,009 / 83 | 43.1 / 67.0 | 168.2 / 239.0 | 0 |
| `/api/accounts` (admin), baseline | 1,009 | 26.0 | 49.7 | 0 |
| Customer `/api/account/hosting`, baseline | 2,105 | 26.4 | 42.0 | 0 |
| Customer `/api/sites/overview`, baseline | 2,105 | 91.0 | 162.4 | 0 |

- Tenant isolation: 4 customer probes, 0 listings differing from the
  account's own sites.
- Both heavy backups completed (queue wait 0.3 s; 25.7 s and 47.1 s).
- Host CPU peaked at 99.7% (mean 45.4%); minimum available memory 6,465 MiB;
  summed PHP-FPM RSS 9.2 GiB peak.
- PHP-FPM: 504 of 504 pools sampled, no saturated pool or listen queue,
  descriptors peaked at 2,808 of 131,072, no master warnings.
- Control-plane database-lock errors: 0. Static-file probe: 0 of 564 failed.
- One baseline site request took 6.9 s to first byte; p99 stayed at
  114.6 ms.

## Operational observations

Setup took 8,758 s. Route changes slow as the route count grows: the first
100 alias routes took about 1.8 s each, and the 1,500 aliases averaged about
3 s each. Creating a customer account, which applies its plan's resource
profiles, took about 12 s. These are administrative latencies, not serving
latencies, but an operator adding many domains on a large host will notice
them.

## Limits

One pass on one runner VM type. The workload is synthetic PHP without
WordPress database traffic, and the 50-scheduled-jobs-per-hour target,
restart and recovery under load, and repeated runs are not covered. This is
diagnostic evidence toward the envelope, not a certification.
