# Ubuntu packaged first-install smoke — 2026-11-22

## Result

Passed on source commit
[`b901b258521ea36a96919ee91c224ff915d597b3`](https://github.com/cyberducttape/StePanel/commit/b901b258521ea36a96919ee91c224ff915d597b3).

- Installed-host workflow: [run 45025566462](https://github.com/cyberducttape/StePanel/actions/runs/45025566462)
- Release evidence: [run 45025579744](https://github.com/cyberducttape/StePanel/actions/runs/45025579744)
- Host artifact:
  `stepanel-install-diagnostics-default-ubuntu-24.04-caddy-mariadb/install-smoke-output.txt`
- The current `main` differs from the tested source only in workflow,
  documentation, and test files; its packaged installer/runtime tree is
  unchanged from this run.

The workflow built and extracted a GoReleaser snapshot, checked the required
installer files and setup command, then installed it in a disposable Ubuntu
24.04 systemd container with Caddy and MariaDB. The installer reported
`post_install_status=complete`, printed the HTTPS sign-in steps and first-run
checklist, and the installed panel recovered after injected control-plane
database and root-broker outages.

The workflow imported a cPanel fixture, exercised interrupted create, backup,
restore, database restore, and termination recovery, and completed one full
recovery-matrix iteration. It also installed WordPress, created a backup,
restored and imported its database in the disposable recovery environment, and
served the application health probe over local HTTPS. Measured recovery time
was **4,490 ms**.

## Limits

This is one disposable-container run using a release-shaped snapshot, not an
official tagged archive and not repeated host certification. The lab used a
local rclone destination and explicitly skipped filesystem quota enforcement;
it does not prove a production storage configuration or quota behavior. HTTPS
used the disposable host's local trust setup, not public DNS or public
certificate issuance. Physical power-loss durability, broad ENOSPC recovery,
quota enforcement, capacity, and the official tagged-archive fresh-install
path remain unproven.
