# Production gap analysis

This document records what is required to operate StePanel safely and what is
required to turn it into a shared-hosting customer panel. It prevents a secure
single-host operator tool from being mistaken for a complete multi-tenant
platform.

## Implemented production controls

- Authentication has secure session cookies, CSRF protection, login
  throttling, production-required TOTP, security headers, and tamper-evident
  audit logs.
- Restore archives are inspected before extraction and restore work is
  asynchronous.
- Backups can be independently verified. Deployments can require an offsite
  backup target with `STEPANEL_REQUIRE_OFFSITE_BACKUP=1`.
- Container and deployment examples use immutable image references. CI pins
  third-party actions, performs a production startup smoke test, and checks
  Kubernetes/Helm/Terraform references.
- Kubernetes examples include a disruption budget and ingress-only network
  policy. TLS termination is explicit; the application does not silently
  trust a proxy.
- Prometheus metrics and alert rules are included for availability, restore
  failures, and stuck jobs.
- Readiness fails when durable jobs are dead-lettered, preventing an instance
  with an unreconciled failed mutation from advertising service health.
- Readiness also runs a SQLite control-plane quick check, so structural
  corruption cannot be advertised as a healthy queue.
- Administrators can review and explicitly requeue a dead-letter job through a
  durable compare-and-set recovery action; the attempt counter is reset only
  after that operator action and the event is audit logged.
- Operators have site-centric inventory, constrained pre-built HTTPS Git
  deployment with atomic file rollback, and credential-safe database detail.
- Privileged helper calls are bounded by context and output limits, and
  background scheduling is cancelled during graceful shutdown.
- High-risk identifier and archive-path validators have native Go fuzz targets;
  CI enforces a repository coverage floor and the local `make fuzz-smoke` target
  provides a short repeatable fuzz pass.
- TOTP replay counters are persisted in the control-plane database, so a panel
  restart cannot re-accept a code already consumed in the active time window.
- Recovery journals are processed independently; malformed entries are
  quarantined and incomplete database cleanup prevents unsafe site rollback.
- Repository rollback and pending-reconciliation drills are now a named Make/CI
  gate with retained result artifacts; disposable-host, power-loss, and
  provider-failure drills remain deployment acceptance requirements.
- The DR manifest now treats legacy job JSON as optional when the durable
  control-plane database is configured, matching the authoritative state model.
- Required DR artifacts with group/world permissions now fail the DR gate rather
  than being reported as warnings.
- Cloud inventory reports partial provider failures, limits provider output,
  and avoids inheriting panel-specific secrets into provider CLI processes.
- Restore uploads are synced before queue admission, backup inventory tolerates
  isolated corrupt artifacts, and audit-event filtering performs one verified
  file pass.
- Build requests require immutable OCI image digests. The privileged runner
  validates the artifact directory ownership and clears build output without
  following symlinks or crossing a filesystem boundary.
- The shared-hosting beta persists customer credentials separately from the
  administrator, requires per-customer TOTP, caps explicit site assignments by
  plan, and restricts customer site, backup, and job visibility to assignments.
- Administrators can enforce CPU, memory, task, PHP-FPM worker, and filesystem
  quota profiles for managed application processes, inspect live cgroup/site
  usage, and reconcile inactive desired profiles. Database counts and logical
  Redis allocations are plan-capped at the data/API boundary. Accounts are
  suspended while required host resource enforcement is pending, including
  startup reconciliation failures. Provider
  bandwidth, mail, and runtime Redis isolation still require external adapters.
- Site termination now enforces `STEPANEL_REQUIRE_OFFSITE_BACKUP=1` directly:
  it uploads (or re-confirms) the termination backup offsite before host state
  is destroyed, instead of only requiring a locally verified copy.
- Administrators can inspect a consolidated read-only Security Center and
  restore verified site files into an isolated, protected, no-index staging
  route, or queue administrator-only files-only/database-only restores with
  safety verification. Verified backup-dump restore into a newly provisioned
  staging database is available; transactional/live snapshot cloning,
  promotion, and outbound-email blocking remain unavailable.

