# 500-site installed capacity profile — `e1195ccc`

**Result:** Failed; diagnostic evidence only. This run does not certify a
500-site operating envelope.

- Workflow: [capacity profile run 44686066350](https://github.com/cyberducttape/StePanel/actions/runs/44686066350)
- Exact source: `e1195cccf8d9f6489e6c22f4f1ce355598b5b658`
- Host: Rocky Linux 9 disposable KVM guest; 500 PHP sites plus two backup
  sites; eight sequential HTTPS clients; 600-second measured interval; two
  concurrent 512 MiB local backups.
- Artifact: `capacity-large-e1195cccf8d9f6489e6c22f4f1ce355598b5b658`,
  attached to the workflow run. This report summarizes its
  `capacity-report.md`, JSON, and results table.

## Result

The profile failed: **184 of 9,791 site requests timed out after 15 seconds**
(1.88%). In the baseline window, successful site requests had p50 72.2 ms,
p95 135.3 ms, p99 8,074.5 ms, and maximum 14,812.3 ms. During the heavy
backups, successful site requests had p50 82.0 ms, p95 152.0 ms, p99
10,560.0 ms, and maximum 14,064.7 ms. The admin API remained responsive:
`/api/sites` and `/api/jobs` had zero errors, with baseline p99s of 29.9 ms
and 29.3 ms. Both 512 MiB backup jobs completed in 31.3 and 58.0 seconds. No
SQLite busy or database-lock errors were recorded.

| Host signal | Measured result |
| --- | ---: |
| CPU | 31.9% mean, 99.9% maximum |
| Minimum available memory | 7,901 MiB |
| PHP-FPM process RSS | 3,438 MiB mean, 4,448.9 MiB maximum |
| MariaDB process RSS | 104.7 MiB |
| Disk writes | 8.2 MiB/s mean, 252.3 MiB/s maximum |
| Runtime metric scrapes | 39 successful, 0 failed |
| Sites sampled / FPM probe available | 504 / 473 |
| Saturated FPM pools / maximum listen queue | 0 / 0 |
| Maximum sampled active workers / slow requests | 1 / 0 |

## Interpretation and limits

The run establishes a customer-HTTP latency failure at this workload and
host configuration. It does **not** identify the root cause: the FPM metrics
did not report saturation, the metrics lacked FPM availability for 31 of 504
sampled sites, and the probe recorded aggregate host CPU rather than
per-request CPU or web-server queue timing. The high tail latency and CPU peak
justify further layer-specific profiling; they do not justify changing PHP
worker defaults or publishing a lower/higher site limit by inference.

One early `/readyz` request returned 503 while the remote-backup target probe
was still running. That readiness response is separate from the site HTTP
timeouts. The two backups completed, but this small number of operations is
not a backup-interference or sustained-load certification. This profile also
does not exercise production WordPress traffic, databases under application
load, migrations, host restart, or recovery at target load.

## Next evidence required

Repeat the profile after adding request-stage timing (TCP connect, TLS
handshake, web-server response, PHP execution), retain bounded per-site
diagnostics for failed requests, and ensure every expected site's FPM probe
status is accounted for. Use the results to isolate the bottleneck before
changing runtime defaults. Then rerun the same workload on exact release
source and compare the full latency/error distribution. The 500-site target
remains uncertified.
