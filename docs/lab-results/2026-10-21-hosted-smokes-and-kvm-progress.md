# Hosted installation and local KVM evidence — 2026-10-21

Status captured 2026-10-21 05:06 UTC. This record separates completed hosted
checks from host certification that was still running at capture.

## Hosted checks

The six-combination installed-host workflow completed successfully on
`c9712da7279533970fe6cd0b5b89b45309e23bb5`:

- Ubuntu 24.04 + Caddy + MariaDB
- Ubuntu 24.04 + Apache + MariaDB
- Rocky Linux 9 + Caddy + MariaDB
- Rocky Linux 9 + Apache + MariaDB
- AlmaLinux 9 + Caddy + MariaDB
- AlmaLinux 9 + Apache + MariaDB

[Installation smoke run 39540812270](https://github.com/cyberducttape/StePanel/actions/runs/39540812270)
passed all six jobs. This workflow commit changes workflow selection and docs,
not application source; the tested application source matches `d931c6a`.

At the original 05:06 UTC capture, the quota-enabled production-install smoke
was still running. A later status check on 2026-10-21 found the workflow
completed successfully, including its `quota-install` job:
[quota install run 39540812247](https://github.com/cyberducttape/StePanel/actions/runs/39540812247).
This is hosted install/quota evidence for the `d931c6a` application source, not
evidence for physical power loss, XFS quotas, or the full failure matrix.

Exact source `7cd215f8a49863bce4c21fa6f35a8889ade1500c` passed:

- [CI integration 39541304606](https://github.com/cyberducttape/StePanel/actions/runs/39541304606)
- [CodeQL 39541304596](https://github.com/cyberducttape/StePanel/actions/runs/39541304596)
- [Documentation 39541304565](https://github.com/cyberducttape/StePanel/actions/runs/39541304565)

## Rocky 9 local KVM run — in progress at capture

The disposable VM was built from exact source commit `7cd215f8a49863bce4c21fa6f35a8889ade1500c`, with no dirty source files, using the Rocky Linux 9 GenericCloud image whose SHA-256 matched its adjacent checksum manifest. Guest configuration: 4 GiB RAM, 4 CPUs, ext4 `/var/www` with user and project quotas enabled, and a separate ext4 ENOSPC volume.

Completed at capture:

- Guest boot and quota mount verification.
- Native Caddy + MariaDB install smoke, including database-backed WordPress HTTP recovery proof and database-outage handling.
- Default installed-host recovery smoke for cpmove import, backup, file restore, database restore, site termination, account suspension, and Git deployment at the tested kill points.
- Full recovery matrix, iteration 1 of 1 so far: cpmove activation; backup init, archive, verify, and commit; file/database restore recovery; site termination; suspension and deployment checks observed through the current point in the log.

Not completed at capture:

- Remaining full recovery-matrix boundaries.
- Abrupt power-loss phases and ENOSPC phases in the outer KVM certification script.
- Repeated 20–50-run certification, other distributions, XFS quota behavior, and the broader release gates.

The live run's artifacts were under `/var/tmp/stepanel-kvm-cert-7cd215f` at capture. A running or partially completed matrix is evidence only for the specific successful cases listed above; it does not close the full-host recovery gate or change the production **NO-GO** decision.
