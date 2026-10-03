# StePanel Code Organization

## Current Structure

Verified against the tree on 2026-10-02. Two binaries are built:
`stepanel` (the panel and worker, from the repository root) and
`stepanel-root` (the root broker, `cmd/stepanel-root`).

### Root package (`package main`, 87 non-test files)

The root package holds the HTTP application, its handlers, and workflow
orchestration:

- **Bootstrap and configuration**: `main.go` (startup, routes, subcommands),
  `config.go`, `assets.go`, `version.go`, `helpers.go`, `setup.go` (guided
  setup command)
- **Authentication and accounts**: `auth.go`, `accounts.go`,
  `account_management.go`, `account_activity.go`, `api_tokens.go`,
  `api_token_limiter.go`, `tenancy.go`
- **Sites and routing**: `sites.go`, `site_lifecycle.go`,
  `site_lifecycle_journal.go`, `site_creation_journal.go`, `site_lock.go`,
  `site_manager_bridge.go`, `site_usage.go`, `routes.go`, `domain_claims.go`,
  `certificates.go`, `node_proxy.go`, `staging.go`, `htaccess.go`
- **Backups and recovery**: `backups.go`, `backup_encryption.go`,
  `backup_restore.go`, `backup_restore_validation.go`, `backup_retention.go`,
  `backup_schedule.go`, `offsite.go`, `recovery.go`, `recovery_status.go`,
  `dr.go`, `retention.go`
- **Migrations and imports**: `cpmove.go`, `importer.go`, `wpress.go`,
  `wordpress.go`, `migration_scan.go`, `doctor.go`
- **Deployments and runtimes**: `deployments.go`, `git_deploy.go`,
  `git_keys.go`, `release_pipeline.go`, `release_activation_journal.go`,
  `node_deployment.go`, `node_tooling.go`, `apps.go`, `workers.go`, `runner.go`,
  `container.go`, `php.go`, `python.go`, `composer.go`, `environment.go`,
  `redis.go`, `resource.go`, `resources.go`
- **Jobs and tasks**: `jobs.go`, `tasks.go`, `task_webhook.go`
- **Databases**: `database.go`, `database_operations.go`
- **Control plane state**: `controlplane.go`, `state_persistence.go`,
  `metadata_cache.go`
- **Security and audit**: `security.go`, `security_center.go`,
  `security_headers.go`, `malware.go`, `audit_events.go`, `audit_outbox.go`
- **Operations and observability**: `health.go`, `capabilities.go`, `logs.go`,
  `metrics.go`, `services.go`, `support_bundle.go`, `failure_injection.go`
- **Infrastructure integrations**: `cloud.go`, `dns.go`, `dns_desired.go`,
  `ssh_access.go`, `ssh_inventory.go`

### Internal packages (`internal/`)

| Package | Responsibility |
|---------|----------------|
| `accounts` | Hosting account and plan store |
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
| `safehttp` | Outbound HTTP policy (public destinations only) |
| `session` | Durable, revocable session registry |
| `setup` | Guided first-time setup |
| `siteidentity` | The Go definition of a site's Unix identity |
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

## Future Refactoring Roadmap

### Phase 1: Executable Wrapper (Low Risk)
```
Root: package main (HTTP app)
      ↓ imports
cmd/stepanel/main.go: package main (bootstrap only)
      ↓ calls
stepanel.Run(config)
```

### Phase 2: Root → Importable (Medium Risk)
```
Root: package stepanel (HTTP app)
      ↓ imports
internal/auth/ (no circular deps)
internal/backup/ (types + logic)
internal/...
```

### Phase 3: Handler/Logic Separation (High Risk)
```
stepanel/ (App struct + HTTP handlers)
internal/sitesvc/ (site logic - no App struct)
internal/backupsvc/ (backup logic - no App struct)
internal/...
```

## Guidelines for New Code

1. **New domain logic?** → Consider internal/ package
2. **HTTP handlers?** → Root package
3. **Shared utilities?** → helpers.go or internal/
4. **New service layer?** → Proposal for Phase 3 refactoring

## Related Documents

- [REFACTORING_PLAN.md](../REFACTORING_PLAN.md) - Detailed refactoring roadmap
- [PRODUCTION_SCORECARD.md](archive/PRODUCTION_SCORECARD.md) - Historical code quality assessment
- [HELPER_ARCHITECTURE.md](HELPER_ARCHITECTURE.md) - Privilege separation design
