# Product roadmap

## 0.1 — Foundation

- Authenticated operations dashboard
- PHP hosting installation with MySQL/MariaDB/PostgreSQL selection
- Safe asynchronous cpmove staging and restore
- Health, metrics, tamper-evident audit events, and release automation
- Live service inventory and authenticated security posture checks

## 0.2 — Hosting operations

- First-run setup wizard
- Site deletion and domain lifecycle completion
- Per-site PHP version selection
- General-purpose database and database-user lifecycle (shipped in 0.6 for local single-host engines)
- Customer-visible operation history and notification delivery
- Site-centric inventory and constrained pre-built Git deployment/rollback
  (available in `Unreleased` after 0.6.0)
- Customer-first hosting workspace with per-site domain connection and
  verified-backup actions (available in `Unreleased` after 0.6.0); DNS
  lifecycle and multi-host authorization remain future work.
- Constrained shared-hosting customer accounts, durable assigned-site
  ownership, assigned-site plan limits, and customer-scoped site/backup/job
  visibility (available in `Unreleased`); panel-session suspension and
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

## 0.7 — Stability and internal package boundaries

The next milestone is an architecture/stability release, not a feature-count
release. It should be supported by failure-injection, concurrency, restore,
and installation evidence before the version is tagged.

- Extract authentication and durable job seams into `internal` packages while
  preserving the current HTTP/API behavior.
- Move migration and backup workflows behind interfaces that can be tested
  without the full control-plane assembly.
- Keep privileged helpers and external cloud/SSH adapters behind explicit
  operation interfaces.
- Publish a tagged binary release with checksums, SBOM, provenance, and known
  limitations before calling the API stable.

## 1.0 — Production contract for operator-managed hosting

- Stable API and migration policy
- Signed multi-platform releases
- Upgrade and rollback tooling
- Full accessibility review
- Security review and documented support policy

## 2.0 — Shared-hosting platform

- Tenant/account model with quotas, service plans, scoped RBAC, customer API
  tokens,
  OIDC, and phishing-resistant MFA
- Multi-host agent orchestration with scoped worker credentials
- Complete domain, DNS, TLS, database, mail, FTP/SFTP, cron, SSH, and billing
  lifecycle
- Database PITR/WAL or binlog orchestration, replication topology, controlled
  switchover, and externally fenced automatic failover
- Customer portal, file manager, Git-provider App/OAuth integrations, database
  promotion, notifications, and self-service backup/restore;
  deploy-key Git webhooks, sandboxed builds, scheduled tasks, and operator
  restore-to-staging are already available with documented beta boundaries.

  The customer portal now includes a tenant-scoped account/usage view and
  session security controls; the remaining shared-hosting work is the
  multi-host and delegated-RBAC platform around that portal.
