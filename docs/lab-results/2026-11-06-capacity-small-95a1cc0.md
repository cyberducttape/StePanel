# Capacity profile `small` (25 sites) on a Rocky Linux 9 KVM guest

Date: 2026-11-06. Source: `95a1cc0f655bb772da05c1d279688ad07f64089f`.
Run: [Capacity profile (KVM) 42215057200](https://github.com/cyberducttape/StePanel/actions/runs/42215057200)
(`capacity-profile.yml`, profile `small`, 600 s measured).

Environment: GitHub-hosted `ubuntu-24.04` runner running a Rocky Linux 9 KVM
guest (kernel 5.14.0-687.10.1.el9_8; 4 vCPU, 4096 MiB), installed with `--unsafe-lab` by
`deploy/lab/local-kvm-certification.sh` (install phase: 19 smoke/matrix
passes), then measured by `deploy/lab/capacity-profile.py`. The guest disk
uses QEMU writeback caching, and the runner is a shared cloud VM, so these
numbers describe this disposable substrate, not a dedicated host.

What ran: 25 PHP sites under 8 concurrent site clients for 600 s, an admin
client polling `/api/sites`, `/api/jobs`, `/readyz` and
`/api/sites/overview`, and two 512 MiB site backups started at 150 s as the
heavy operations. "During heavy" covers the time either backup was running.

| Traffic | Window | Requests | p50 ms | p95 ms | p99 ms | max ms | Errors |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| site pages | baseline | 77023 | 54.3 | 73.9 | 85.4 | 141.6 | 0 |
| site pages | during heavy | 6538 | 58.3 | 84.0 | 97.8 | 123.3 |  |
| /api/sites | baseline | 1064 | 1.8 | 15.8 | 21.2 | 37.6 | 0 |
| /api/sites | during heavy | 96 | 3.1 | 14.4 | 27.8 | 435.8 |  |
| /api/jobs | baseline | 1064 | 2.6 | 12.6 | 18.9 | 30.9 | 0 |
| /api/jobs | during heavy | 96 | 3.5 | 14.2 | 23.0 | 23.4 |  |
| /readyz | baseline | 1064 | 1.4 | 9.5 | 17.5 | 37.7 | 0 |
| /readyz | during heavy | 96 | 1.7 | 9.9 | 17.8 | 18.8 |  |
| /api/sites/overview | baseline | 1064 | 0.9 | 15.2 | 23.3 | 105.1 | 0 |
| /api/sites/overview | during heavy | 96 | 1.0 | 18.6 | 34.9 | 35.9 |  |

| Heavy operation | State | Queue wait s | Duration s |
| --- | --- | ---: | ---: |
| backup cap-heavy-0 | completed | 0.29 | 25.3 |
| backup cap-heavy-1 | completed | 0.27 | 50.1 |

| Resource | Mean | Max |
| --- | ---: | ---: |
| cpu_pct | 24.0 | 70.7 |
| panel_worker_rss_mib | 75.1 | 80.3 |
| php_fpm_rss_mib | 884.0 | 974.2 |
| mariadb_rss_mib | 102.0 | 102.0 |
| disk_read_mib_s | 0.2 | 45.3 |
| disk_write_mib_s | 9.0 | 254.6 |

Minimum available memory: 2804 MiB. Control-plane database-lock errors in the panel and worker journals: 0.

## Reading

- No request failed in any window, including `/readyz`. The run before the
  readiness fix on `9d3c752` (offsite probe served stale-while-refresh) saw
  `/readyz` return 503 every 30 s while the offsite probe refreshed.
- Site p95 rose from 73.9 to 84.0 ms while the backups ran; admin p95 stayed
  under 19 ms. The worst admin request during heavy work was one
  `/api/sites` at 436 ms.
- Both backups started within 0.3 s of being queued.
- No control-plane database-lock errors were logged.

## Limits of this evidence

This is one run of the smallest profile. The node (100–200 sites) and large
(500 sites) profiles, repeated runs, and dedicated hardware remain open, so
it does not certify the envelope in [CERTIFIED_ENVELOPE.md](../CERTIFIED_ENVELOPE.md).
