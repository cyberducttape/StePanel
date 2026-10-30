# Mixed HTTP load baseline

Original baseline: 2026-10-02. Additional 500-site runs: 2026-10-14.
Host for the October 14 runs: Linux 7.0.0-38-generic x86_64, AMD Ryzen 7
7735U (16 threads, developer laptop).
Go: go1.26.7

Command for the original 200-site profile (CI uses this harness and profile):

```sh
go build -o stepanel ./cmd/stepanel
STEPANEL_LOAD_WORKERS=8 STEPANEL_LOAD_REQUESTS=60 STEPANEL_LOAD_SITES=200 \
  STEPANEL_LOAD_WRITES=20 STEPANEL_LOAD_MAX_P95_MS=500 \
  bash scripts/e2e-mixed-load-test.sh ./stepanel
```

The post-fix 500-site runs used commit
`3c5f9e49859e7836cb115acaa47e5f0b8e012f65`, 8 readers × 100 requests, 50
concurrently executed file-backup jobs, and a 250 ms p95 threshold. They used
the same harness and an executable built from `./cmd/stepanel`.

The original 200-site workload used a real `stepanel` process with
authentication enabled, 200 seeded sites (64 KiB of incompressible data
each), 8 concurrent readers cycling through 12 dashboard and API endpoints,
and 2 writers enqueueing 20 file backups that the in-process worker runs
while readers are active. The 500-site profile used the same site fixture and
reader mix with 50 backup jobs. Each run fails on non-2xx responses, backup
jobs that do not complete, or p95 latency above its configured limit.

| Run | Requests | Avg | p95 | Max | Slowest endpoint (max) |
| --- | ---: | ---: | ---: | ---: | --- |
| Empty control plane, read-only (previous script) | 480 | 2.9 ms | 4.4 ms | 43 ms | — |
| 200 sites + 20 backups, before fixes | 500 | 65 ms | 262 ms | 884 ms | `/api/sites/overview` 884 ms, `/api/health` 411 ms |
| 200 sites + 20 backups, after fixes | 500 | 11 ms | 108 ms | 131 ms | `/api/sites/overview` 141 ms, `/api/health` 22 ms |
| 500 sites + 50 backups, before overview optimization | 850 | 34.8 ms | 392.1 ms | 652.6 ms | `/api/sites/overview` p95 642.9 ms, max 652.6 ms |
| 500 sites + 50 backups, after optimization (run 1) | 850 | 5.2 ms | 14.6 ms | 40.9 ms | `/api/sites/overview` max 40.9 ms |
| 500 sites + 50 backups, after optimization (repeat) | 850 | 23.7 ms | 69.0 ms | 254.9 ms | `/api/sites/overview` max 254.9 ms |

Fixes found by this workload:

- `/api/sites/overview` rescanned the whole sites directory once per site
  (quadratic in the number of sites) and ran the database inventory helper on
  every request. It now resolves all sites from one scan and reads the
  inventory from a 5-second cache that API database mutations invalidate.
- Service status (`/api/health` for administrators) was cached, but every
  request arriving after expiry rebuilt it, each spawning its own
  `systemctl` processes. One request now refreshes it while the others wait.
- The assembled `/api/sites/overview` response uses an invalidation-aware,
  five-second snapshot. Administrator identity is validated once per request,
  and tenant ownership is still checked against the current principal on every
  request. The 500-site pre-fix overview p95 was 643 ms; post-fix runs measured
  15 ms and 69 ms overall p95.

For SQLite session-validation latency under job load on the same day, see
[the 2026-10-02 control-plane baseline](2026-10-02-control-plane-load-baseline.md).

At the repeat run, 14 GiB RAM was installed, 2.8 GiB was available, and
511 MiB swap was in use. Both post-fix runs passed the configured 250 ms p95
threshold, but the variance and maximum latency show why this is not a
certified envelope. The harness omits real web servers, databases, PHP
workloads, TLS termination, long-duration soak, and sustained scheduled jobs;
it does not establish production capacity or customer-traffic SLOs.
