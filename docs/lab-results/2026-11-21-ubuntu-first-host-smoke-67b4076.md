# Ubuntu first-install and recovery smoke — `67b40760`

**Source:** `67b40760e575e0dd2d626b64010c496d9245f0da`
**Host profile:** Ubuntu 24.04, Caddy, MariaDB
**Result:** Passed once on a disposable systemd host
**Integration CI:** [run 44985692286](https://github.com/cyberducttape/StePanel/actions/runs/44985692286)
**Installed-host smoke:** [run 44985692207](https://github.com/cyberducttape/StePanel/actions/runs/44985692207)
**Release-evidence workflow:** [run 44987771693](https://github.com/cyberducttape/StePanel/actions/runs/44987771693)

## Results

- Installed the exact source revision on a fresh disposable Ubuntu host using
  the recommended Beta profile.
- Completed one installed-host recovery-matrix iteration.
- Restored and validated a WordPress application. The recovery proof reported
  `rto_ms=4327` (4.327 seconds).
- Interrupted privileged site-identity repair at its durable SQLite
  checkpoint, restarted the repair, and observed it complete for both fixture
  sites.
- Verified the repaired sites received distinct identities and that
  cross-site file reads and writes were denied.
- The identity-repair test exercised SQLite WAL mode and confirmed the repair
  reserves its writer transaction before reading mappings, avoiding the
  previous `SQLITE_BUSY` race at the injected interruption point.

The downloaded host-smoke artifact is available under
`stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb/` in run
44985692207. Its `install-smoke-output.txt` contains the recovery proof and
identity-repair transcript.

## Scope and remaining work

This is one disposable-host run. It does not certify repeated recovery,
physical host power loss, broader disk-exhaustion cases, competing-writer quota
enforcement, sustained capacity, other OS/web-server combinations, or a
production workload. The default official stable release archive is still
v0.7.0 and does not contain the current guided setup; a clean-host installation
from the eventual tagged release archive remains required. Production remains
**NO-GO** until the authoritative release gates are satisfied.
