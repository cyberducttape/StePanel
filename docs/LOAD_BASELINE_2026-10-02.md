# Control-plane job and session load baseline

Date: 2026-10-02
Host: Linux 7.0.0-34-generic x86_64, AMD Ryzen 7 7735U (16 threads)
Go: go1.26.7

Evidence label: **Measured** (synthetic SQLite/control-plane benchmark). This
is not HTTP latency and not a validated production ceiling; see
[PERFORMANCE.md](PERFORMANCE.md) for the evidence labels.

## Capacity target

**At 500 sites, session validation p99 stays at or below 25 ms while 50 Job
Center viewers poll once per second and two workers claim ten jobs per second
each.** The dataset is a 20,000-job history across 500 owners with 400 queued
and 8 running jobs.

Session validation is the query every authenticated API request runs, and
the control plane uses one SQLite connection, so it queues behind job
listing and claiming. Its latency under job activity is the most direct
measure of how background work affects interactive requests.

## Method

```sh
go test . -run '^$' -bench 'SessionValidationUnderJobLoad' -benchtime=3000x -count=3
go test . -run '^$' -bench 'JobsListAtScale|JobsClaimNextAtScale' -benchtime=30x -count=3
```

`BenchmarkSessionValidationUnderJobLoad` samples open-loop: each session check
starts after a random 0–2 ms pause, as independent requests would. A
back-to-back loop understates queueing (coordinated omission). An earlier
version of this benchmark reported a p99 of 29 µs for the target profile for
exactly that reason.

## Results

Session validation (µs, three runs):

| Profile | p50 | p90 | p99 |
| --- | ---: | ---: | ---: |
| Idle | 46–52 | 73–79 | 103–114 |
| **Target** (50 viewers at 1/s, 2 workers at 10/s) | 64–65 | 92–99 | **6,023–8,582** |
| Target, before the List/index changes | 540–599 | 5,618–6,270 | 146,140–147,131 |
| Saturated (4 pollers and 2 workers back to back) | 9,683–10,631 | 31,866–34,561 | 58,440–69,216 |

Job queries (20,000-job history, 400 queued):

| Query | Before | After |
| --- | ---: | ---: |
| `Jobs.List(100)` | 15.2–15.5 ms | 2.4 ms |
| `Jobs.ClaimNext` | 70–76 ms | 3.8–4.3 ms |

The improvements come from three changes. `Jobs.List` now loads jobs in one
query instead of one query per job. Migration 9 adds `jobs(started_at, id)`
for the newest-first listing and `jobs(owner, state, started_at, id)` for the
per-owner check in `ClaimNext`, which previously rescanned the whole queue for
every candidate.

## Conclusion and limits

- The target profile meets the 25 ms p99 target with about 3× headroom on this
  host. Before these changes it missed it by about 6×.
- Under saturation the single connection is the bottleneck: session checks
  wait 10 ms at p50 and over 50 ms at p99. If the target grows (more
  concurrent viewers, faster workers, more sites), the next step is a
  separate read-only connection pool for session, token, and job reads,
  keeping a single writer. WAL mode supports this. Code that relies on
  single-connection ordering must be audited first.
- These are single-process measurements on a developer laptop. A production
  envelope still needs the panel and worker as separate processes on a
  production-like host, with HTTP request latency, as described in
  [LOAD_BASELINE_2026-09-29.md](LOAD_BASELINE_2026-09-29.md).
