# Rocky Linux 9 large capacity profile — 2026-11-20

Workflow: [capacity profile 44725885994](https://github.com/cyberducttape/StePanel/actions/runs/44725885994)<br>
Product source: `f464f79d6287741eefd14174caaac35cd9f18534`<br>
Result: **failed; this run does not support a 500-site capacity claim.**

The Rocky Linux 9 KVM guest booted and installed successfully with ext4 user
and project quotas enabled. The profile created 500 PHP sites and two backup
sites (setup 2,289 seconds), then measured eight sequential HTTPS clients for
600 seconds while two 512 MiB backups ran concurrently. Both backups
completed in 22.1 and 40.7 seconds; queue waits were 0.04 and 0.52 seconds.

## Measurements

| Signal | Baseline | During backups |
| --- | ---: | ---: |
| Successful site requests | 8,889 | 573 |
| Successful site p50 / p95 / p99 | 60.6 / 89.8 / 8,607.6 ms | 56.6 / 86.3 / 10,731.0 ms |
| Successful site maximum | 14,855.1 ms | 12,956.4 ms |
| `/api/sites` p99 | 15.8 ms | 9.0 ms |
| `/api/jobs` p99 | 16.2 ms | 9.9 ms |
| `/api/sites/overview` p99 | 1,028 ms | 448 ms |
| Control-plane SQLite lock errors | 0 | 0 |

There were **196 failed site requests out of 9,658 total (2.03%)**. The
retained failures were 15-second timeouts while waiting for the first HTTPS
response byte. The report recorded 39 successful runtime-metrics scrapes and
no failed scrapes. At the sampled peaks, 504 sites were represented, FPM
metrics were available for 477, no pool was marked saturated, and the maximum
listen queue was zero. Up to 96 site samples were missing. These periodic
samples are not synchronized to individual failed requests and do not explain
the timeouts.

Host CPU averaged 20.0% and peaked at 99.3%; available memory stayed above
7,861 MiB. Panel/worker RSS peaked at 89.2 MiB. PHP-FPM aggregate RSS peaked
at 4,523.9 MiB; this process RSS may include shared mappings. Disk writes
peaked at 379.7 MiB/s. One initial `/readyz` request returned 503 while the
offsite target probe was in progress; this was separate from the customer
site-response failures.

## Interpretation and limits

This is a genuine workload failure for the tested host configuration: the
site traffic returned no response within 15 seconds for 196 requests, and the
successful-response p99 tails were high. The API remained responsive and the
two backups completed. The available measurements do not establish whether
the cause was PHP-FPM, web-server queueing, CPU throttling, process scheduling,
or another request-path bottleneck. Do not change runtime defaults or infer a
supported site limit from this single run.

The profile source is an ancestor of current `main`, but this is not a
current-commit capacity certification. It is also not a WordPress/database
production workload, a sustained multi-day soak, or recovery evidence. The
capacity gate remains open.

The follow-up capacity harness now records status/error counts separately for
the baseline and backup windows and attaches the most recent bounded,
site-specific FPM and cgroup resource sample to each retained HTTP failure.
That diagnostic change is covered by `deploy/lab/test_capacity_profile.py`;
a new installed-host run is required to collect the added evidence.
