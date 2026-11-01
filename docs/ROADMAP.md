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

## Beyond 1.0 — Competitive hosting capabilities

The 1.0 promise stays deliberately narrow: a credible, operator-managed,
single-host PHP/WordPress product. Features below improve migration confidence
or establish parity with established hosting panels; they do not delay 1.0
unless a specific item is already part of a 1.0 production gate.

### 2.0A — Migration and WordPress lifecycle confidence

Prioritize these before broadening the service catalog:

- **Migration fidelity:** strengthen the existing cPanel importer and
  compatibility report. Every discovered component must be marked imported,
  transformed, skipped, unsupported, or failed with a reason and evidence.
  Compare source and target domains, files, databases, mailbox inventory,
  scheduled tasks, and DNS records; distinguish observed results from
  assumptions. Export a report tied to the archive digest and StePanel build.
- **Post-migration proof:** run a target-side database check and HTTP
  application probe after activation. Show unresolved mail, DNS, TLS, cron,
  and configuration work as explicit cutover tasks. Do not label a migration
  verified when an item was only inspected or assumed compatible.
- **WordPress lifecycle:** treat create, secure, backup, restore, stage, and
  promote as one supported workflow. Acceptance requires a clean supported-host
  install, encrypted local/offsite backup, restore to a clean destination,
  database-backed HTTPS probe, staging clone, and guarded promotion with
  verified database separation and rollback evidence.
- **Repeated disaster recovery:** keep repeated installed-host application
  recovery as a 1.0 release gate. A passing archive check or one successful
  restore is not a substitute for the repeated evidence contract in
  [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md).

### 2.0B — Hosting-panel parity for operator-managed fleets

- **Customer file manager:** complete the current partial experience with
  confined browsing, upload/download, rename, deletion, and permission
  management. Acceptance requires tenant-scoped authorization, descriptor-rooted
  filesystem operations, bounded transfers/listings, audit history, and
  installed-host race and quota tests. This is a high-priority competitive
  feature, but not a 1.0 release blocker.
- **DNS lifecycle:** add provider adapters and import/exportable zone
  management, complete record reconciliation, and DNSSEC key/DS lifecycle
  support. Each adapter needs provider-contract tests and a read-after-write
  check; DNSSEC changes need a preview and recovery procedure.
- **Mail scope decision:** keep mail externally managed for 1.0. Before 2.0
  implementation, decide through an ADR whether StePanel will own a complete
  customer mail lifecycle or integrate external mail providers. Do not imply
  first-party mailbox provisioning, delivery, migration, or recovery while
  those workflows remain operator-managed.
- **Provider operations:** add billing, reseller/support roles, customer
  onboarding, and delegated support only with auditable authorization and
  lifecycle contracts. The existing tenant portal and owner/manager/developer/
  viewer roles are foundations, not a complete provider platform.

### 2.0C — Enterprise and distributed operation

- **Identity:** production-complete WebAuthn and OIDC, with recovery and
  account-transition tests.
- **Database recovery:** managed point-in-time recovery using verified WAL or
  binlog retention, with restore-to-new-instance rehearsal and measured RPO/RTO.
- **Multi-host control plane:** separate architecture work for durable job
  routing, shared state, upgrades, and failure fencing; do not imply that the
  current single-host SQLite design provides clustering or HA.
- **Hostile tenant isolation:** require a tested per-tenant VM or comparably
  strong isolation boundary before advertising support for mutually
  untrusted customer code. Unix accounts and cgroups remain useful controls,
  not a substitute for that boundary.
