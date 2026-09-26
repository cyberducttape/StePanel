# StePanel production-readiness wiki

This page is the operator-facing reference for running StePanel safely in a
production hosting environment. It complements the shorter installation and
operations guides and records the security and reliability boundaries that are
important during incident response.

## What StePanel is

StePanel is a Go control plane for a single Linux host. It authenticates an
administrator, invokes narrowly scoped root-owned helpers, manages site
metadata, and queues long-running backup and restore work. Apache,
OpenLiteSpeed or Caddy, PHP-FPM, Node, the database server, mail, and FTP remain
host services managed by the installer and helper scripts.

### Webserver integrations

Caddy is the default. Its root-owned PHP-site and proxy snippets are written to
`/etc/caddy/stepanel.d`, imported by `/etc/caddy/Caddyfile`, validated, and
loaded with an atomic rollback-aware reload. The `.htaccess` migration tool
converts a deliberately constrained directive subset and reports every
unsupported line. With `STEPANEL_WEBSERVER=openlitespeed`, proxy snippets are written to
`/usr/local/lsws/conf/vhosts/stepanel/proxy` and OpenLiteSpeed is restarted
after validation; include that directory from the relevant OLS listener/vhost
rewrite configuration. Apache remains available for workloads that require
direct module compatibility. OpenLiteSpeed PHP site provisioning requires an
operator-reviewed listener/vhost template at
`/etc/stepanel-ols-vhost.template`; the installer will not use the Apache
vhost helper for OLS.

The container and Kubernetes packages run the control plane only. They do not
provide access to a host's systemd, Apache, PHP-FPM, or database services.

## Security model

- Bind the panel to loopback or a private management network and terminate TLS
  at a trusted reverse proxy.
- Configure `STEPANEL_SESSION_SECRET` with at least 32 random characters.
- Prefer `STEPANEL_ADMIN_PASSWORD_HASH`; if `STEPANEL_ADMIN_PASSWORD` is used,
  treat the environment and process supervisor as secret-bearing.
- Enable TOTP with `STEPANEL_ADMIN_TOTP_SECRET` and protect recovery material.
- Keep the audit key root-readable only. Verify audit continuity with the
  offline verifier after incidents or restores.
- Helpers must be absolute, root-owned, non-writable by site users, and limited
  to the documented verbs. Do not point helper settings at shell wrappers that
  accept arbitrary user input.
- Keep managed roots separate from user-controlled symlink farms. Restore and
  backup workflows reject archive traversal, special files, and symlinks.

## Deployment checklist

1. Install on a supported Debian/Ubuntu or RHEL-family host.
2. Select the database engine and exact repository version where required.
3. Set a panel FQDN, strong administrator credentials, session secret, and
   `STEPANEL_ADMIN_TOTP_SECRET` before exposing the service. MFA is a release
   gate even though an existing installation can still start without it.
4. For Caddy, verify DNS, ports 80/443, automatic HTTPS, secure cookies, and
   HSTS; for Apache/OpenLiteSpeed, complete an equivalent TLS termination
   configuration before exposure.
5. Confirm `/livez` and authenticated `/readyz` responses.
6. Verify helper paths, ownership, sudo policy, writable roots, and free space.
7. Run a small backup and restore rehearsal before accepting customer data.
8. Configure an offsite target and set `STEPANEL_REQUIRE_OFFSITE_BACKUP=1`.
   The target must be independently administered and use provider retention
   lock/immutability where available; StePanel cannot prove that property.
9. Configure log rotation, metrics scraping, alerting, and host-level backups.
10. Run authenticated `/api/doctor` and resolve every `fail` result before
    launch. A transport-security `warn` requires documented reverse-proxy
    evidence; a `pass` only means the application TLS files are configured.
11. Exercise SIGTERM shutdown during a disposable restore and confirm active
    jobs drain, schedulers stop, and readiness reports the service as draining.

## Day-to-day operations

