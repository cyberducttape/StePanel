# Ubuntu 24.04 packaged first-host smoke — 2026-11-23

**Result:** Pass for this disposable-host smoke. This is one run, not release or
production certification.

## Run identity

- Workflow: [installation smoke run 45129211602](https://github.com/cyberducttape/StePanel/actions/runs/45129211602)
- Source commit: `2e9e06e98e538f87c17bf31bf1a8691ea8b07782`
- Artifact: GoReleaser snapshot archive built from that source and installed
  on a disposable Ubuntu 24.04 systemd container
- Profile: Ubuntu 24.04, Caddy, MariaDB, distro PHP-FPM
- Recovery-matrix repetitions: 1

## Evidence

- The packaged installer completed and cleanup reported complete.
- The installed control plane recovered from a database outage with an
  actionable startup diagnostic. Liveness/readiness degraded during the
  control-plane database and root-broker outages and recovered afterward.
- The installed WordPress fixture was created, served through its HTTPS test
  endpoint, and recovered from a simulated database outage.
- The application-level recovery proof restored the database and files, then
  reported `rto_ms=4398` for recovery point
  `20261123-073131.429238511-ci-wp-proof`.
- The one-iteration recovery matrix passed cPanel import, backup, restore,
  site termination, account suspension, and deployment recovery scenarios,
  including injected worker/process interruption points.
- The disposable host had no project-quota enforcement. Git deployment failed
  closed and left the live site unchanged; this verifies refusal behavior, not
  quota enforcement.

## Limits

This used a snapshot archive and a disposable systemd container, not a tagged
stable release or physical host. One repetition does not meet the documented
20–50-repeat release gate. It does not establish quota enforcement, physical
power-loss recovery, sustained capacity, or production readiness. See
[`V1_PRODUCTION_GATES.md`](../V1_PRODUCTION_GATES.md) for the remaining gates.
