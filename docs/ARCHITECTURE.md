# Architecture

StePanel is deliberately small at this stage. The Go process owns the control-plane HTTP API and server-rendered dashboard. The operating system owns the webserver, PHP, database, and systemd lifecycle.

## Request lifecycle

The same admission boundary applies to read-only inspection and privileged
mutations. A `cpmove` inspection stops after validation and metadata extraction;
an authorized restore records a job before any background work starts. The
job worker then crosses only the narrowly scoped helper/database boundaries it
needs.

```mermaid
sequenceDiagram
    autonumber
    participant C as Browser / API client
    participant P as StePanel HTTP API
    participant A as Auth + CSRF + rate limits
    participant I as cpmove inspector
    participant J as Durable async job store / worker
    participant M as MySQL / MariaDB
    participant W as Apache / Caddy helper

    C->>P: HTTPS request
    P->>A: Authenticate session and validate CSRF
    A-->>P: Admit or reject (audit rejected mutations)
    alt POST /api/cpmove/inspect
        P->>I: Bounded upload stream
        I->>I: Validate gzip/tar paths and inspect manifest
        I-->>P: Safe metadata and findings
        P-->>C: Inspection response
    else POST /api/cpmove/import
        P->>I: Preflight and archive validation
        I-->>P: Approved restore plan
        P->>J: Persist job and preflight audit event
        J-->>C: Job ID / polling URL
        J->>J: Stage archive and restore site transaction
        J->>M: Create account-prefixed schema and import SQL
        J->>W: Apply validated vhost/config and reload
        J->>J: Persist result and completion audit event
        C->>P: GET job status
        P->>J: Read durable state
        J-->>C: State, result, or failure
    end
```

The database and webserver are external system resources. SQL is executed
through the selected database boundary, while Apache/Caddy changes are made
through root-owned helpers that validate their complete configuration before a
reload. The control-plane service account does not edit active webserver
configuration directly.

The container image packages only the StePanel control plane. Caddy/Apache/OpenLiteSpeed, MySQL/MariaDB or PostgreSQL, PHP, and site files remain external concerns in container deployments.

## Go package boundaries

The current root package is deliberately kept buildable as one package:
the HTTP handlers, shared configuration, test fixtures, and embedded dashboard
are still tightly coupled. A mechanical directory move would obscure those
dependencies without improving the runtime boundary.

The structural refactor is now underway. Authentication rate limiting and
peer-address normalization, durable deployment history, revocable session
state, bounded filesystem accounting, and asynchronous job admission have
been extracted behind internal package seams. The
remaining boundaries should be extracted in this order:

1. `internal/auth` for CSRF, MFA, and the remaining authentication policy.
2. `internal/jobs` for durable state, worker admission, and per-target
   serialization.
3. `internal/migration` for cpmove/WPress inspection, staging, and recovery.
4. `internal/backup` for verified archives, retention, and offsite publishing.
5. `internal/operations` for cloud, SSH, service, and privileged-helper
   adapters.

The current extracted seams are `internal/auth` for bounded login throttling
and trusted peer-address extraction, `internal/deployment` for deployment history,
`internal/session` for durable, revocable session entries,
`internal/operations` for site mutation locks, `internal/usage` for bounded
filesystem accounting, `internal/state` for atomic state writes, and
`internal/jobs` for bounded asynchronous admission. These packages retain
atomic persistence or pure concurrency behavior with package-level tests.

The HTTP layer should remain an assembly point that supplies interfaces for
these packages. Each extraction should preserve the existing tests and add a
package-level contract test before the next boundary is moved. This makes
package decomposition an incremental reliability improvement rather than a
large rewrite.

This is an active engineering boundary, not a claim that the extraction is
complete. New code should avoid adding unrelated state or platform commands to
`main.go`; feature work should first introduce a narrow interface or domain
file, then move that seam into `internal/` once its dependencies are stable.
The next package extractions are intentionally gated on interfaces that can be
tested independently of the HTTP server and embedded dashboard.

```text
Browser
   │ HTTPS via Caddy (default), Apache, or another reverse proxy
   ▼
StePanel HTTP server
   ├── dashboard and static assets
   ├── process liveness and persistent-storage readiness endpoints
   ├── site/database inventory and constrained Git release switching
   └── cpmove inspection/import API
          ├── staging area: /var/lib/ste-panel/imports
          ├── site roots: /var/www/sites/<account>/public
          └── Database client → MySQL/MariaDB or PostgreSQL socket
```

## Import safety model

1. The upload is size-limited.
2. gzip and tar streams are parsed before any restore.
3. Absolute and parent-traversing archive paths are rejected.
4. The archive is copied to a private, timestamped staging directory.
5. Website files are copied to a target-specific root.
6. SQL restore is opt-in, uses account-prefixed database names, refuses an
   existing destination database, and removes newly created databases when a
   restore fails.

