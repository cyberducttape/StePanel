# Durable queue claim measurements — 2026-10-31

**Production source under test:** `ec88c85d207115c61c34d921ae6725acfc9b58c9`.
The benchmark-only change adds direct claim latency percentiles to the existing
multi-worker load fixture; no production query or schema changed for this run.

**Toolchain/host:** Go 1.26.10; Linux amd64; AMD Ryzen 7 7735U with Radeon
Graphics; 16 logical CPUs; 14 GiB RAM; SQLite database on the local workspace
filesystem. `GOMAXPROCS=2`.

**Methods:**

```sh
GOMAXPROCS=2 go test -run '^$' -bench '^BenchmarkClaimNextQueueDepth$' -benchtime=30x -count=1 .
GOMAXPROCS=2 go test -run '^$' -bench '^BenchmarkSessionValidationUnderJobLoad/(target_500_sites_50_viewers_2_workers|saturated_4_pollers_2_workers)$' -benchtime=500x -count=3 .
```

The adverse queue fixture seeds 1,000, 10,000, or 100,000 queued jobs; ten
owners have older running jobs with live leases, forcing the ordered scan past
blocked work to one eligible owner. Each timed claim is followed by re-arming
that eligible row outside the timed section. The temporary ordered-index
variant adds `(kind,state,started_at,id)` only to its benchmark database.

## Queue depth and query plan

| Queued | Index variant | Mean | p50 | p95 | p99 |
|---:|---|---:|---:|---:|---:|
| 1,000 | Production indexes | 2.120 ms | 2.103 ms | 2.177 ms | 2.250 ms |
| 1,000 | Temporary ordered index | 2.159 ms | 2.144 ms | 2.272 ms | 2.378 ms |
| 10,000 | Production indexes | 20.606 ms | 20.586 ms | 20.897 ms | 21.043 ms |
| 10,000 | Temporary ordered index | 21.287 ms | 21.250 ms | 21.636 ms | 21.993 ms |
| 100,000 | Production indexes | 211.122 ms | 210.297 ms | 218.867 ms | 220.819 ms |
| 100,000 | Temporary ordered index | 223.065 ms | 216.939 ms | 260.384 ms | 271.728 ms |

At 100,000 rows, `EXPLAIN QUERY PLAN` reports the production candidate lookup
through `jobs_kind_state_idx`, the correlated owner check through
`jobs_owner_state_idx`, and a temporary B-tree for ordering. The temporary
ordered index removes the sort but is slower in this sample, including p95 and
p99. These results do not justify changing the production indexes.

Passive WAL checkpoints after the single-worker queue-depth runs reported no
busy reader and zero remaining frames. They took 117–1,430 microseconds in
this run; the WAL file remained allocated at approximately 3.5–72.2 MB. These
are post-sample checkpoints, not checkpoint measurements during sustained
concurrent writes.

## Multi-worker SQLite contention

The shared-database workload contains 20,000 completed jobs, 400 queued jobs,
eight running jobs, and 500 simulated owners. It runs four job-list pollers
and two claim/re-arm workers. Target mode paces pollers at one second and
workers at 100 ms; saturated mode runs them continuously. Each run performs
500 session checks while collecting the latency of each `ClaimNext` call.

| Profile | Run | Claim samples | Claim p50 | Claim p95 | Claim p99 | Claim errors / busy | Session p99 |
|---|---:|---:|---:|---:|---:|---:|---:|
| Target | 1 | 20 | 7.899 ms | 20.027 ms | 107.397 ms | 0 / 0 | 9.077 ms |
| Target | 2 | 19 | 7.765 ms | 49.487 ms | 49.487 ms | 0 / 0 | 8.106 ms |
| Target | 3 | 20 | 8.188 ms | 55.027 ms | 103.027 ms | 0 / 0 | 13.714 ms |
| Saturated | 1 | 473 | 19.127 ms | 57.298 ms | 85.054 ms | 0 / 0 | 86.004 ms |
| Saturated | 2 | 488 | 19.414 ms | 54.880 ms | 74.602 ms | 0 / 0 | 79.489 ms |
| Saturated | 3 | 490 | 17.295 ms | 49.867 ms | 62.749 ms | 0 / 0 | 68.174 ms |

No claim, SQLite busy-classified, job re-arm, or session-rejection errors were
observed. Target-mode claim percentiles are based on only 19–20 samples and
are too small for a stable p95/p99 estimate. The saturated mode yields roughly
470–490 claim samples per run and shows queue contention: claim p99 was
62.7–85.1 ms and session p99 was 68.2–86.0 ms.

## Conclusion and limits

The 100k adverse queue claim is measurable at roughly 211 ms mean on this
developer workstation. Under the tested two-worker/two-poller saturation,
claims complete without SQLite busy errors, but tail latency rises into the
roughly 63–85 ms range. The temporary ordering index did not improve the
adverse queue case, so no production optimization is proposed from these
results alone.

This is repeatable local microbenchmark evidence, not a certified capacity
envelope. The fixture does not run real site workloads, backup/migration I/O,
PHP/database traffic, host power loss, or long-duration WAL pressure. The
SQLite connection pool is configured with one open connection, so it measures
StePanel's current single-connection coordination model rather than multiple
independent database writers. The installed-host and sustained-load gates
remain open in [`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md).
