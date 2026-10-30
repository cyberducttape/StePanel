# Capacity evidence and limits

**Status: measured components; no production capacity envelope is certified.**

The proposed single-host target is documented in
[`CERTIFIED_ENVELOPE.md`](CERTIFIED_ENVELOPE.md). It is a planning target, not
a supported limit. Production claims still require the full host, application,
recovery, and resource evidence defined there.

## Durable job-claim latency

The current-source high-backlog claim experiment is recorded in the
[2026-10-29 queue and WAL benchmark report](lab-results/2026-10-29-queue-claim-benchmark-1f6de4c.md).
On a Linux workstation (AMD Ryzen 7 7735U, Go 1.26.10), 100,000 queued jobs
arranged behind ten owners with unexpired running leases measured p50/p95/p99
claim latency of 229/249/291 ms with existing indexes. A temporary ordered
index measured 240/411/539 ms in one 100-sample run, so it does not justify a
production schema change. The earlier repeated experiment is retained in the
[2026-10-18 report](lab-results/2026-10-18-queue-claim-benchmark-a8d0340.md).
These pathological fixtures support regression comparisons, not production
throughput promises.

Other measurements cover session validation and job listing at
[`lab-results/2026-10-02-control-plane-load-baseline.md`](lab-results/2026-10-02-control-plane-load-baseline.md), and synthetic HTTP
mixes at [`lab-results/2026-10-02-http-load-baseline.md`](lab-results/2026-10-02-http-load-baseline.md).
They are short developer-host or CI measurements and do not represent a
certified workload envelope.

A current-source, two-process SQLite write probe is recorded in the
[cross-process contention report](lab-results/2026-10-28-sqlite-process-contention-34045cb.md).
Both processes completed 100,000 writes without busy errors on a shared WAL
database. The run used an unsupported Ubuntu 26.04 developer host and tmpfs;
it does not establish checkpoint behavior, killed-process recovery, or a
production capacity limit.

## What remains unmeasured

Before publishing site, account, domain, or concurrent-job ceilings, run the
complete envelope on the declared OS/filesystem/database stack. Include
separate panel and worker processes; sustained scheduled jobs; concurrent
backups, imports, restores, and PHP/database customer traffic; CPU, memory,
disk and inode headroom; SQLite contention; queue age and claim latency; and
restart/failure recovery. Record duration, sample counts, host specifications,
workload generator, raw artifacts, and excluded features. Repeat on each
supported platform combination. Until then, the platform remains uncertified.
