# Durable queue claim benchmark — 2026-10-18

**Source:** `a8d03403f415e5f73a456e60928e6bfe8bfb8f48`

**Toolchain/host:** Go 1.26.9; Linux x86_64; AMD Ryzen 7 7735U, 16 threads

**Method:** `go test -run '^$' -bench '^BenchmarkClaimNextQueueDepth/queued_100000' -benchtime=5x -count=3 .`
and `go test -run '^$' -bench '^BenchmarkClaimNextQueueSelectionStrategies$' -benchtime=5x -count=3 .`

## Workload and results

The synthetic fixture contains 100,000 queued `site.backup` jobs divided among
ten owners whose oldest jobs are running with unexpired leases, followed in
queue order by one eligible job. Each timed claim must preserve owner ordering
and skip all blocked work. Setup and re-arming the eligible job are outside the
timed section. Each reported sample contains five claim operations; three
independent benchmark processes were run.

| Selection plan | Run 1 | Run 2 | Run 3 | Median |
|---|---:|---:|---:|---:|
| Existing indexes | 230.801 ms | 232.842 ms | 231.758 ms | 231.758 ms |
| Temporary `(kind,state,started_at,id)` index | 235.117 ms | 236.719 ms | 238.521 ms | 236.719 ms |
| Ordered candidate scan | 226.418 ms | 224.712 ms | 225.147 ms | 225.147 ms |
| Grouped owner-head CTE | 291.432 ms | 290.041 ms | 291.243 ms | 291.243 ms |

The production query plan uses `jobs_kind_state_idx`, a correlated owner
subquery using `jobs_owner_state_idx`, and a temporary B-tree for ordering.
The temporary ordered index removes that sort, but did not improve elapsed
time in this workload. The grouped CTE adds a temporary grouping B-tree,
correlated subqueries, and an automatic covering index, and was slower. Neither
variant justifies a production schema/query change based on this result.

An exploratory one-sample run measured 8.1 ms at 1,000 queued rows and 47.3 ms
at 10,000 rows with the current indexes. Those figures are directional only;
they were not repeated and should not be used as capacity guarantees.

## Interpretation and limits

This is a deliberately adverse queue-ordering workload, not an expected
steady-state rate or a production capacity claim. The 100k result shows that
owner serialization plus a large backlog can add hundreds of milliseconds to
claim latency, and that a simple ordering index does not resolve it. A durable
owner-head structure or equivalent algorithm may be worth evaluating, but any
change must preserve retries, leases, cross-kind owner ordering, and atomic
concurrent claims, with contract and race tests before adoption.

These measurements do not establish mixed-workload throughput, HTTP latency,
or a certified host/site envelope. See [`CAPACITY.md`](../CAPACITY.md) and
[`CERTIFIED_ENVELOPE.md`](../CERTIFIED_ENVELOPE.md).
