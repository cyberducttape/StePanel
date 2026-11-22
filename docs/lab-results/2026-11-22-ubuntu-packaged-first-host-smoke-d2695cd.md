# Ubuntu packaged first-host and recovery smoke — 2026-11-22

## Result

Passed on exact installer/runtime source commit
[`d2695cd01ccdbbb8f2fd9130c42db7d22c5ec8f1`](https://github.com/cyberducttape/StePanel/commit/d2695cd01ccdbbb8f2fd9130c42db7d22c5ec8f1).

- Installed-host workflow: [run 45060415981](https://github.com/cyberducttape/StePanel/actions/runs/45060415981)
- Host profile: Ubuntu 24.04, Caddy, MariaDB
- Artifact: `stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb/install-smoke-output.txt`
- The GoReleaser snapshot was extracted and installed in a disposable
  systemd container. It reported `post_install_status=complete` and
  `post_install_cleanup_status=complete`.
- The installed WordPress database restore and HTTPS application proof passed
  for backup `20261122-194032.396746849-ci-wp-proof`; measured recovery time
  was **4,512 ms**.
- Installed control-plane database outage recovery and root-broker readiness
  degradation/recovery passed. One recovery-matrix iteration passed.
- The fresh-host `/var/www/sites` inventory emitted no missing-directory
  `find | wc -l` error after the fix in `d2695cd0`.
- The workflow completed in about 30 minutes.

## Limits

This is one disposable systemd-container run, not repeated certification,
physical power-loss evidence, or a fresh install from an official tagged
release. It does not prove public DNS or public certificate issuance, external
offsite recovery, or project-quota enforcement. The lab had no project
quotas; Git deployment refused to run and left the live site unchanged. The
production repeat, quota, and tagged-release gates remain open.
