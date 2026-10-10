# StePanel Code Organization

## Current Structure

Verified against the tree on 2026-10-10. Three binaries are built from
`cmd/`:

- `cmd/stepanel` — the panel and worker; a thin `main` that calls into the
  root package
- `cmd/stepanel-root` — the root broker
- `cmd/stepanelctl` — the read-only operator CLI for the authenticated API

### Root package (`package stepanel`, 99 non-test files)

The root package holds the HTTP application, its handlers, and workflow
orchestration:

- **Bootstrap and configuration**: `main.go` (startup, subcommands), `routes.go`,
  `config.go`, `assets.go`, `version.go`, `helpers.go`, `setup.go` (guided
  setup command), `api_errors.go` (client-safe error responses)
- **Authentication and accounts**: `auth.go`, `accounts.go`,
  `account_management.go`, `account_activity.go`, `api_tokens.go`,
  `api_token_limiter.go`, `tenancy.go`
- **Sites and routing**: `sites.go`, `site_lifecycle.go`,
  `site_lifecycle_journal.go`, `site_creation_journal.go`, `site_lock.go`,
  `site_creation.go`, `site_operations.go`, `site_manager_bridge.go`,
  `site_usage.go`, `domain_claims.go`,
  `certificates.go`, `node_proxy.go`, `staging.go`, `staging_cleanup.go`,
  `htaccess.go`
- **Backups and recovery**: `backups.go`, `backup_encryption.go`,
  `backup_restore.go`, `backup_restore_validation.go`, `backup_retention.go`,
  `backup_schedule.go`, `offsite.go`, `recovery.go`, `recovery_status.go`,
  `dr.go`, `retention.go`, `startup_recovery.go`
- **Migrations and imports**: `cpmove.go`, `importer.go`, `wpress.go`,
  `wordpress.go`, `wordpress_maintenance.go`, `wpcli.go`, `migration_scan.go`,
  `doctor.go`, `upload_admission.go`
- **Deployments and runtimes**: `deployments.go`, `git_deploy.go`,
  `git_keys.go`, `release_pipeline.go`, `release_activation_journal.go`,
  `app_activation_journal.go`,
  `node_deployment.go`, `node_tooling.go`, `apps.go`, `workers.go`, `runner.go`,
  `container.go`, `php.go`, `python.go`, `composer.go`, `environment.go`,
  `redis.go`, `resource.go`, `resources.go`
- **Jobs and tasks**: `jobs.go`, `tasks.go`, `task_webhook.go`,
  `event_streams.go`
- **Databases**: `database.go`, `database_operations.go`
- **Control plane state**: `controlplane.go`, `state_persistence.go`,
  `metadata_cache.go`, `encryption_format.go`
- **Security and audit**: `security.go`, `security_center.go`,
  `security_headers.go`, `malware.go`, `audit_events.go`, `audit_outbox.go`, `audit_contract.go` (see
  [AUDIT_CONTRACT.md](AUDIT_CONTRACT.md))
- **Operations and observability**: `health.go`, `capabilities.go`, `logs.go`,
  `metrics.go`, `services.go`, `support_bundle.go`, `failure_injection.go`
- **Infrastructure integrations**: `cloud.go`, `dns.go`, `dns_desired.go`,
  `ssh_access.go`, `ssh_inventory.go`

### Internal packages (`internal/`)

| Package | Responsibility |
|---------|----------------|
| `accounts` | Hosting account and plan store |
| `archivesafe` | The single policy for reading untrusted archives: format detection, entry-type rules, path normalization, confined extraction |
| `audit` | HMAC-chained audit logger, audit levels and handlers |
| `auth` | Authentication policy independent of the HTTP assembly: login rate limiting, legacy token deprecation |
| `backup` | Backup manifest, result, and restore request types |
| `deployment` | Durable deployment-history state |
| `doctor` | Server inventory types and analysis for the migration doctor |
| `domainname` | The single host-name validator and IDNA policy |
| `helper` | Bounded subprocess execution (`RunCapped`), safe paths, atomic writes, process locks |
| `http` | Route-class HTTP timeout configuration, including the event-stream class |
| `importer` | Archive analysis and extraction for generic archive imports |
| `jobs` | Concurrency primitives for asynchronous work |
| `metadata` | SQLite indexes for backups (including offsite state) and tasks |
| `migration` | Versioned control-plane schema migrations |
| `operations` | Durable distributed locks shared by host operations |
| `recovery` | Restore rehearsal history and per-site recovery assessment |
| `rootbroker` | Root broker: typed requests, validation, helper schema, scheduler, journals, and the client |
| `secretbox` | Context-bound sealing of small secrets at rest (environment secrets, MFA seeds, durable job payloads) |
| `safehttp` | Outbound HTTP policy (public destinations only) |
| `session` | Durable, revocable session registry |
| `setup` | Guided first-time setup |
| `siteidentity` | The Go definition of a site's Unix identity |
| `sitelifecycle` | Site lifecycle orchestration as domain logic, independent of HTTP (currently termination) |
| `sites` | Site lifecycle filesystem primitive (`SiteManager`) |
| `startup` | Startup phase timeline |
| `state` | Durability primitives for control-plane state stores |
| `testing` | Failure-injection helpers for recovery tests |
| `upload` | Single-pass multipart archive staging (hash, size limit, in-flight free-space checks) |
| `usage` | Bounded site filesystem usage measurement |

### Privileged helpers

Root-owned helper scripts live in `deploy/integrations/stepanel-*` and are
installed to `/usr/local/sbin`. In production the panel never runs them
directly: it sends requests to `stepanel-root` over a peer-authorized Unix
socket, and the broker validates each request before running a helper. See
[ROOT_BROKER_INTEGRATION.md](ROOT_BROKER_INTEGRATION.md).

### Placement guidance

If a file touches root, the database, and HTTP handlers, it stays in the root
package for now. Logic that does not depend on the HTTP layer belongs in an
`internal/` package with its own tests.

## Refactoring direction

The root package is already importable (`package stepanel`) and the binaries
are thin wrappers in `cmd/`. Further extraction is incremental: when a domain
is touched, its logic moves into an `internal/` package behind an interface,
while HTTP handlers stay in the root package for now. See
[ADR 0005](adr/0005-incremental-go-package-boundaries.md) and the
"Package Refactoring Strategy" section of [CLAUDE.md](../CLAUDE.md).

## Guidelines for New Code

1. **New domain logic?** → An `internal/` package with its own tests
2. **HTTP handlers?** → Root package
3. **Shared utilities?** → `internal/helper` or another `internal/` package
4. **Privileged host work?** → A typed request in `internal/rootbroker`

## Related Documents

- [ARCHITECTURE.md](ARCHITECTURE.md) - System design and components
- [ROOT_BROKER_INTEGRATION.md](ROOT_BROKER_INTEGRATION.md) - Privilege separation and the typed broker
- [adr/](adr/) - Architecture decision records
