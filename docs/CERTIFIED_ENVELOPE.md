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
