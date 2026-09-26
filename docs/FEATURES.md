# Feature catalog

StePanel is a small control plane for operators moving web and application
workloads from cPanel to Linux hosting. This page describes shipped behavior separately from the
longer-term hosting-panel roadmap.

This file is the canonical feature-status source for the `main` branch.
Status vocabulary: **Stable** means supported and tested; **Beta** means
implemented with explicit operator caution; **Operator-only** means available
to administrators but not exposed as a tenant entitlement; **Experimental**
means the interface may change; **Planned** means not implemented. Release-tag
documentation must be read from the matching release tag, not from `main`.

Documentation version: `main` (reviewed 2026-09-24)
Release approval: use `V1_PRODUCTION_GATES.md`; this catalog is not a release gate.

## Available now

- Go HTTP control plane with signed administrator sessions.
- CSRF protection, login rate limiting, production-required TOTP MFA, and
  security headers.
- Actor-attributed, HMAC-linked JSONL audit logs with offline verification.
- cPanel `cpmove` inspection with archive traversal and size checks.
- Asynchronous website and optional SQL restore jobs.
- MySQL/MariaDB/PostgreSQL selection during installation, including PostgreSQL AppStream stream selection on RHEL-family systems.
- Optional phpMyAdmin or phpPgAdmin installation with dashboard status and management links.
- Native local database inventory, least-privilege database/user provisioning,
  credential rotation, safety-dump-protected deletion, session diagnostics,
  explicit session termination, and read-only effective settings.
- PostgreSQL and MySQL/MariaDB logical dumps for managed databases.
- Database Prometheus metrics for connection pressure, long transactions,
  blocking, deadlocks, and allocated bytes; scheduled backups expose last
  success, age, and consecutive failures.
- ModSecurity and optional OWASP CRS installation in DetectionOnly mode.
- Configured-stack service inventory for Apache, OpenLiteSpeed, Caddy,
  versioned PHP-FPM, MySQL/MariaDB/PostgreSQL, and detected optional services
  such as Fail2Ban and ModSecurity.
- Cloud inventory and audited lifecycle actions for Linode, AWS, and OpenStack, plus Linode DNS, load-balancer, and snapshot operations.
- Strict-host-key SSH server inventory with allowlisted asynchronous restart and reboot actions.
- Authenticated security posture endpoint at `/api/security/audit`.
- Provider-neutral DNS capability contract at `/api/dns/capabilities`; Linode
  record mutations persist desired state and use retryable idempotent jobs.
  Zone/registrar lifecycle, additional provider adapters, and DNSSEC remain
  external requirements.
- Verified, bounded audit-event queries at `/api/audit/events` for deployment and operational history.
- Prometheus-compatible metrics, Docker packaging, Helm, Kubernetes, and Terraform examples.
- Secret-safe `stepanel dr-check` control-plane DR inventory plus verified
  SQLite control-plane backup, dry-run validation, and guarded lock-coordinated
  live restore. Remote audit anchoring remains planned.
- Transactional Caddy and Apache PHP vhosts and reverse proxies with
  validation, rollback, and duplicate-domain checks.
- Fail-closed Apache `.htaccess` preview/import for Caddy, covering common
  front-controller and redirect rules with explicit unsupported-line reports.
- Read-only site-centric inventory at `/api/sites/overview`, grouping document roots, domains, Node applications, and managed database counts without exposing secrets.
- Customer-first hosting workspace that leads with managed sites, connected
  domains, and verified-backup counts. Site workspaces can queue a verified
  file-and-managed-database backup and connect a validated web route; the UI
  explains the required DNS cutover after a route is created.
- Domain route desired state is durable in the control plane, reconciles after
  restart, and supports tenant-scoped route removal for tracked customer
  routes. Customer route activation requires a durable DNS TXT claim at
  `_stepanel.<domain>` through `/api/sites/domains/claim` and `/verify`;
  activation revalidates the TXT record, and administrators have an explicit
  operator bypass. Registrar ownership, DNS zone lifecycle, DNSSEC, and ACME
  issuance remain separate provider boundaries.
- Shared-hosting beta: administrator-provisioned customer accounts with
  independently hashed passwords, encrypted customer TOTP at rest when
  `STEPANEL_ACCOUNT_KEY` is configured, mandatory per-customer TOTP, plan-enforced
  assigned-site limits, reserved administrator identity, and authorization that scopes customer site workspace,
  backup, and job access to their assignments. Provider operations remain
  administrator-only.
- Account plan and site-assignment updates persist resource desired state and
  reconcile affected cgroup/PHP profiles; stricter operator ceilings are
  preserved and failed host application remains visibly pending.
- Administrator-only customer MFA regeneration through
  `/api/accounts/{username}/mfa`, returning the replacement seed once and
  revoking that customer's sessions.
- Customer-scoped API tokens are available at `/api/account/tokens`. Secrets
  are hashed, optionally expire, can be revoked, and are returned only once;
  token management and password/MFA recovery require the browser session.
- Administrator API tokens are available at `/api/admin/tokens` with explicit
  `admin:read` or `admin:operate` scopes. Read tokens cannot mutate state;
  creation and revocation require the administrator browser session.
- Administrator customer credential recovery with temporary password,
  regenerated MFA, one-time recovery codes, session revocation, and customer
  password/MFA completion endpoints.
