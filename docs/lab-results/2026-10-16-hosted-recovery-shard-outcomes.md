# Hosted Recovery Shard Outcomes — 2026-10-16

## Result

The three completed two-repeat shards did **not** satisfy the repeated-host
release gate. Each shard had five successful OS/web-server jobs and one failed
job. The failures occurred in the cPanel-move smoke's administrator login and
returned HTTP 401 before an import request was made.

| Shard | Source | Failed host job | Job | Result |
|---|---|---|---:|---|
| [01](https://github.com/cyberducttape/StePanel/actions/runs/38830877337) | `60f96ee` | Ubuntu 24.04 / Apache / MariaDB | `116749123119` | Login HTTP 401; other five host jobs passed both iterations |
| [02](https://github.com/cyberducttape/StePanel/actions/runs/38830880739) | `60f96ee` | Rocky Linux 9 / Caddy / MariaDB | `116749134250` | Login HTTP 401; other five host jobs passed both iterations |
| [03](https://github.com/cyberducttape/StePanel/actions/runs/38830884355) | `60f96ee` | Ubuntu 24.04 / Caddy / MariaDB | `116749146945` | Login HTTP 401; other five host jobs passed both iterations |

The diagnostic artifacts show the cPanel smoke login was rejected before the
import endpoint ran. The affected script computed a single TOTP and treated any
401 as fatal. The other five OS/web-server jobs in each shard completed both
configured recovery iterations. This is useful per-host evidence, but the
failed cells mean none of these three six-host shards is a passing cohort.
These runs used older source (`60f96ee`), not the current smoke retry change.

## Follow-up and validation state

Commit `3be9b99` updates the smoke harness to retry exactly one HTTP 401 after
waiting for a fresh TOTP counter and clearing the cookie jar. Rate limits,
transport errors, non-401 responses, and a second 401 remain fatal. Product
authentication behavior was not changed.

On `3be9b99`, hosted CI ([run 38843586085](https://github.com/cyberducttape/StePanel/actions/runs/38843586085)),
CodeQL ([run 38843585904](https://github.com/cyberducttape/StePanel/actions/runs/38843585904)),
quota-enabled installation smoke ([run 38843586000](https://github.com/cyberducttape/StePanel/actions/runs/38843586000)),
and N-1 upgrade smoke ([run 38882203108](https://github.com/cyberducttape/StePanel/actions/runs/38882203108))
passed. The standard six-host installation smoke ([run 38871962287](https://github.com/cyberducttape/StePanel/actions/runs/38871962287))
was still queued at report time; therefore the harness retry has not yet been
verified in that installed-host workflow.

## Gate disposition

No complete 20–50-repeat, same-source six-host recovery cohort has passed.
These runs do not prove product authentication is defective: they demonstrate
an intermittent failure in the test's login step and do not reach the import
workflow for the failing jobs. Do not count the three shards as successful
release certification. The retry change still requires a completed hosted
installed-host run, followed by a complete repeated cohort on a single source
revision. Other Gate 5 requirements, including physical host power-loss and
broader ENOSPC evidence, remain open.
