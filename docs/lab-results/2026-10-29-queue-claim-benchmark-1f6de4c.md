# Durable queue claim and WAL benchmark — 2026-10-29

**Benchmark source:** `1f6de4cbd5c90bd08cb8436af0dee0ecc6fd5046`

The session-under-job-load results use the instrumented benchmark at
`cc6de882d997121536bfb4c0763d06ebd850a680`.

**Toolchain/host:** Go 1.26.10; Linux amd64; AMD Ryzen 7 7735U; 16 logical CPUs;
14 GiB RAM; ext4 on NVMe.

**Method:** `GOMAXPROCS=2 go test -run '^$' -bench '^BenchmarkClaimNextQueueDepth$' -benchtime=100x -count=1`

The concurrent read-path method was
`GOMAXPROCS=2 go test -run '^$' -bench '^BenchmarkSessionValidationUnderJobLoad$' -benchtime=500x -count=3`.

## Workload and results

The fixture contains 1,000, 10,000, or 100,000 queued jobs. Ten owners have an
older running job with an unexpired lease, so every measured claim skips
blocked work before selecting the one eligible owner. There are 100 timed
claims per row. Re-arming the eligible row is outside the timed section. The
ordered-index variant adds a temporary `(kind,state,started_at,id)` index; it
does not change the production schema.

| Queue depth | Selection index | Mean | p50 | p95 | p99 |
|---:|---|---:|---:|---:|---:|
| 1,000 | Existing | 2.284 ms | 2.228 ms | 2.422 ms | 3.117 ms |
| 1,000 | Temporary ordered | 2.302 ms | 2.266 ms | 2.378 ms | 3.198 ms |
| 10,000 | Existing | 22.432 ms | 22.235 ms | 23.504 ms | 24.325 ms |
| 10,000 | Temporary ordered | 23.292 ms | 23.122 ms | 24.652 ms | 24.786 ms |
| 100,000 | Existing | 232.937 ms | 229.256 ms | 249.431 ms | 290.529 ms |
| 100,000 | Temporary ordered | 278.270 ms | 240.293 ms | 410.795 ms | 539.490 ms |

At 100,000 rows the production plan uses `jobs_kind_state_idx`, a correlated
owner lookup through `jobs_owner_state_idx`, and a temporary B-tree for
ordering. The temporary ordered index removes the sort but was slower in the
mean and had a substantially higher p95/p99 in this run. This does not justify
adding that index.

After each 100-claim sample, a passive WAL checkpoint reported zero busy
readers and zero remaining frames. Checkpoint duration was 94–145 microseconds.
The WAL file still occupied 4.1–72.1 MB after checkpointing; SQLite retained
the file allocation. This is an end-of-sample checkpoint observation, not a
measurement of checkpoint behavior during concurrent backup, migration, or
customer traffic.

## Session validation while the job queue is active

The companion benchmark seeds 500 simulated site owners, 20,000 completed
jobs, 400 queued jobs, and eight running jobs. Its target profile starts 50
dashboard pollers at one-second intervals and two claim workers at 100 ms
intervals. The saturated profile runs four pollers and two workers
continuously. Each row below is one of three 500-session-validation samples.

| Profile | Run | p50 | p90 | p99 | Claim errors | Busy errors | Rearm errors |
|---|---:|---:|---:|---:|---:|---:|---:|
| Idle | 1 | 71 µs | 114 µs | 166 µs | 0 | 0 | 0 |
| Idle | 2 | 66 µs | 125 µs | 162 µs | 0 | 0 | 0 |
| Idle | 3 | 86 µs | 142 µs | 187 µs | 0 | 0 | 0 |
| Target profile | 1 | 74 µs | 140 µs | 14.960 ms | 0 | 0 | 0 |
| Target profile | 2 | 77 µs | 134 µs | 8.057 ms | 0 | 0 | 0 |
| Target profile | 3 | 72 µs | 134 µs | 14.095 ms | 0 | 0 | 0 |
| Saturated | 1 | 12.384 ms | 42.063 ms | 78.448 ms | 0 | 0 | 0 |
| Saturated | 2 | 14.543 ms | 44.380 ms | 78.473 ms | 0 | 0 | 0 |
| Saturated | 3 | 13.995 ms | 41.856 ms | 91.166 ms | 0 | 0 | 0 |

All runs recorded zero claim failures, SQLite busy-classified claim errors,
job re-arm failures, and session rejections. The target profile's single-digit
to mid-teens millisecond p99 outliers and the saturated profile's roughly
78–91 ms p99 show contention that an idle latency number would hide. The
counters cover benchmark claim and re-arm errors; they are not a host-wide
SQLite busy metric.

## Interpretation and limits

The queue comparison is 100 samples from one invocation; the concurrent
session profiles have three runs of 500 samples each. All were run on a
developer workstation. The p95/p99 estimates and differences between index
variants need more repeated runs. The 100k fixture is intentionally adverse
and measures durable claim selection plus SQLite lease persistence. It does
not include 500 live sites, 100 active accounts, concurrent backups or
migrations, PHP/database traffic, abrupt reboot recovery, or sustained WAL
checkpoint pressure. It is not a capacity certification or a site-count
promise.

The benchmark now emits per-claim percentiles, passive checkpoint duration,
busy status, remaining frames, and post-checkpoint WAL size for future
reproducible comparisons. See [`CAPACITY.md`](../CAPACITY.md) and
[`CERTIFIED_ENVELOPE.md`](../CERTIFIED_ENVELOPE.md) for the still-open full
host envelope.
