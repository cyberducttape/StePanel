# Control-plane microbenchmark baseline

Date: 2026-09-29
Commit state: worktree with uncommitted readiness changes
Host: Linux 7.0.0-34-generic x86_64, AMD Ryzen 7 7735U with Radeon Graphics
Go: go1.26.7

Command:

```sh
TMPDIR=/var/tmp go test . -run '^$' -bench 'Benchmark(ControlPlaneSiteMutations|ControlPlanePanelWorkerContention|ControlPlaneFiftyBackupRestoreJobs|ControlPlaneAuditHeavyMutations)$' -benchtime=1s -count=1
```

Results from one local run:

| Workload | Result |
| --- | ---: |
| 10 sites, 10 concurrent mutators | 454,779 ns/op |
| 100 sites, 10 concurrent mutators | 443,409 ns/op |
| 500 sites, 10 concurrent mutators | 425,956 ns/op |
| Panel/worker-style contention, two SQLite pools and 10 mutators | 874,318 ns/op |
| Two actual Go test processes concurrently updating the same WAL database, process A | 1,330,834 ns/op |
| Two actual Go test processes concurrently updating the same WAL database, process B | 346,257 ns/op |
| Enqueue, claim, and complete a 50-job backup/restore batch | 90,636,812 ns/batch (650 jobs/batch) |
| Audit-heavy mutations, 10 concurrent writers | 812,121 ns/op |

These are synthetic SQLite/control-plane microbenchmarks, not HTTP latency,
filesystem backup/restore throughput, or a production capacity certification.
The cross-process results came from two concurrent `go test` processes sharing
one SQLite file; their asymmetry reflects scheduling and is not a stable
per-process service-level estimate. The database reported a 5-second SQLite
busy timeout. Results vary with CPU, filesystem, SQLite build, and host load. A
production envelope still requires repeated runs on a disposable production-like
host, including two actual panel/worker processes, request latency, queue age,
lock wait time, backup/restore throughput, and recovery measurements.
