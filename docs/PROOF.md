# What is proven

StePanel does not ask you to take recovery on trust. This page lists what is
tested on real, disposable Linux hosts (not mocks), how it is tested, the
latest result with a link to the CI run or reviewed report, and what that
result does **not** prove. It is updated as evidence lands; the authoritative
release decision is in [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md).

Last reviewed: 2026-11-21.

## Tested on real hosts

| Claim | How it is tested | Latest result | Does not prove |
| --- | --- | --- | --- |
| It installs and recovers on the supported platforms | Fresh install, PHP sites, a recovery matrix, database and broker outages, and a WordPress restore over HTTP, on Ubuntu 24.04, AlmaLinux 9 and Rocky Linux 9 with Apache and Caddy | Passed once on all six combinations, WordPress recovery in 4.6–6.9 s ([report](./lab-results/2026-11-20-six-host-install-smoke-059ba72.md)) | Repeated runs; other operating systems; physical servers |
| A server crash mid-operation does not corrupt sites or data | The whole virtual machine is killed (no shutdown, host disk cache bypassed) in the middle of a file restore, database restore, Git deployment, backup retention, and control-plane writes, then booted again | All five recovered correctly ([report](./lab-results/2026-11-20-kvm-abrupt-loss-operations-cache-none-c3e37eb.md)) | Physical power loss; repeated runs |
| An upgrade interrupted by a crash recovers | v0.6.0 and v0.7.0 hosts are upgraded; the guest is killed while the installer is changing files, booted again, and the upgrade rerun; customer site and database are checked, then rolled back and upgraded again | Passed for both versions ([run 44863673113](https://github.com/cyberducttape/StePanel/actions/runs/44863673113)) | AlmaLinux/Rocky upgrade paths; large installations |
| A full database disk does not damage the live database | A restore larger than the free space is attempted on a small database filesystem | Refused at admission; an undeclared oversized load was stopped before the disk filled; the live database was unchanged ([report](./lab-results/2026-11-21-kvm-db-enospc-alerts-7e2a575.md)) | A disk that actually fills during the load |
| A crash of the privileged helper mid-restore leaves a consistent site | The root broker is killed while it seals a just-activated restored tree | The site held the backed-up files, every file owned by the site account ([run 44863669797](https://github.com/cyberducttape/StePanel/actions/runs/44863669797)) | Every helper call; repeated runs |
| Sites cannot read or modify each other | Each site's own account (the identity its PHP runs as) tries to read, list, traverse and write another site's files and PHP sessions, and to connect to its PHP-FPM socket | All refused, on Apache and Caddy ([run 44863669797](https://github.com/cyberducttape/StePanel/actions/runs/44863669797), [run 44860226545](https://github.com/cyberducttape/StePanel/actions/runs/44860226545)) | Isolation against hostile tenants on a shared kernel |
| Suspending a site is complete and reversible | A live PHP site with a database is suspended and resumed through the API | 503 page with no PHP, pool and database logins disabled, deployments refused; everything restored on resume ([run 44863669797](https://github.com/cyberducttape/StePanel/actions/runs/44863669797)) | Sites behind application proxies |
| Operators hear about failures | A real job is made to fail; the alert email must arrive over verified TLS, survive a mail-server outage, and be retried | Delivered, outage reported in health, retried after recovery ([report](./lab-results/2026-11-21-kvm-db-enospc-alerts-7e2a575.md)) | Webhook delivery; third-party mail providers |
| An interrupted identity repair resumes safely | The legacy identity repair is killed at its database commit and at three host steps, then resumed | Every resume point held the expected state and the repair completed ([run 44863669797](https://github.com/cyberducttape/StePanel/actions/runs/44863669797)) | Every supported filesystem |
| The code has no known vulnerabilities | govulncheck, staticcheck, Trivy and CodeQL on `main` | No open findings on 2026-11-19 ([report](./lab-results/2026-11-19-security-scan-e5ddeee.md)) | An independent security review |

## Not yet proven

These are open, and the project says so rather than implying otherwise:

- **A capacity limit.** A 500-site test on a 4-CPU virtual machine still
  times out on about 2% of requests during periodic host-wide stalls; the
  cause is under investigation ([latest report](./lab-results/2026-11-21-capacity-large-e19d737-after-fpm-fix.md)).
  No site count is certified.
- **Repeated recovery.** Each result above is a single pass. The 20-repeat
  recovery cohort has not completed.
- **Physical power loss.** All crash tests kill a virtual machine.
- **Webhook alert delivery** against a live external endpoint.
- **An independent security review.**
- **Hostile multi-tenant hosting.** Sites share one kernel and network; see
  [what tenant isolation covers](../SECURITY.md#what-tenant-isolation-does-and-does-not-mean).

## How to check a result yourself

Every link above opens the CI run or the reviewed report with its source
commit. The test scripts are in [`deploy/lab/`](../deploy/lab/) and run on
disposable hosts; [`deploy/lab/README.md`](../deploy/lab/README.md) explains
how to run them on your own lab machine.
