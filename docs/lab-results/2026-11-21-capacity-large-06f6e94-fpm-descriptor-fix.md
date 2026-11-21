# Rocky Linux 9 large capacity profile: PHP-FPM descriptor exhaustion found and fixed — 2026-11-21

Profile: 500 PHP sites on a 4-CPU, 10 GiB KVM guest, eight sequential HTTPS
clients for 600 s, two 512 MiB backups started at 150 s.

| Run | Source | Site requests | Failed | Successful p99 | Result |
| --- | --- | ---: | ---: | ---: | --- |
| [44880222946](https://github.com/cyberducttape/StePanel/actions/runs/44880222946) | `0ff03909`, static probe added | 11,487 | 269 | 105.7 ms | stall located in PHP |
| [44886286422](https://github.com/cyberducttape/StePanel/actions/runs/44886286422) | `5ad16f3f`, descriptor diagnostics, no fix | 11,436 | 272 | 111.4 ms | cause confirmed |
| [44886417491](https://github.com/cyberducttape/StePanel/actions/runs/44886417491) | `06f6e94d`, limit raised | **81,362** | **0** | 97.1 ms baseline, 115.0 ms during backups | pages pass; see readiness note |

## Locating the stall

A static file served by the same web server over the same TLS path, with no
PHP, never failed (0 of 572 requests), while PHP pages timed out. The
per-5-second timeline showed about 2.5 s of fast requests reaching roughly
330 different sites, then all eight clients hung together until their 15 s
timeout, repeating every 17–18 s.

## Cause

One PHP-FPM master serves every site's pool. It holds a listening socket
per pool and a pair of log pipes per worker. The PHP-FPM unit ran with the
systemd default soft limit of 1,024 open files, so 500 pools left room for
only a few hundred live workers. The diagnostic run logged
`failed to prepare the stderr pipe: Too many open files (24)` 279 times
during the measurement, and peak live workers stopped at 257.

When a fork fails, the request waits in the pool's listen queue. The
ondemand accept event is edge-triggered, so PHP-FPM does not try again until
another request reaches the same pool; with 500 sites that rarely happens
within the client's 15 s timeout. Workers reaped after their 10 s idle
timeout freed descriptors, which produced the repeating cycle.

## Fix

The installer now sets `LimitNOFILE=65536` in the existing PHP-FPM drop-in
and restarts PHP-FPM when that drop-in changes, which also covers upgrades.
The install smoke checks the running master's limit before restarting it
itself (six-host [install smoke 44886416024](https://github.com/cyberducttape/StePanel/actions/runs/44886416024) passed).

After the fix the master peaked at 1,622 open descriptors of 65,536 with
557 live workers. There were no PHP-FPM master warnings, no page failures,
and the eight clients completed about seven times as many requests, because
they no longer spent most of the run blocked.

## Readiness note

The fixed run was still marked failed by one `/readyz` 503 at second 0.0:
a scheduled off-site target probe was in progress as measurement began.
That is a real, transient readiness state, not a page failure. The probe now
waits (up to 180 s) for the host to report ready before measuring; any
readiness failure after that point still fails the run.

## What this does not show

- Memory: with every worker now able to start, summed PHP-FPM RSS averaged
  9.1 GiB (shared pages are counted once per process, so this overstates
  real use); minimum available memory was 6.8 GiB of 10 GiB. A memory
  ceiling per site count has not been measured.
- One run on one runner VM. No site count is certified; the capacity gate
  stays open until repeated passing runs.
