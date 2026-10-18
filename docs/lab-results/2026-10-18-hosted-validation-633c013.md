# Exact-head hosted validation — 2026-10-18

**Source:** `633c013ce1dd2480928487c0fcf0b7f3aecf4b62`  
**Result:** CI, CodeQL, standard installed-host smoke, and quota-enabled smoke passed.

## Exact-head checks

- [CI](https://github.com/cyberducttape/StePanel/actions/runs/39147354411)
  passed on Go 1.26.9, including formatting, release checks, vet, full tests,
  coverage floors, race tests, repository recovery drills, mixed-load tests,
  MySQL 8.0 and PostgreSQL restore exercises, Python deployment, static
  analysis, container checks, and documentation validation.
- [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39147354339)
  passed.
- [Standard installed-host smoke](https://github.com/cyberducttape/StePanel/actions/runs/39147354348)
  passed all six combinations: Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9
  with Apache and Caddy, using MariaDB. Each host restored a populated
  WordPress database, verified the application over HTTP, recovered from a
  database outage, and passed one interruption-matrix iteration. The measured
  WordPress proof RTOs from the uploaded host logs were:

  | Host | RTO |
  | --- | ---: |
  | AlmaLinux 9 / Caddy | 5.538 s |
  | AlmaLinux 9 / Apache | 5.269 s |
  | Rocky Linux 9 / Caddy | 4.977 s |
  | Rocky Linux 9 / Apache | 4.792 s |
  | Ubuntu 24.04 / Caddy | 5.195 s |
  | Ubuntu 24.04 / Apache | 4.269 s |

- [Quota-enabled install smoke](https://github.com/cyberducttape/StePanel/actions/runs/39147354334)
  passed on the disposable Ubuntu guest with the quota fixture enabled.

The six-host run adds six successful single-run application proofs. The
repository had nine prior six-host smoke runs recorded; this makes 60
single-run proofs across ten runs, not 20 repetitions per configuration and
not production capacity certification.

## Twenty-repeat installed-host cohort

Ten uniquely keyed two-iteration shards were dispatched against this exact
source SHA. At the initial capture, shards 01 and 02 were running all six host
jobs and shards 03–10 were queued. No repetition is counted until all six host
jobs in its shard finish successfully.

| Shard | Run | Initial state |
| --- | --- | --- |
| 01 | [39158683549](https://github.com/cyberducttape/StePanel/actions/runs/39158683549) | In progress |
| 02 | [39158685481](https://github.com/cyberducttape/StePanel/actions/runs/39158685481) | In progress |
| 03 | [39158687456](https://github.com/cyberducttape/StePanel/actions/runs/39158687456) | Queued |
| 04 | [39158689282](https://github.com/cyberducttape/StePanel/actions/runs/39158689282) | Queued |
| 05 | [39158691457](https://github.com/cyberducttape/StePanel/actions/runs/39158691457) | Queued |
| 06 | [39158694231](https://github.com/cyberducttape/StePanel/actions/runs/39158694231) | Queued |
| 07 | [39158696555](https://github.com/cyberducttape/StePanel/actions/runs/39158696555) | Queued |
| 08 | [39158698690](https://github.com/cyberducttape/StePanel/actions/runs/39158698690) | Queued |
| 09 | [39158700925](https://github.com/cyberducttape/StePanel/actions/runs/39158700925) | Queued |
| 10 | [39158703306](https://github.com/cyberducttape/StePanel/actions/runs/39158703306) | Queued |

The prior `a27becc` cohort has completed with eight shards passing and two
failing. Its eight successful shards represent 16 successful repetitions per
host configuration; the two failed shards contribute none, so it does not
meet the 20-repeat gate. It is not combined with the current-source cohort.

## Limits and open gates

This validation does not establish physical host power-loss recovery, ENOSPC
behavior during database shadow loads/publication/import, broker death during
an in-flight mutation, live SMTP/webhook outage recovery, a certified capacity
envelope, unsupported OS/web-server/database combinations, or independent
security review. Production remains **NO-GO** until every P0 release gate is
closed with reproducible evidence.
