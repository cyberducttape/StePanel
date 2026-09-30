# Control-plane SQLite load measurements

The Go benchmarks in `controlplane_benchmark_test.go` give a repeatable
baseline for the SQLite access patterns that currently serialize through the
control-plane database. They deliberately leave `SetMaxOpenConns(1)` unchanged.
These are database-path benchmarks, not HTTP endpoint latency or backup-file
throughput tests.

Run the in-process scenarios with:

```sh
go test -run '^$' -bench '^BenchmarkControlPlane' -benchmem -benchtime=2s -count=1
```

For actual panel/worker process contention, initialize a fresh shared database
and barrier directory, then start two copies of the process benchmark with the
same environment:

```sh
bench_dir=$(mktemp -d /tmp/stepanel-sqlite-bench.XXXXXX)
mkdir "$bench_dir/barrier"
export STEPANEL_BENCH_DB="$bench_dir/shared.db"
export STEPANEL_BENCH_BARRIER="$bench_dir/barrier"
go test -run '^$' -bench '^BenchmarkControlPlaneProcessContention$' -benchtime=5s -count=1 >"$bench_dir/panel.log" 2>&1 & panel_pid=$!
go test -run '^$' -bench '^BenchmarkControlPlaneProcessContention$' -benchtime=5s -count=1 >"$bench_dir/worker.log" 2>&1 & worker_pid=$!
wait "$panel_pid"
wait "$worker_pid"
cat "$bench_dir/panel.log" "$bench_dir/worker.log"
```

## Baseline

Measured 2026-09-29 on Linux/amd64, AMD Ryzen 7 7735U, Go benchmark default
`GOMAXPROCS=16`, modernc SQLite, local temporary filesystem. The test binary
reported roughly 33 microseconds per concurrent tenant-row update for all
three site counts. Results from one 2-second run:

| Workload | Result | Interpretation |
| --- | ---: | --- |
| 10 sites, 10 concurrent mutators | 35.17 µs/op (~28.4k ops/s) | SQL row-update proxy; not the HTTP API handler |
| 100 sites, 10 concurrent mutators | 33.81 µs/op (~29.6k ops/s) | Same proxy and concurrency |
| 500 sites, 10 concurrent mutators | 33.69 µs/op (~29.7k ops/s) | No measurable site-count slope for indexed point updates |
| Two one-connection pools (panel/worker proxy) | 44.83 µs/op (~22.3k ops/s) | Same-process separate pools; cross-process result below is more representative |
| 25 backup + 25 restore jobs, enqueue/claim/complete | 26.52 ms/batch (~1.89k completed jobs/s) | Durable SQL job lifecycle; backup/restore payload work excluded |
| Audit-outbox writes and delivery | 126.72 µs/event (~7.89k events/s) | Includes durable enqueue, outbox flush, and signed audit publication to a temporary sink |

The real two-process writer run is not yet stable enough to claim a capacity
figure. In two synchronized runs, a writer returned `SQLITE_BUSY` after
5.009 seconds despite `PRAGMA busy_timeout` reading 5000 ms. One other 5-second
run passed but showed highly asymmetric rates (about 1.6k versus 36k
updates/s). This is a correctness/reliability signal, not a passing load
result. Do not use the in-process measurements to certify 100+ site production
capacity. The process-level failure needs root-cause analysis, repeated runs,
and measurement on the target storage and deployment hardware before that
claim is made.

The current suite does not model real HTTP authentication/validation, state-blob
serialization size, simultaneous backup I/O, 50 workers executing real backup
and restore operations, or audit bursts against production storage. In
particular, the 50-job scenario measures 50 durable jobs in a queue-and-drain
batch rather than 50 simultaneously running backup/restore tasks.
