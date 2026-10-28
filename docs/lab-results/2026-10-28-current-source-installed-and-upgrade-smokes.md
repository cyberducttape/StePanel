# Current-source installed and upgrade smoke evidence — 2026-10-28

**Application source:** [`d6234fe4cf310b6ce3246d414104c3ccdf866be6`](https://github.com/cyberducttape/StePanel/commit/d6234fe4cf310b6ce3246d414104c3ccdf866be6)

This report records successful disposable-host and upgrade checks against the
same source revision. These results advance individual release gates; they do
not certify StePanel for production. Production remains **NO-GO**.

## Results

| Check | Result | Evidence |
| --- | --- | --- |
| Repository checks | PASS | `make check` completed locally on this commit: `go vet ./...` and `GOMAXPROCS=2 go test -p 1 -timeout 10m ./...`; root package completed in 68.884 seconds. |
| Installed host smoke | PASS, 1 host | [Run 40610894156](https://github.com/cyberducttape/StePanel/actions/runs/40610894156), [Ubuntu 24.04 / Apache / MariaDB job](https://github.com/cyberducttape/StePanel/actions/runs/40610894156/job/122562032662). Includes installed WordPress restore and HTTP application proof. |
| XFS quota and installed-host recovery | PASS, 1 QEMU guest | [Run 40607361392](https://github.com/cyberducttape/StePanel/actions/runs/40607361392), [quota-install job](https://github.com/cyberducttape/StePanel/actions/runs/40607361392/job/122550095033). Ubuntu 24.04, XFS `/var/www`, user and project quota accounting/enforcement, quota-enabled `/home`, Caddy, PHP-FPM and MariaDB. |
| Upgrade and rollback matrix | PASS, 6/6 jobs | [Run 40610530756](https://github.com/cyberducttape/StePanel/actions/runs/40610530756). Published v0.6.0 and v0.7.0 fixtures upgraded to this candidate in disposable Ubuntu 24.04 systemd containers and QEMU guests, with uninterrupted and verification-interrupted paths. |
| Security fuzzing | PASS | [Run 40612056944](https://github.com/cyberducttape/StePanel/actions/runs/40612056944). |
| CodeQL | PASS, no open alerts | [Run 40606917724](https://github.com/cyberducttape/StePanel/actions/runs/40606917724). |

## XFS and recovery observations

- The installed smoke enabled a 1 MiB Git release-stage project quota. A first
  700 KiB write succeeded; the second write was rejected by the filesystem
  with `No space left on device`, confirming aggregate enforcement rather
  than only per-file limits.
- The workflow abruptly killed the QEMU guest, restarted it with its persistent
  disks, and verified that systemd services, `/readyz`, and XFS quota accounting
  and enforcement recovered. This is abrupt guest-loss evidence, not physical
  host power-loss evidence.
- On a separate 96 MiB ext4 volume, the real-ENOSPC backup and file-restore
  recovery tests passed. Those tests do not cover ENOSPC during database shadow
  restore, publication, or every import boundary.
- Installed WordPress recovery on the XFS guest restored a populated database
  and filesystem, passed the HTTP application probe, and measured an RTO of
  114.511 seconds.
- The N-1 matrix passed both published source fixtures, but it is not a full
  upgrade certification across all supported distributions, populated
  production data sets, or every interruption point.

## Remaining scope

At report time, the exact-source [20-repeat Gate 5 cohort](https://github.com/cyberducttape/StePanel/actions/runs/40607212197)
was still running. Host selection passed; its first six-host repetition had
not completed, so zero repetitions are counted here. Physical host power loss,
database-restore ENOSPC, live notification outage/retry, repeated installed
recovery, and a certified capacity envelope remain open. See
[`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md) for the authoritative
gate status.
