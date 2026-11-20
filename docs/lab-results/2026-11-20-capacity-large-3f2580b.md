# Rocky Linux 9 large capacity profile — 2026-11-20

Workflow: [capacity profile 44663139216](https://github.com/cyberducttape/StePanel/actions/runs/44663139216)<br>
Product source: `3f2580b` (the workflow-dispatch source recorded for this run)<br>
Result: **failed; no 500-site capacity claim is supported by this run.**

The KVM guest boot and installation passed. The test seeded 500 sites, then
ran an eight-client HTTPS probe for 600 seconds while starting two 512 MiB
backups. Seeding took 2,356 seconds. Both backup jobs completed: 20.6 and
39.7 seconds, with 0.28 and 0.26 seconds of queue wait.

## Measurements

| Signal | Baseline | During backups |
| --- | ---: | ---: |
| HTTPS site requests | 9,183 | 444 |
| HTTPS p50 / p95 / p99 | 56.1 / 80.7 / 8,115.4 ms | 60.9 / 95.3 / 10,559.9 ms |
| HTTPS maximum latency | 14,879.3 ms | 12,267.6 ms |
| `/api/sites` p95 / p99 | 5.5 / 12.2 ms | 7.6 / 10.7 ms |
| `/api/jobs` p95 / p99 | 6.5 / 13.3 ms | 6.2 / 10.2 ms |
| `/api/sites/overview` p95 / p99 | 215.9 / 819.2 ms | 28.5 / 448.2 ms |
| Control-plane database-lock errors | 0 | 0 |

The resource sampler reported CPU mean/max 19.2%/98.8%, control-plane
panel/worker RSS max 91.2 MiB, PHP-FPM RSS max 4,486.3 MiB, and available
memory minimum 8,031 MiB. One baseline `/readyz` probe returned 503 while the
offsite target probe was in progress; subsequent probes passed.

The run observed **202 non-2xx/3xx site responses out of 9,829**. That is why
the capacity workflow failed. The profile at this source only retained the
aggregate count, not the response codes, bodies, or correlated PHP/web-server
diagnostics, so the cause of those responses cannot be determined from this
artifact. Its p99 and maximum site latency also show substantial tails even
though median and p95 latency stayed low. The failures must not be dismissed as
test noise without a diagnostic rerun.

The capacity probe now records response status counts and up to 50 bounded
failure samples (including response headers/body prefix or connection error).
A rerun is required to identify the cause. The current run is useful
exploratory evidence only and does not certify the proposed 500-site envelope.
