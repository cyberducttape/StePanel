# Product roadmap

## 0.1 — Foundation

- Authenticated operations dashboard
- PHP hosting installation with MySQL/MariaDB/PostgreSQL selection
- Safe asynchronous cpmove staging and restore
- Health, metrics, tamper-evident audit events, and release automation
- Live service inventory and authenticated security posture checks

## 0.2 — Hosting operations

- First-run setup wizard (guided installation on `main`, unreleased)
- Site deletion and domain lifecycle completion
- Per-site PHP version selection
- General-purpose database and database-user lifecycle (shipped in 0.6 for local single-host engines)
- Customer-visible operation history and notification delivery
- Site-centric inventory and constrained pre-built Git deployment/rollback
  (shipped in 0.7)
- Customer-first hosting workspace with per-site domain connection and
  verified-backup actions (shipped in 0.7); DNS
  lifecycle and multi-host authorization remain future work.
- Constrained shared-hosting customer accounts, durable assigned-site
  ownership, assigned-site plan limits, and customer-scoped site/backup/job
  visibility (shipped in 0.7); panel-session suspension and
  application/filesystem resource ceilings are shipped, while
  hosting-workload lifecycle and provider-specific quotas remain future work.

## 0.3 — Recovery and scale

- Snapshot-backed restore rollback for files and databases
- Scheduled backup policies with bounded local retention and RPO evidence (shipped in 0.6)
- Durable job state, lease/retry/cancellation semantics, and worker health
- Import progress, cancellation, and retry
- Docker and distribution integration tests
- Durable SQLite control-plane state, leases, worker supervision, and recovery
  reconciliation (shipped in 0.7)

## 0.7 — Stability and internal package boundaries (released 2026-09)

An architecture/stability release, not a feature-count release.

- Authentication and durable job seams extracted into `internal` packages
  while preserving the HTTP/API behavior.
- Privileged helpers and cloud/SSH adapters behind explicit operation
  interfaces; the typed root broker that now carries every production helper
  call landed on `main` after 0.7.0.
- Tagged binary release with checksums, SBOM, provenance, and known
  limitations.

## Next release (on `main`, unreleased)

The next release continues the 1.0 evidence work rather than widening scope.
Changes already on `main` are listed under `[Unreleased]` in
[CHANGELOG.md](../CHANGELOG.md) and under "New on `main`" in
[FEATURES.md](./FEATURES.md). The main ones are guided installation,
recovery status, the concurrent root broker, blank PHP and WordPress site
creation, WordPress staging clone and promotion, preview-guarded host
changes, per-site incident diagnostics and runtime alerts, the `/api/v1`
automation API with Go/TypeScript SDKs and `stepanelctl`, and
crash-recoverable application activation.

The ordered work toward 1.0, with acceptance evidence for each milestone, is
in [DEVELOPMENT_PLAN.md](../DEVELOPMENT_PLAN.md). In short:

- Repeat the real-host recovery matrix as a 20–50 run cohort and extend
  ENOSPC, database-outage, and abrupt-loss drills
  ([REAL_HOST_FAILURE_MATRIX.md](./REAL_HOST_FAILURE_MATRIX.md)).
- Prove alert delivery and publish a measured capacity envelope
  ([CERTIFIED_ENVELOPE.md](./CERTIFIED_ENVELOPE.md)).
- Continue moving migration and backup workflows behind interfaces that can be
  tested without the full control-plane assembly.

Known open hardening items:

- Node application units still start through `bash -lc` and `nvm use`;
  resolving the Node binary when the unit is generated is described in
  [NODE_STARTUP_HARDENING.md](./NODE_STARTUP_HARDENING.md).
- The container image pins its base image digest, but runtime packages come
  from live Debian repositories, so images are not byte-for-byte
  reproducible. A complete package lock or a dated Debian snapshot is needed;
  `docker/apt-pins.txt` is only an inventory.

## 1.0 — Production contract for operator-managed hosting

- Stable API and migration policy
- Signed multi-platform releases
- Upgrade and rollback tooling
- Full accessibility review
- Security review and documented support policy

Release criteria are the evidence gates in
[V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md). 1.0 is a single-host
product: high availability, multi-host and multi-region operation, SLA and
escalation tooling, and automated incident response are 2.0 work.

## 2.0 — Shared-hosting platform

- Tenant/account model with quotas, service plans, scoped RBAC, customer API
  tokens,
  OIDC, and phishing-resistant MFA
- Multi-host agent orchestration with scoped worker credentials
- Complete domain, DNS, TLS, database, mail, FTP/SFTP, cron, SSH, and billing
  lifecycle
- Database PITR/WAL or binlog orchestration, replication topology, controlled
  switchover, and externally fenced automatic failover
- Customer portal, file manager, Git-provider App/OAuth integrations,
  general (non-WordPress) database promotion, notifications, and self-service
  backup/restore; deploy-key Git webhooks, sandboxed builds, scheduled tasks,
  operator restore-to-staging, and WordPress staging promotion are already
  available with documented beta boundaries.

  The customer portal now includes a tenant-scoped account/usage view and
  session security controls, owner/manager/developer/viewer roles, and
  owner-initiated site creation; the remaining shared-hosting work is the
  multi-host platform, support/reseller roles, and the provider lifecycle
  around that portal.
