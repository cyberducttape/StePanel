# Ubuntu packaged first-host and recovery smoke — 2026-11-23

## Result

Passed on installer/runtime source
[`07726007b1c835b2fa34b55ba6bb7e806ccff196`](https://github.com/cyberducttape/StePanel/commit/07726007b1c835b2fa34b55ba6bb7e806ccff196).

- Installed-host workflow: [run 45108095187](https://github.com/cyberducttape/StePanel/actions/runs/45108095187)
- Host profile: Ubuntu 24.04, Caddy, MariaDB
- Artifact: `stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb/install-smoke-output.txt`
- The exact-source GoReleaser snapshot installed in a disposable systemd
  container; `post_install_status=complete` and
  `post_install_cleanup_status=complete`.
- Installed control-plane database outage recovery passed.
- The installed WordPress database/HTTPS recovery proof passed after database
  outage recovery; measured RTO was **4,380 ms**.
- One installed-host recovery-matrix iteration passed, including injected
  worker/broker interruptions, cPanel import recovery, backup verification,
  restore commit recovery, termination recovery, account suspension, and
  deployment admission checks.
- Without project quotas in this container, Git deployment was refused and
  the live site remained unchanged. This verifies fail-closed behavior only.

## Limits

This is one disposable systemd-container pass, not repeated recovery
certification or physical-host/power-loss evidence. It does not prove project
quota enforcement, external DNS/TLS issuance, offsite restore to an independent
host, or a production capacity envelope. It is a release-shaped snapshot, not
a clean-host install from an official tagged release.

The full install diagnostics are retained as the artifact named above in run
45108095187.
