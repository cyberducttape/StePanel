# SQLite cross-process contention probe

**Source:** `34045cb895fa54a0e58c9bc66bdf618a0eea1c6d`  
**Run:** 2026-10-28 UTC  
**Status:** Exploratory developer-host measurement; not a capacity certification.

## Environment

- Ubuntu 26.04, Linux 7.0.0-38-generic, x86_64 (not a supported production OS
  in the current support matrix)
- AMD Ryzen 7 7735U, 16 logical CPUs; Go 1.26.10
- 14 GiB RAM; 511 MiB swap, fully used at measurement time
- Fresh SQLite database on `/tmp` (tmpfs), WAL mode, two independent Go test
  processes, each opening its own one-connection pool
- `PRAGMA busy_timeout=5000`; 100,000 fixed-count writes per process to
  separate state rows in the same database

## Results

| Process | Writes | Average write latency | Benchmark process wall time | Outcome |
| --- | ---: | ---: | ---: | --- |
| A | 100,000 | 182.7 µs/op | 18.35 s | Passed; all row revisions persisted |
| B | 100,000 | 159.8 µs/op | 16.01 s | Passed; all row revisions persisted |

Neither process reported `SQLITE_BUSY` or another write error. The database
file was 290,816 bytes after both processes closed; the WAL file had been
checkpointed/removed by close. This post-run size does not measure peak WAL
growth or checkpoint latency.

The benchmark measures serialized point updates to a small SQLite table. It
does not exercise StePanel HTTP handlers, large payloads, long transactions,
simultaneous backup I/O, customer traffic, production storage, or a sustained
queue. The reported latency is an average, not a tail-latency distribution.
The host's fully used swap and unsupported OS further limit interpretation.

## Not established by this run

- Transaction-duration and p95/p99 write-lock wait distributions
- WAL growth under sustained load, checkpoint duration, or disk-pressure
  behavior
- Recovery after killing either process during an active write
- Behavior on supported Ubuntu, Debian, AlmaLinux, or Rocky Linux releases and
  their production filesystems
- A supported sites/jobs/concurrency envelope

The result adds evidence for one narrow shared-database workload only. The
production capacity and failure-recovery gates remain open.
