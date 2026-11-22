# Ubuntu packaged first-host and recovery smoke

## Result

Passed on exact `main` source commit
`6af93e88b07d7e7f02742a70b95bedf998e67569`.

- Workflow: [Installation smoke run 45037318148](https://github.com/cyberducttape/StePanel/actions/runs/45037318148)
- Host profile: Ubuntu 24.04, Caddy, MariaDB
- Artifact: GoReleaser snapshot archive built from exact source and validated
  before installation; no release was published.
- Installation: `post_install_status=complete` and
  `post_install_cleanup_status=complete`.
- Application recovery: installed WordPress HTTPS/database recovery proof
  passed for backup `20261122-153432.187415942-ci-wp-proof`, with measured
  `rto_ms=4259`.
- Failure checks: control-plane database and root-broker outages were detected
  and recovered. One recovery-matrix iteration passed, including cPanel import,
  backup, restore, and termination worker-interruption cases.
- Duration: the disposable-host job completed in 30m41s.

## Limits

This is one disposable systemd-container run, not repeated installed-host
certification or physical-host/power-loss evidence. The container did not have
project quotas: Git deployment was refused and the live site remained
unchanged. That verifies fail-closed behavior, not quota enforcement. The
recovery proof used the smoke environment's HTTPS trust setup; it does not
verify public DNS, public certificate issuance, an external offsite provider,
or recovery after loss of the host and its local state.

This run does not close production gates requiring repeated recovery
iterations, real quota enforcement, or a supported tagged fresh-install
release.
