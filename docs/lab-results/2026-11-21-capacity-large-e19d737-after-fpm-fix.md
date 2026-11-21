# Rocky Linux 9 large capacity profile, after the FPM telemetry fix — 2026-11-21

Workflow: [capacity profile 44852768241](https://github.com/cyberducttape/StePanel/actions/runs/44852768241)<br>
Product source: `fd14d657` (FPM telemetry no longer wakes idle pools)<br>
Result: **failed. The fix removed the slow-success tail, but a separate host-wide stall remains.**

Compared with the [before-fix baseline](./2026-11-21-capacity-large-e19d737-before-fpm-fix.md)
on the same profile (500 PHP sites, eight sequential HTTPS clients, 600 s,
two 512 MiB backups at 150 s):

| Signal | Before fix | After fix |
| --- | ---: | ---: |
| Site requests | 9,903 | 11,810 |
| Failed (15 s first-byte timeout) | 193 (1.95%) | 271 (2.29%) |
| Successful p99, baseline | 9,283.8 ms | 112.0 ms |
| Successful p99, during backups | 6,591.8 ms | 115.7 ms |
| Sites with a live FPM sample (peak) | 460 of 504 | 504 of 504 |
| PHP-FPM RSS mean / peak | 3,373 / 4,522 MiB | 3,008 / 4,504 MiB |

## What changed

The fix did what it was meant to: successful requests no longer have
multi-second tails (p99 fell from about 9 s to about 112 ms), and every
site's pool is now reported without waking it.

## What did not change

The 15-second timeouts remain, and their pattern shows a stall that hits
every site at once. Each retained failure burst holds exactly eight
failures, one per client, within about 0.3 s, at roughly 16–18 s intervals.
Every failing site's newest FPM sample showed a live pool with no queue.
271 timeouts at 15 s add up to about 4,000 client-seconds of the 4,800 the
eight clients had, so the clients spent most of the run blocked together,
with short windows of fast responses in between. The before-fix runs show
the same synchronized bursts, so this stall predates the FPM fix and is a
separate problem. CPU averaged 20.7% and peaked at 99.3%; available memory
stayed above 7.9 GiB.

## Next measurement

Retained failures are capped at 50, so this run cannot show when stalls
start and end. The capacity probe now records a per-5-second timeline of
successes and failures by request start time. It also requests a static
file through the same web server and TLS path, with no PHP, once a second.
If the static probe stalls with the PHP pages, the stall is in the web
server or host; if it does not, it is in PHP-FPM. The next run uses both.

## Limits

One run on one runner VM; not certification. The capacity gate stays open.
