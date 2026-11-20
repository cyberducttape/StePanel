# Rocky Linux 9 large capacity profile — 2026-11-20

Workflow: [capacity profile 44674647682](https://github.com/cyberducttape/StePanel/actions/runs/44674647682)

Product and probe source: `8382e9c4e35b03320418fe601ebbcef4c2d7a484`
Result: **failed; no 500-site capacity claim is supported.**

The KVM guest booted on Rocky Linux 9 with ext4 user and project quotas
enabled. Installation passed 19 smoke/matrix checks. The profile seeded 500
PHP sites, then ran eight HTTPS site clients and administrative probes for ten
minutes while starting two 512 MiB site backups. Site setup took 2,283 seconds;
the complete run took 2,941 seconds.

## Measurements

| Traffic | Window | Requests | p50 ms | p95 ms | p99 ms | Maximum ms | Errors |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Site pages | Baseline | 8,819 | 58.5 | 88.5 | 7,681.0 | 14,943.3 | 210 |
| Site pages | During backups | 580 | 65.5 | 113.9 | 9,962.2 | 14,370.1 | — |
| `/api/sites` | Baseline | 1,022 | 2.5 | 6.2 | 14.9 | 61.7 | 0 |
| `/api/sites` | During backups | 79 | 2.7 | 6.6 | 22.3 | 45.1 | 0 |
| `/api/jobs` | Baseline | 1,022 | 2.9 | 6.7 | 17.0 | 49.7 | 0 |
| `/api/jobs` | During backups | 79 | 3.3 | 7.8 | 10.9 | 11.0 | 0 |
| `/api/sites/overview` | Baseline | 1,022 | 2.0 | 179.4 | 978.8 | 3,153.2 | 0 |
| `/api/sites/overview` | During backups | 79 | 2.5 | 261.8 | 1,049.0 | 1,375.3 | 0 |

The 210 failed site requests were connection/read timeouts at the probe's
15-second limit; no HTTP response was received. The available samples do not
establish whether PHP-FPM saturation, site CPU throttling, process startup
overhead, or another part of the request path caused the timeouts.

Both large backup jobs completed (24.1 and 43.8 seconds; queue wait 0.54 and
0.51 seconds). Host CPU averaged 19.4% and peaked at 99.0%; minimum available
memory was 7,895 MiB. The probe's aggregate PHP-FPM RSS sample peaked at
4,492.8 MiB, which includes shared mappings and is not private-memory usage.
The control-plane database recorded zero lock errors. One initial readiness
probe returned 503 while the offsite target probe was still running.

The run used source `8382e9c4`, not current main, and did not capture per-site
PHP-FPM queue/saturation or cgroup CPU/memory snapshots during the failing
requests. Those are now included in the capacity probe for the next run. This
result is diagnostic evidence only, not a capacity certification; a current-
source rerun and a measured remediation are still required.