The dashboard and JSON endpoints expose service state, security checks,
capabilities, recent jobs, and request correlation IDs. Treat a failed helper,
audit persistence error, or readiness failure as an operational incident rather
than retrying blindly. Long-running jobs survive graceful shutdown; after an
unclean shutdown, inspect reconciled failed jobs and transaction journals before
starting another restore.

If startup reports quarantined recovery journals, preserve the quarantine
directory and reconcile the affected resources before removal. A database
cleanup failure intentionally prevents the corresponding site rollback so the
operator can recover both resources safely.

Use the job status endpoint for polling instead of repeatedly submitting the
same operation. Restore admission limits and per-target locks are intentional:
they protect database state and prevent concurrent overwrites.

## Backup and restore runbook

- Confirm sufficient free space before creating an archive.
- Include databases only when the root-owned database helper is configured.
- Preserve the archive, manifest, and SHA-256 values together.
- Verify an archive offline before moving it between hosts.
- Restore into a test site first when the source is untrusted or the archive is
  unusually large.
- Keep the transaction and recovery directories on the same filesystem as the
  destination so atomic rename and crash recovery semantics hold.
- After a failed overwrite, inspect the recovery journal and retained previous
  document root before manually deleting anything.

## Node and proxy applications

Node deployment validates the site, installed version, local/private backend,
port, and domain. The application helper owns the systemd unit; Apache proxy
configuration is managed separately. Treat a proxy-helper failure after an app
deployment as a partial deployment and reconcile the app and proxy state before
retrying.

Git deployment is a separate file-release operation for pre-built public trees.
Configure `STEPANEL_GIT_ALLOWED_HOSTS`, monitor retained
`.stepanel-previous-*` directories, and use `/api/sites/git-rollback` for an
atomic file rollback. It does not restart Node, migrate databases, run package
managers, or inject secrets; coordinate those operations explicitly.

## Cloud and SSH operations

Configure one provider with `STEPANEL_CLOUD_PROVIDER` for authenticated cloud
inventory and audited asynchronous lifecycle actions. Linode additionally
supports DNS records, load-balancer backends, and snapshot lifecycle operations;
AWS and OpenStack currently provide inventory and server lifecycle actions.
Configure `STEPANEL_SSH_SERVERS` with aliases from the service account's SSH
configuration for strict-host-key health checks and allowlisted service
restarts/reboots. Keep provider credentials and SSH keys in protected host
credential stores, never in site-controlled files.

## Observability and incident response

Capture structured request logs, `X-Request-ID`, job IDs, audit events, service
status, and readiness results. Alert on repeated 5xx responses, failed jobs,
audit persistence failures, low disk space, and helper execution failures.

During an incident:

1. Restrict panel access at the network layer.
2. Preserve logs, audit files, job state, transaction journals, and helper output.
3. Stop starting new restores; allow active work to finish or shut down
   gracefully.
4. Verify audit-chain integrity and compare manifests/checksums with the source.
5. Rotate administrator credentials and session secret if compromise is
   suspected.
6. Re-enable traffic only after readiness, helper, and backup/restore checks
   pass.

## Known limitations and planned hardening

- Destination parent validation currently performs a filesystem walk before
  opening the destination. On hosts where an untrusted principal can mutate
  managed parent directories concurrently, use filesystem permissions and
  private staging to reduce exposure; directory-FD/openat2 traversal remains a
  future hardening item.
- Job, account, tenant ownership, and session state use the transactional
  control-plane database. Very high-volume or multi-host fleets still require
  PostgreSQL-class HA storage and independently monitored workers.
- Git file deployment, app process deployment, and proxy deployment are
  separate API operations. Operators should use the site workspace and audit
  history to reconcile partial failures explicitly.
- cPanel mailbox restores are staged with the site transaction. Preserve the
  recovery journal until the restored mail tree has been verified.

## Release and upgrade policy

Builds are versioned from `version.go`; Helm, Kubernetes, Terraform, and
OpenAPI metadata must match. Before release, run `make check`, inspect the
generated artifacts, create an annotated `vX.Y.Z` tag, and test an in-place
upgrade with a backup and rollback rehearsal.
