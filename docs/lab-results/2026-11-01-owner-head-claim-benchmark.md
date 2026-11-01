# Durable queue claim after owner-head selection — 2026-11-01

**Change under test:** `ClaimNext` selection rewritten (F-06). The original
query tested every queued row against the eligibility rules, so its cost grew
with the backlog (see the
[2026-10-31 report](2026-10-31-queue-claim-benchmark-ec88c85.md)). The new
selection:

1. checks only the 64 oldest queued jobs in claim order
   (`jobs_state_started_idx`), which is exact because the first eligible job
   in global order is found whenever it lies in that prefix; its
   running-owner and older-queued checks are separate `NOT EXISTS` clauses so
   each is an exact seek on `jobs_owner_state_idx` (combined with `OR`,
   SQLite used only the owner prefix and walked the owner's whole queue for
   every candidate);
2. only when that whole prefix is blocked, walks the distinct owners with
   queued work (`jobs_state_owner_started_idx`) and picks the oldest eligible
   owner head.

Eligibility is unchanged: past retry backoff, no running job of the owner
with a live lease, no older queued job of the owner.
`TestClaimSelectionMatchesOrderedScanReference` compares the new selection
with the original query on 300 randomized queues (owner ties, retry backoff,
live and expired leases, completed history, kind filters, forced
fallbacks) and requires the same job every time.

**Host/toolchain:** Go 1.26.10; Linux amd64; AMD Ryzen 7 7735U, 16 logical
CPUs; SQLite on tmpfs-backed `t.TempDir()`; `GOMAXPROCS=2`; 20 samples per
row. Developer-host measurements, not a certified envelope.

## Adverse queue depth (`BenchmarkClaimNextQueueDepth`)

Ten owners each hold a running job with a live lease; the backlog is queued
behind them; one eligible job sits after the backlog.

| Queued | Before p50 / p99 (2026-10-31) | After p50 / p99 |
|---:|---:|---:|
| 1,000 | 2.1 / 2.3 ms | 1.0 / 1.9 ms |
| 10,000 | 20.6 / 21.0 ms | 0.96 / 1.8 ms |
| 100,000 | 210.3 / 220.8 ms | 1.0 / 1.3 ms |

Claim latency no longer grows with the blocked backlog.

## Owner-walk worst case (`BenchmarkClaimNextManyBlockedOwners`)

Every queued job belongs to a different owner whose earlier job is running,
so the prefix is blocked and every owner head is examined.

| Blocked owners | Owner walk p50 | Original query p50 |
|---:|---:|---:|
| 1,000 | 6.2 ms | 2.5 ms |
| 10,000 | 68.4 ms | 28.4 ms |

This shape is slower than before (about 7 µs per blocked owner). It is
largely synthetic: a running job only blocks while it holds a live lease and
running jobs are capped by `STEPANEL_MAX_CONCURRENT_JOBS` (at most 32), so
in production many blocked owners means many owners whose next job is in
retry backoff, and `STEPANEL_MAX_PENDING_JOBS` (default 10,000) bounds the
total. It is recorded here as the known trade-off.

## Mixed workload (`BenchmarkSessionValidationUnderJobLoad`, 500 samples)

| Profile | Claim p50 / p95 / p99 | Session p99 | Busy errors |
|---|---:|---:|---:|
| Target, before (2026-10-31, run 1) | 7.9 / 20.0 / 107.4 ms | 9.1 ms | 0 |
| Target, after | 1.2 / 4.0 / 4.0 ms | 0.9 ms | 0 |
| Saturated, before (run 1) | 19.1 / 57.3 / 85.1 ms | 86.0 ms | 0 |
| Saturated, after | 7.6 / 28.8 / 42.8 ms | 39.7 ms | 0 |

## Still outstanding

Sustained mixed workloads on the declared OS/filesystem stack with WAL
checkpoint measurement during concurrent writes (not only after samples),
separate panel and worker processes, and restart recovery, as listed in
[CAPACITY.md](../CAPACITY.md#what-remains-unmeasured).
