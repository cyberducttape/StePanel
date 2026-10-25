# AlmaLinux 9 Caddy installed-host smoke — 2026-10-25

**Application source:** `070dce9eeee18dd075596c2fe0561112d45db2e5`  
**Result:** PASS for this installed-host smoke and one recovery-matrix
iteration. This is not production certification.

| Item | Value |
| --- | --- |
| GitHub Actions run | [40158267578](https://github.com/cyberducttape/StePanel/actions/runs/40158267578) |
| Integration CI | [40158267549](https://github.com/cyberducttape/StePanel/actions/runs/40158267549) — passed |
| Documentation | [40158267587](https://github.com/cyberducttape/StePanel/actions/runs/40158267587) — passed |
| CodeQL | [40158267505](https://github.com/cyberducttape/StePanel/actions/runs/40158267505) — passed |
| Host combination | AlmaLinux 9, Caddy, PHP-FPM, MariaDB |
| Test host | Disposable systemd container on a GitHub-hosted runner |
| Started / finished | 2026-10-25 03:27:57–03:54:37 UTC |
| Recovery-matrix repetitions | 1 |
| WordPress recovery proof RTO | 6.590 seconds |

## Results

- Native install and installed-host smoke: PASS.
- Caddy isolated view activation and cleanup: PASS. The helper applied and
  verified the no-follow view; the smoke exercised stable document-root
  activation, HTTPS responses, and symlink rejection.
- Installed WordPress application recovery: PASS. The proof restored files
  and MariaDB data, configured PHP-FPM and the Caddy route, then verified the
  application through HTTP. Reported RTO was 6.590 seconds.
- MariaDB outage handling: PASS. The HTTP request failed during the outage and
  recovered after the database service returned.
- Root-broker outage handling: PASS. Readiness degraded while liveness
  remained available, then recovered.
- Caddy route cleanup and replay: PASS, including replay when the route file
  was already absent.
- SFTP-only OpenSSH and FTPS upload/download, SSH password isolation, and
  credential revocation smokes: PASS.
- One recovery-matrix iteration: PASS. The run exercised cpmove import and
  recovery, backup interruption, file and database restore interruption, site
  termination interruption, account suspension interruption, deployment
  safeguards, and app lifecycle operations.
- Exact-source Integration CI passed formatting, release checks, vet, tests and
  coverage floors, race tests, repository recovery and ENOSPC drills, database
  restore checks, static analysis, container checks, and UI E2E/accessibility.

## Limits

- This was one AlmaLinux 9 + Caddy + MariaDB combination. The other supported
  OS/web-server combinations have not passed this exact-source run.
- The systemd host was a disposable container, not a dedicated VM. It does not
  establish physical host power-loss or storage-controller failure recovery.
- One recovery-matrix iteration does not meet the required 20–50 repeats per
  operation/fault combination.
- This run does not certify broad host ENOSPC behavior, sustained capacity,
  live alert outage/retry delivery, upgrade rollback, or hostile multi-tenant
  isolation.

Production remains **NO-GO**. See
[V1_PRODUCTION_GATES.md](../V1_PRODUCTION_GATES.md) for the release contract.
