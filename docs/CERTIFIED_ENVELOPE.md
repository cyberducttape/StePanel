# Single-host certification envelope

**Status: proposed target; not yet certified.**

StePanel should publish a measured operating envelope instead of making a
generic production-readiness claim. The first target to evaluate is:

| Dimension | Initial target |
|---|---:|
| Managed sites | 500 |
| Domains | 2,000 |
| Customer accounts | 100 |
| Scheduled jobs | 50 per hour |
| Concurrent mutating workflows | 2 |
| Control-plane database and state | Publish measured GB ceiling with storage class |

The targets are not a certification. A release can claim the envelope only
after a reproducible test run demonstrates all of the following on the stated
OS, filesystem, CPU, memory, and storage configuration:

- seeded sites, domains, accounts, backups, and job history at the target
  scale;
- sustained scheduled-job load and two concurrent mutations without lost,
  duplicated, or permanently leased jobs;
- p95 API and job-admission latency, peak CPU/RAM, database size, and storage
  growth recorded;
- restart, worker interruption, backup restore, and quota behavior verified;
- authorization, tenant isolation, audit continuity, and backup-integrity
  checks passing;
- a documented recovery time and recovery point result, including the
  workload and failure point used for the measurement.

The latest 500-site diagnostic profile
[45312970493](https://github.com/cyberducttape/StePanel/actions/runs/45312970493)
ran on a Rocky Linux 9 disposable KVM guest using source `c6425b05`. Across
600 seconds it served 71,606 HTTPS site requests with zero failures while two
512 MiB backups completed. Site-page p99 was 114.1 ms at baseline and 126.1 ms
during backups. The site overview API p95/p99 was 50.7/134.7 ms after the
route/proxy directory-scan optimization, compared with 302.5/665.8 ms before
that change. Host CPU peaked at 99.5%; summed PHP-FPM RSS reached 9.4 GiB, with
6.9 GiB minimum available memory. The [reviewed report](./lab-results/2026-11-24-capacity-large-c6425b0.md)
contains workload and measurement details.

This is the third passing 500-site run on one runner VM type and the first on
that source, not a certified limit. It did not include real WordPress/database
traffic, migrations, restart or recovery under load, or the 2,000-domain and
100-account targets. The earlier Ubuntu 26.04.1 synthetic HTTP run is recorded
in the [2026-10-24 lab report](./lab-results/2026-10-24-500-site-mixed-load.md);
it lacked resource monitoring and ran outside the declared supported OS
matrix, so it is exploratory evidence only.

The certification record must name the StePanel version, schema/data format,
kernel and distribution, SQLite/storage settings, worker count, resource
limits, workload generator, test duration, and every excluded feature. Any
change to those inputs invalidates the claim until rerun.

## Deployment classification

- **Native systemd:** the supported full single-host host-management model.
- **Docker/Kubernetes:** control-plane packaging for evaluation or explicitly
  integrated remote services; one replica and no HA/failover claim.
- **Multi-host or multi-tenant SaaS:** outside this envelope and requires a
  separate architecture for shared state, job routing, and host operations.

Until this document contains a signed, reproducible evidence record, operators
should describe StePanel as an operator beta/single-host production candidate,
not as a generally certified hosting platform.
