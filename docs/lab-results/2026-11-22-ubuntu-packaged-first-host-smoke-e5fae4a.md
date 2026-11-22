# Ubuntu packaged first-host and recovery smoke — 2026-11-22

## Result

Passed on exact source commit
[`e5fae4ad39638e0b81b04e81fdafe77e53f6c1e6`](https://github.com/cyberducttape/StePanel/commit/e5fae4ad39638e0b81b04e81fdafe77e53f6c1e6).

- Installed-host workflow: [run 45040904045](https://github.com/cyberducttape/StePanel/actions/runs/45040904045)
- Host profile: Ubuntu 24.04, Caddy, MariaDB
- Artifact: `stepanel-install-diagnostics-ubuntu24-caddy-r2-ubuntu-24.04-caddy-mariadb`
- The release-shaped GoReleaser snapshot installed in a disposable systemd
  host and reported `post_install_status=complete`.
- Installed WordPress recovery proof passed over HTTPS with its database
  restored; measured recovery time was **4,539 ms**.
- Installed control-plane database and root-broker outage recovery checks
  passed. One recovery-matrix iteration passed, including interrupted cPanel
  imports, backups, restores, database restores, termination, and suspension.
- The complete workflow took about 3 hours 15 minutes; the recovery matrix is
  expensive and should remain a release/host-certification check rather than
  a routine local onboarding step.

## Limits and follow-up

This is one disposable systemd-container run, not repeated host certification,
physical power-loss evidence, or a supported tagged release. The smoke used a
local rclone destination and did not establish public DNS, public certificate
issuance, external offsite recovery, or project-quota enforcement. Git
deployment correctly failed closed without project quotas and left the live
site unchanged. Repeat recovery iterations, real quota enforcement, and a
fresh install from an official tagged archive remain open.

The artifact also exposed a harmless first-install inventory warning: a new
host has no `/var/www/sites` directory yet, and the preflight's `find | wc`
count reported the expected missing directory as a command failure. The
current source now treats an absent directory as zero sites and has a shell
contract regression. That small follow-up has not yet been exercised on an
installed host.