## Required before exposing the panel to customers

### Identity and tenancy

The shipped customer-account beta is limited to a single host and assigned-site
authorization. Durable customer API tokens now provide hashed, expiring,
revocable automation credentials scoped to the owning account. Add durable
tenant/account ownership for every object, scoped support/reseller RBAC,
administrator token policy beyond the shipped read/operate scopes, OIDC/WebAuthn,
session revocation,
approval workflows for destructive actions, and tenant-aware audit/event
records. Every object and background job must be authorized against the tenant
at the data-access boundary, not only in HTTP handlers. Customer usernames are
also rejected when they collide with the configured administrator identity.

### Durable control plane

Jobs, customer accounts, site ownership, revocable sessions, environment,
Redis, SSH access, workers, PHP, tasks, deployments, resource profiles, and
backup schedules now use the versioned control-plane SQLite database, with
one-time imports from their legacy JSON files. The durable job table now
includes worker leases, renewal, expiry requeue, and lease-checked completion.
The queue supports atomic claims, lease renewal, expiry requeue, cancellation
requests, progress, bounded retry backoff, and dead-letter records. The
claims and worker mutations use SQLite row-level transactions across the
independent panel and worker processes, and cancellation is an authoritative
durable column rather than process-local state. The
cold-start loader treats the relational job state and lease columns as the
authority over serialized payload snapshots, so claim and expiry transitions
remain correct after a process restart. The
single-job status path also refreshes its state from SQLite, so an external
worker claim or cancellation is visible to the panel process without restart;
durable job listings refresh the same way and include jobs created by another
worker process.
cpmove restore, site-backup, certificate, WordPress, backup-restore, cloud,
and SSH action paths now run through the durable worker interface, including
persisted input, output, retries, and restart recovery. WordPress credentials
are encrypted with the configured account key and hidden from job-status
responses. The packaged single-host deployment runs the worker as an
independently supervised `stepanel-worker.service` using the same encrypted
control plane. Multi-host operation still requires scoped worker credentials,
mutual authentication, and an authenticated remote-agent protocol; those are
not implemented by this local service split.

### Hosting lifecycle

Implement complete, transactional lifecycle operations for domains/DNS/ACME
certificates, sites, PHP runtimes, databases/users, mailboxes/forwarding,
FTP/SFTP, quotas, service plans, and billing entitlements. The shipped SSH
key policy, deploy keys, and timer-backed task definitions still need full
tenant enforcement and operator lifecycle controls.
Site termination is now a confirmation-gated durable job with a verified
retained backup, ordered local cleanup, tenant detachment, and retry recovery.
Domain route mutations now persist pending/applied/deletion desired state and
reconcile after restart; tracked customer routes can be removed by their owner.
Customer route activation now requires a durable DNS TXT claim at
`_stepanel.<domain>` before a tenant route is applied; administrators can use an
explicit operator bypass. The TXT record is revalidated at each customer route
activation; removing it blocks new activation without deleting an existing
route, and repeated claim requests are idempotent for the owning site. This
proves control at activation time, but does not
replace registrar/DNS-zone ownership, DNSSEC, or ongoing ACME lifecycle checks.
Customer staging route activation uses the same TXT revalidation gate.
Mail, registrar, DNS zone ownership/DNSSEC, billing, and other
external-provider objects still need provider adapters and an operator-approved
cascade policy. Linode DNS record desired state is now durable, but it does not
constitute registrar or DNSSEC lifecycle support.

### Customer experience

Add a customer portal, file manager, Git-provider App/OAuth integrations, live
database cloning/promotion, customer backup
browsing/restore, notifications, API/webhooks, and clear operation progress.
All customer-visible operations should be
idempotent and explain what changed.

## Release gates

Before a shared-hosting launch, require an external security review, tenant
isolation tests, restore drills, upgrade/rollback drills, load and failure
testing, the documented RPO/RTO contract in [`DR_OBJECTIVES.md`](DR_OBJECTIVES.md), on-call ownership, data-retention policy, and a
support/compatibility policy. A deployment is not production-ready merely
because its container starts or its health endpoint is green.
