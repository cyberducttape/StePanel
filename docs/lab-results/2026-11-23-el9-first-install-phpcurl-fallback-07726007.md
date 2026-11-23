# EL 9 fresh-install PHP curl fallback — 2026-11-23

## Result

Passed on installer/runtime source
[`07726007b1c835b2fa34b55ba6bb7e806ccff196`](https://github.com/cyberducttape/StePanel/commit/07726007b1c835b2fa34b55ba6bb7e806ccff196).

| Host profile | Workflow | WordPress recovery proof | Recovery matrix |
| --- | --- | ---: | --- |
| AlmaLinux 9 + Apache + MariaDB | [run 45108134199](https://github.com/cyberducttape/StePanel/actions/runs/45108134199) | 7,522 ms | 1 iteration passed |
| Rocky Linux 9 + Apache + MariaDB | [run 45108134385](https://github.com/cyberducttape/StePanel/actions/runs/45108134385) | 7,576 ms | 1 iteration passed |

Both packaged installs completed with `post_install_status=complete` and
`post_install_cleanup_status=complete`. Their package plans used `php-common`,
which provides the curl module on these EL 9 repositories. The installer then
verified that PHP loaded the curl extension. This reproduces the repositories
that lack a standalone `php-curl` package and verifies the fallback through
installed WordPress recovery.

## Limits

Each result is one disposable systemd-container pass, not repeated or
physical-host certification. The containers lacked project quotas, so Git
deployment was refused and the live site remained unchanged. These runs do not
establish quota enforcement, power-loss recovery, external DNS/TLS, independent
offsite restore, or a production capacity envelope. They cover Apache on EL 9;
they do not certify Caddy or other EL minor releases.

The full diagnostics are retained in the named artifacts for the two workflow
runs.