- Constrained Git releases and one-click rollback at `/api/sites/git-deploy` and `/api/sites/git-rollback`, with public HTTPS sources or per-site deploy-key-authenticated `git@host:path.git` sources, an exact hostname allowlist, shallow ref checkout, commit identification, symlink rejection, Git-metadata removal, atomic activation, previous-release preservation, and audit events. Repository build scripts are not executed.
- Developer runtime APIs for encrypted site environments, version-selected
  PHP-FPM profiles, Composer inspection/install, Node package tooling, Python
  Gunicorn services, WordPress WP-CLI maintenance/update actions, fixed-command
  workers, scoped site logs, and Redis/Valkey allocation metadata.
- Rootless Podman build-runner integration. Builds receive an isolated container
  with read-only source, a dedicated artifact directory, and the site's CPU,
  memory, and PID resource envelope; deployment activation remains the existing
  atomic release workflow.
- Recovery-journaled staging site creation with safe file copies, optional
  non-secret environment cloning, verified domain ownership before customer
  route activation, and verified selected-database restore into a newly
  provisioned staging database.
- Site SSH public-key fingerprint/policy lifecycle, confirmation-gated durable
  site termination, and audited account suspension, unsuspension, and
  login-record removal. Suspension immediately revokes panel sessions.
- Per-site deploy-key generation/retirement where private key material remains
  root-owned and is never returned by the panel API.
- Scheduled site tasks backed by hardened systemd services and timers instead
  of a writable host crontab, with persisted pending state and administrator
  reconciliation after interrupted host mutations.
- Preview per-site resource profiles that enforce CPU quota/weight,
  MemoryHigh/MemoryMax, I/O weight, and task limits
  for managed systemd application/worker processes plus PHP-FPM worker
  ceilings. Plan-assigned sites additionally inherit an aggregate account
  cgroup envelope; direct site-profile changes cannot exceed the owning
  account plan. Optional disk/inode values are enforced with Linux user
  quotas when the filesystem is preconfigured for quotas. Profiles are
  re-applied during startup and through the administrator reconciliation
  endpoint; bandwidth, mail, and Redis runtime enforcement remain
  provider-specific planned work, while supported local database lifecycle and
  plan caps are available.
- Read-only per-database detail at `/api/databases/<name>` for DBA tooling without credential disclosure.
- Deterministic site identities and isolated PHP-FPM pools for restored sites.
- Independently verified site and registered-database backups.
- Scheduled local backup jobs with retention controls and optional enforced
  offsite-target policy.
- Privileged helper execution has bounded contexts/output, shutdown cancels
  background schedulers, malformed recovery journals are quarantined, and
  migration uploads are synced before queue admission.
- Cloud inventory preserves partial results with explicit warnings, bounds
  provider output, and removes panel-specific secrets from CLI environments.
- Site restore/delete operations, including deterministic site IDs and
  per-site Node.js runtime selection where supported by the target host.
- Production deployment examples with immutable image/action references,
  readiness smoke tests, Kubernetes disruption/network controls, and release
  provenance/SBOM generation.

## Partial or operator-only features

- Mail installation is an optional operator integration only. StePanel core
  does not claim mailbox, alias, quota, DKIM/SPF/DMARC, queue, webmail, or
  customer mail lifecycle support; use external mail or an independently
  managed optional mail module.

- Administrator site termination is a confirmation-gated durable job. It
  retains a verified backup, removes managed databases, routes, application
  services, SSH/PHP/quota state, durable site state, and tenant ownership.
  Mail, DNS, registrar, billing, and other external-provider objects remain
  operator responsibilities and are not silently deleted.
- Backup verification is available through the CLI and administrator API. Each
  backup records `crash-consistent / logical backup` classification and can be
  authenticated with an external `STEPANEL_BACKUP_SIGNING_KEY`. Administrator
  files-only restore and restore-to-staging are available for verified files;
  database-only restore is available for existing managed databases with a
  verified pre-restore safety backup. Administrator off-site files-only restore
  is also available when rclone is configured. Administrator off-site
  database-only restore is available for existing managed databases when
  rclone is configured; off-site browsing and full customer self-service
  restore remain deliberately guarded. Schema rollback remains manual.
- The customer workspace currently authorizes assigned site viewing, verified
  backup creation, tracked domain route lifecycle, and job history only. It is not yet a full
  tenant self-service portal.
- PITR/WAL or binlog management, replication orchestration, configuration
  mutation, and automatic failover remain operator-managed and deliberately
  have no unsafe simulated controls.

## Not yet production-complete for shared hosting

StePanel is currently a single-administrator, single-host control plane. It is
not yet a cPanel/Plesk-equivalent multi-tenant hosting product. The following
must be implemented before offering untrusted customer access:

- Durable tenant/account isolation beyond a single host, scoped support and
  reseller roles, OIDC/WebAuthn,
  and approval/audit workflows.
- Durable relational state and a distributed job/agent model for multiple
  servers, retries, cancellation, idempotency, and event delivery.
- Complete domain/DNS/SSL, database/user, mail, FTP/SFTP, bandwidth,
  database/Redis entitlement, and billing lifecycle management. Built-in
  CPU/memory/process/PHP-worker/disk/inode resource envelopes are now shipped;
  bandwidth and provider-specific database, mail, and Redis quotas remain
  unfinished.
- Customer-facing file manager, Git-provider App/OAuth integrations, database
  promotion, notifications, and self-service
  backup/restore. The shipped deploy-key, resource-profile, Security Center,
  scheduled-task, and restore-to-staging APIs remain operator/beta controls
  until tenant enforcement and durable state are complete.

These are product and architecture work items, not safe one-file patches. The
sequencing, acceptance gates, and operational prerequisites are tracked in
[`PRODUCTION_GAP_ANALYSIS.md`](archive/PRODUCTION_GAP_ANALYSIS.md).

Planned operations will be introduced behind explicit permissions and dry-run
modes. The project will not silently mutate live web-server configuration.
