# Ubuntu first-install with optional mail — 2026-11-22

## Result

Passed on exact source commit
`94fbf293dcdff3a8aa7790807ac2117ef380267d`.

- [Installed-host workflow run 45002811271](https://github.com/cyberducttape/StePanel/actions/runs/45002811271)
- [Integration CI run 45002811283](https://github.com/cyberducttape/StePanel/actions/runs/45002811283)
- Host artifact: `stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb/install-smoke-output.txt`

The workflow built an exact-source GoReleaser snapshot, extracted and
validated the installer package, then installed it on a disposable Ubuntu
24.04 systemd container with Caddy and MariaDB. The exercised optional-mail
configuration installed Ubuntu's `spamd` package and managed
`spamd.service`. The log shows the service was disabled and
`post_install_status=complete`; there was no pending SpamAssassin post-install
action.

The installed panel provisioned WordPress, served it over HTTPS, created and
restored a backup, and passed the application recovery proof in **4,422 ms**
for backup `20261122-084402.141805024-ci-wp-proof`. Database and broker outage
recovery passed, as did the one-iteration recovery matrix and the included
cPanel, backup, restore, termination, suspension, deployment, and broker
interruption drills.

## Limits

This is one disposable systemd-container run for Ubuntu 24.04 + Caddy +
MariaDB, using an exact-source snapshot archive rather than a published
release. One recovery-matrix iteration is not repeated certification. The
run does not establish physical power-loss durability, broad ENOSPC behavior,
quota enforcement, a certified capacity envelope, or production readiness.
