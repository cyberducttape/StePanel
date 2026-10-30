# Migration Doctor

**Status:** Read-only source preflight and cpmove migration evidence are implemented; mail activation, DNS cutover, and full installed-host recovery certification remain operator workflows.

Migration Doctor connects to the source over SSH using a fixed, non-interactive inspection script. It collects operating-system, PHP, database-client, storage, memory, CPU, and user-cron facts, then compares them with the configured destination. It is still an advisory preflight, not migration approval or production readiness evidence.

## Purpose

Before migrating a production site, operators need confidence that:
- Source server meets StePanel requirements
- Application dependencies are compatible
- Database schema is supported
- Custom code won't break in StePanel environment
- Migration will complete within acceptable timeframe

Unknown facts are reported as warnings or blockers rather than replaced with demo data. Operators must still verify mail identities and delivery, DNS records, SSL, application plugins, database contents, custom directives, and credentials before migration. A live source scan is advisory: it reads the source PHP CLI runtime, which may differ from a site's PHP-FPM pool.

## Implemented source inventory

### Implemented server analysis

- PHP version and loaded-extension detection
- OS, kernel, architecture, CPU, memory, and filesystem capacity
- MySQL/PostgreSQL client version and, when local source credentials permit,
  live database server version (the report labels which source was observed)
- PHP extension comparison against the destination PHP CLI runtime, and
  source/destination database compatibility warnings
- User crontab count
- Destination free-space comparison and transfer/capacity estimates

The cPanel archive import separately inventories DNS zone filenames and cron schedule expressions when present in `dnszones/` and `cron/`. Its import result identifies these as observed but not migrated. Individual zone records are not parsed or applied, and cron command text is deliberately omitted from reports because it can contain credentials. Cron metadata reads are capped at 1 MiB per file and 8 MiB total; evidence lists are capped at 500 entries and report additional omitted items.

The cpmove preflight also reads bounded `userdata/main` and per-domain userdata to show provisional source-domain/document-root mappings, extracts version comments from SQL dump headers when present, and checks archived Maildir folder layout and approximate message counts. These checks do not prove the source database server's live version, mailbox login/delivery, DNS correctness, or destination PHP-FPM compatibility. Missing evidence stays unknown or warning. The preview and completed import report can be downloaded as JSON or Markdown; the report includes component dispositions, domain mappings, database checks, mailbox validation, the DNS cutover checklist, and post-migration verification states.

SSH host keys should be supplied through `source_ssh_known_hosts`; strict host-key checking remains enabled. Private keys are accepted only when `STEPANEL_ACCOUNT_KEY` is configured, because the queued job must encrypt the durable payload.

### Not provided by current source scan

- WordPress core/plugin compatibility check
- Custom code static analysis (deprecated functions, compatibility)
- Database schema validation (storage engines, collations)
- Large file detection (may require special handling)
- Symlink detection in document root

### Reported migration estimates

- Estimated migration time based on measured source data where available
- Required downtime window recommendation
- Network bandwidth requirements
- Storage space needed on target

### Workflow boundaries

| Workflow | Supported | Notes |
|----------|-----------|-------|
| WordPress sites | ⏳ Not analyzed | Use the WordPress import workflow directly |
| cPanel accounts | ⏳ Not analyzed | Use the cpmove importer directly |
| Generic archives | ⏳ Not analyzed | Inspect the archive and follow its workflow requirements |
| Docker apps | ⏳ Not analyzed | Requires custom deployment config |
| Static sites | ⏳ Not analyzed | Validate the archive manually |

## Integration Points

- `POST /api/admin/migration-doctor` — Queue a read-only source scan
- `GET /api/admin/migration-doctor/status?job_id=:id` — Check job status
- cpmove preview and import evidence export as JSON and Markdown. A completed SSH source scan is included in both formats.

## Implementation Timeline

- **v0.8.0**: Basic server analysis (PHP, DB, storage)
- **v0.9.0**: Application-level analysis (WP plugins, code)
- **v1.0.0**: Pre-migration recommendations engine

## Related Documents

- [ARCHIVE_IMPORTER.md](./ARCHIVE_IMPORTER.md) — Generic archive import workflow
- [MIGRATION_CUTOVER.md](./MIGRATION_CUTOVER.md) — DNS, mail, cron, verification, and rollback checklist for cPanel cutover
- [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) — Migration capabilities by workflow
- [SECURITY.md](./SECURITY.md) — Security considerations during migration
