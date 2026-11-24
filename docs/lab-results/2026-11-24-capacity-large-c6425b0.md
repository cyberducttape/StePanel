# Rocky Linux 9 large capacity profile after the overview rebuild fix — 2026-11-24

- Workflow: [capacity profile 45312970493](https://github.com/cyberducttape/StePanel/actions/runs/45312970493)
- Exact source: `c6425b05` (site overview lists route and proxy directories once per rebuild)
- Host: Rocky Linux 9 disposable KVM guest on a GitHub runner; 500 PHP sites plus two backup sites; eight sequential HTTPS clients; 600 s measured; two 512 MiB backups started at 150 s.
- Artifact: `capacity-large-c6425b05…`, attached to the run.

**Result: passed.** 71,606 site page requests, 0 failed; 0 of 564 static probes failed; 0 PHP-FPM master warnings; 0 control-plane database-lock errors.

## Site traffic

| Window | Requests | p50 ms | p95 ms | p99 ms | max ms | Errors |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| baseline | 66,471 | 62.8 | 93.6 | 114.1 | 8,559.9 | 0 |
| during backups | 5,135 | 65.8 | 100.4 | 126.1 | 188.7 | 0 |

Rare multi-second time-to-first-byte outliers remain in the baseline window (maximum 8.5 s against a p99 of 74 ms), consistent with the php-fpm ondemand idle-worker race noted in the [descriptor-fix report](./2026-11-21-capacity-large-06f6e94-fpm-descriptor-fix.md). No runtime default was changed for it.

## Administrative API

The change under test removed a quadratic directory scan from the site overview rebuild (a 500-site local benchmark fell from 594 ms to 39 ms per rebuild). Against the previous passing run [44897996332](https://github.com/cyberducttape/StePanel/actions/runs/44897996332) on `3def0129`:

| `/api/sites/overview`, baseline | `3def0129` | `c6425b05` |
| --- | ---: | ---: |
| p50 | 3.2 ms | 2.7 ms |
| p95 | 302.5 ms | 50.7 ms |
| p99 | 665.8 ms | 134.7 ms |
| max | 1,361.9 ms | 549.9 ms |

`/api/sites`, `/api/jobs`, and `/readyz` stayed below 62 ms maximum with no errors. Both backups completed (21.3 s and 44.6 s; queue wait under 0.4 s).

## Host

| Signal | Mean | Max |
| --- | ---: | ---: |
| CPU | 42.4% | 99.5% |
| PHP-FPM summed RSS (shared pages counted per process) | 8,890 MiB | 9,398 MiB |
| PHP-FPM master descriptors | 1,540 | 1,596 of 65,536 |
| PHP-FPM live children | 516 | 544 |
| Minimum available memory | | 6,906 MiB |

## What this does not show

This is the third passing 500-site run on one runner VM type and the first on this source; it is diagnostic evidence, not certification. It does not exercise the 2,000-domain or 100-account targets in [CERTIFIED_ENVELOPE.md](../CERTIFIED_ENVELOPE.md), WordPress or database application load, migrations, restart or recovery under load, or a memory ceiling per site count.
