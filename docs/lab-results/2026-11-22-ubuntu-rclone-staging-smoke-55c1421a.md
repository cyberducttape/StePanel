# Ubuntu install and rclone config staging smoke — 2026-11-22

## Result

Passed on exact source commit
[`55c1421a6d203949600ad7897830d2538a47c0bc`](https://github.com/cyberducttape/StePanel/commit/55c1421a).

- [Installed-host workflow run 45017173718](https://github.com/cyberducttape/StePanel/actions/runs/45017173718)
- [Integration CI run 45017173779](https://github.com/cyberducttape/StePanel/actions/runs/45017173779)
- Host artifact: `stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb/install-smoke-output.txt`

The workflow built and extracted the exact-source GoReleaser snapshot, then
installed it on a disposable Ubuntu 24.04 systemd host using Caddy and
MariaDB. The host smoke started with rclone's default config at
`/root/.config/rclone/rclone.conf`, owned by root with mode `0600`, and left
`RCLONE_CONFIG` unset so the installer had to discover the default path.

The install smoke passed its checks that StePanel copied the config to
`/etc/stepanel/rclone.conf` as `root:stepanel` mode `0640`; did not change the
source file or its hash; did not make the source readable to `stepanel`; and
could load the `local:` remote as the service user. It also checked that
`/etc/ste-panel.env` points to the staged file. The installer completed with
`post_install_status=complete`. The smoke's successful `/readyz` checks ran
with offsite backup required and exercised the bounded rclone write/read/delete
probe against the local test remote.

The same host run provisioned WordPress and passed its database-backed HTTPS
recovery proof in **4,333 ms**. It also passed one recovery-matrix iteration,
including injected worker interruptions, broker restart, database outage, and
control-plane recovery checks.

## Limits

This is one disposable-container run on Ubuntu 24.04 + Caddy + MariaDB using
an exact-source snapshot archive. The offsite destination was a local rclone
remote, not an external cloud provider. HTTPS used the host's local test trust
setup; this does not establish public DNS or certificate issuance. One run is
not repeated host certification, physical power-loss evidence, broad ENOSPC or
quota certification, or production approval. The release decision remains
NO-GO.
