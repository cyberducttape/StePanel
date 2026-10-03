# Mixed HTTP load baseline

Date: 2026-10-02
Host: Linux 7.0.0-34-generic x86_64, AMD Ryzen 7 7735U (developer laptop)
Go: go1.26.7

Command (the same workload CI runs on every push):

```sh
go build -o stepanel .
STEPANEL_LOAD_WORKERS=8 STEPANEL_LOAD_REQUESTS=60 STEPANEL_LOAD_SITES=200 \
  STEPANEL_LOAD_WRITES=20 STEPANEL_LOAD_MAX_P95_MS=500 \
  bash scripts/e2e-mixed-load-test.sh ./stepanel
```

Workload: a real `stepanel` process with authentication enabled, 200 seeded
sites (64 KiB of incompressible data each), 8 concurrent readers cycling
through 12 dashboard and API endpoints (`/api/sites/overview`, `/api/health`,
`/api/backups`, `/api/jobs`, `/metrics`, and others), and 2 concurrent writers
enqueueing 20 file backups that the in-process worker runs while the readers
are active. The run fails on any non-2xx response, on any backup job that
does not complete, or when p95 latency exceeds the limit.

| Run | Requests | Avg | p95 | Max | Slowest endpoint (max) |
| --- | ---: | ---: | ---: | ---: | --- |
| Empty control plane, read-only (previous script) | 480 | 2.9 ms | 4.4 ms | 43 ms | — |
| 200 sites + 20 backups, before fixes | 500 | 65 ms | 262 ms | 884 ms | `/api/sites/overview` 884 ms, `/api/health` 411 ms |
| 200 sites + 20 backups, after fixes | 500 | 11 ms | 108 ms | 131 ms | `/api/sites/overview` 141 ms, `/api/health` 22 ms |

Fixes found by this workload:

- `/api/sites/overview` rescanned the whole sites directory once per site
  (quadratic in the number of sites) and ran the database inventory helper on
  every request. It now resolves all sites from one scan and reads the
  inventory from a 5-second cache that API database mutations invalidate.
- Service status (`/api/health` for administrators) was cached, but every
  request arriving after expiry rebuilt it, each spawning its own
  `systemctl` processes. One request now refreshes it while the others wait.

For SQLite session-validation latency under job load on the same day, see
[LOAD_BASELINE_2026-10-02.md](LOAD_BASELINE_2026-10-02.md).

This is one developer-host run plus the CI gate. It is not a capacity claim
for production hardware: it does not include real web servers, databases,
or PHP workloads, TLS termination, or more than a few hundred sites.