When configured, StePanel provides administrator authentication with signed,
expiring, server-revocable sessions persisted in private control-plane state.
Password-hash rotation invalidates previously issued sessions. CSRF tokens for mutating forms, login rate limiting,
production-required replay-resistant TOTP, security response headers, and JSONL audit
events with explicit actor and target identity. Unsafe authenticated requests
must persist a preflight audit event before reaching their handler. The default
Caddy host integration provides automatic HTTPS; alternate deployments must
provide an equivalent TLS boundary. Webserver snippets and
systemd application units cross narrowly validated, root-owned helper
boundaries; the service account cannot edit active configuration directly.
Request-facing helper invocations are context-bound with bounded lifetimes and
output, preventing a wedged service command from exhausting worker capacity.
The shared-hosting beta adds separately persisted customer credentials,
relational tenant-site ownership, customer-specific TOTP, and an explicit
assignment allowlist. Customer sessions may see only their assigned site
workspaces, matching backups, and matching job records; administrator-only APIs
retain the privileged operational boundary. Ownership data is durable in the
control-plane database, and session validation reads that shared database so
multiple panel instances do not accept stale or revoked sessions. High-
availability database failover, durable cross-host job routing, and agent
authorization still remain outside the current contract.
Cloud CLI children receive a filtered environment so panel session, audit, and
database secrets are not inherited.
Local database administration crosses a root-owned helper boundary. Uploaded
SQL is executed through an ephemeral account with privileges only on the newly
created target schema. Remote database deployments use the explicitly supplied
credential and remain an operator-managed trust boundary.

PHP site domains use a root-owned helper selected for Caddy or Apache. It
serializes configuration changes with Node proxy changes, rejects conflicting
managed domains, binds PHP requests to an active per-site FPM socket, validates
the complete webserver configuration, and restores the prior snippet if
validation or reload fails. Caddy migrations may translate a conservative
subset of `.htaccess`; unsupported lines are reported and apply is fail-closed
unless the operator explicitly accepts a partial conversion.

Git site deployment runs as the unprivileged control-plane account and accepts
only HTTPS repositories from an exact hostname allowlist. Checkout disables
interactive credentials, applies a time and entry limit, rejects symlinks and
special files, and removes Git metadata before activation. The final directory
switch is serialized and atomic on the site filesystem; ownership/isolation is
then restored through the site helper. Repository code is never executed as a
build step by the control plane.

## Durable restore state

Restore and certificate jobs are recorded atomically before background work
starts. cpmove restores, site backups, certificate issuance, WordPress
restores, and backup restores use the durable worker path with persisted input
and output. The durable queue supports row-level upserts, atomic worker claims,
per-target serialization, leases, renewal, progress, bounded retry, cancellation requests, and
dead-letter state. A restart preserves completed results and marks work
interrupted by an unclean shutdown as failed. Site overwrites use a transaction journal on the
site filesystem: the previous document root is moved into the recovery
transaction before deployment. Newly created database names and users are
journaled in the same transaction before provisioning. Startup removes managed
databases first and then rolls back every uncommitted site transaction. Invalid
journals are quarantined for inspection and do not block unrelated recovery;
site rollback is withheld while database cleanup is incomplete. Scheduler and
cleanup goroutines are cancelled during graceful shutdown. Recovery artifacts
are retained for the same period as restore staging data.

## Verified backups

Backups are built outside the live site and recovery trees. Site files and
optional locally managed database dumps are written into a private gzip/tar
archive. The publisher records a SHA-256 digest for every regular entry and for
the complete archive, then reopens and reads the entire archive before an atomic
directory rename makes it visible. Database dumps use single-transaction mode;
the root helper only dumps databases whose ownership ledger matches the site.

This verifies artifact integrity, not application-level consistency for files
or nontransactional database tables that change during the backup. Put
`STEPANEL_BACKUP_ROOT` on a dedicated backup filesystem, replicate completed
directories off-host or to immutable storage, and run periodic restore drills.

## Admission and health

Liveness is deliberately dependency-free so a full disk does not cause a
restart loop. Readiness checks durable job persistence plus free space on import,
backup, and recovery filesystems. Restore admission separately checks both the
upload staging filesystem and the destination site filesystem. Global job slots,
per-site serialization, upload bytes, and archive-entry counts provide bounded
concurrency and work size.

Audit events are HMAC-authenticated and linked by monotonically increasing
sequence and previous-hash fields. Separately HMAC-authenticated chain state survives
log rotation and detects tail/prefix inconsistency; startup and readiness expose
audit persistence failures. This is tamper evidence, not immutable storage, so
the log, state, and key still require independent retention.

The authenticated audit-events endpoint verifies and filters the chain in one
file pass. Query cost is still proportional to retained log size, so large
installations should rotate only after verification and retain indexed exports
for historical searches.
