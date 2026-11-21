# Rocky Linux 9 large capacity profile, before the FPM telemetry fix — 2026-11-21

Workflow: [capacity profile 44848048837](https://github.com/cyberducttape/StePanel/actions/runs/44848048837)<br>
Product source: `e19d73705166a8580a0dea7a3581dbe3c7361d01` (before `fd14d657`)<br>
Result: **failed; the before-fix baseline for the FPM telemetry fix.**

This run used the failure-correlated capacity probe. It ran on source that
still had the per-minute PHP-FPM status sweep, which woke every idle
`ondemand` pool. The comparison run on the fixed source is
[44852768241](https://github.com/cyberducttape/StePanel/actions/runs/44852768241).

The guest booted and installed (20 smoke/matrix checks). The profile created
500 PHP sites and two backup sites (setup 2,506 s), then measured eight
sequential HTTPS clients for 600 s. Two 512 MiB backups started at 150 s and
completed in 20.7 s and 45.8 s.

## Measurements

| Signal | Baseline | During backups |
| --- | ---: | ---: |
| Successful site requests | 9,141 | 569 |
| Site p50 / p95 / p99 | 52.4 / 85.2 / 9,283.8 ms | 56.1 / 88.4 / 6,591.8 ms |
| Site maximum | 15,010.3 ms | 12,755.4 ms |
| `/api/sites` p99 | 13.1 ms | |
| `/api/sites/overview` p99 | 1,149 ms | |

**193 of 9,903 site requests failed (1.95%).** Every retained failure (50 of
193) timed out after 15 s waiting for the first response byte; TCP connect
and TLS handshake were fast. The rate matches the three earlier failed runs
(2.03%, 1.88% and 2.19%).

Host CPU averaged 19.7% and peaked at 99.8%. PHP-FPM aggregate RSS averaged
3,373 MiB and peaked at 4,522 MiB. Eight sequential clients need only a few
workers at a time, so this is consistent with the status sweep keeping
hundreds of idle-pool workers alive, though RSS includes shared pages.

## Failure correlation

Each retained failure carries the site's newest periodic FPM sample. 14 of 50
failures (28%) hit a site whose last FPM status probe had failed, against a
mean of 17% of sites without a live probe at any scrape. The samples were a
median 34 s old and the probe is periodic, so this weak enrichment does not
establish cause on its own.

The cause was established off-host: a 4-CPU Rocky Linux 9 container with 500
pools from the shipped template reproduced 15-second stalls about 10 s after
each status sweep. php-src `fpm_pctl_on_socket_accept` skips spawning while
a child that was just sent SIGQUIT for idle timeout is still counted as
idle. Stopping the sweep from waking idle pools cut stalls from 5 in 5,048
to 1 in 5,184 requests in that reproduction (see `PROGRESS.md`, 2026-11-21).

## Limits

This is the pre-fix baseline, not certification. The capacity gate stays
open until the fixed-source run, and repeats of it, pass.
