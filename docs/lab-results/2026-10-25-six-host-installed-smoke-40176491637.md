# Six-host installed-system smoke — 2026-10-25

**Application source:** `070dce9eeee18dd075596c2fe0561112d45db2e5`  
**Result:** PASS for all six listed host combinations, with one recovery-matrix
iteration per combination. This is not production certification.

| Item | Value |
| --- | --- |
| GitHub Actions run | [40176491637](https://github.com/cyberducttape/StePanel/actions/runs/40176491637) |
| Integration CI | [40158267549](https://github.com/cyberducttape/StePanel/actions/runs/40158267549) — passed |
| Documentation | [40158267587](https://github.com/cyberducttape/StePanel/actions/runs/40158267587) — passed |
| CodeQL | [40158267505](https://github.com/cyberducttape/StePanel/actions/runs/40158267505) — passed |
| Test host | Disposable systemd containers on GitHub-hosted Ubuntu runners |
| Started / finished | 2026-10-25 08:24:37–08:53:39 UTC |
| Database | MariaDB on all six hosts |
| Recovery-matrix repetitions | 1 per host combination |

## Results

All six installation jobs and their full smoke steps passed:

| Distribution | Web server | WordPress application recovery RTO |
| --- | --- | ---: |
| Ubuntu 24.04 | Apache | 5.480 s |
| Ubuntu 24.04 | Caddy | 6.316 s |
| AlmaLinux 9 | Apache | 5.154 s |
| AlmaLinux 9 | Caddy | 5.082 s |
| Rocky Linux 9 | Apache | 4.109 s |
| Rocky Linux 9 | Caddy | 6.479 s |

Each host completed the installed WordPress restore and application HTTP proof,
then passed one recovery-matrix iteration. The smoke also exercised the Caddy
isolated-view/no-follow route lifecycle and cleanup, MariaDB and broker outage
handling, SFTP/FTPS workflows, account suspension, deployment safeguards, and
application lifecycle operations as applicable to the host.

## Limits

- The hosts are disposable systemd containers, not dedicated VMs or physical
  machines. This does not establish physical host power-loss recovery or
  storage-controller failure behavior.
- One iteration per combination does not meet the required 20–50 repeats per
  operation/fault combination.
- The matrix covers the six listed OS/web-server combinations with MariaDB; it
  does not certify other database engines, PHP versions, filesystems, or
  alternate SELinux configurations.
- This run does not certify broad host ENOSPC behavior, capacity, live alert
  outage/retry delivery, upgrade rollback, or hostile multi-tenant isolation.

Production remains **NO-GO**. See
[V1_PRODUCTION_GATES.md](../V1_PRODUCTION_GATES.md) for the release contract.
