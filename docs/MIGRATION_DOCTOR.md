# Migration Doctor

**Status:** Read-only source preflight is implemented; staged migration orchestration remains separate.

Migration Doctor connects to the source over SSH using a fixed, non-interactive inspection script. It collects operating-system, PHP, database-client, storage, memory, CPU, and user-cron facts, then compares them with the configured destination. It is still an advisory preflight, not migration approval or production readiness evidence.

## Purpose

Before migrating a production site, operators need confidence that:
- Source server meets StePanel requirements
- Application dependencies are compatible
- Database schema is supported
- Custom code won't break in StePanel environment
- Migration will complete within acceptable timeframe

Unknown facts are reported as warnings or blockers rather than replaced with demo data. Operators must still verify mail, DNS, SSL, application plugins, database contents, custom directives, and credentials before migration.

## Planned Features

### Implemented server analysis

- PHP version and loaded-extension detection
- OS, kernel, architecture, CPU, memory, and filesystem capacity
- MySQL/PostgreSQL client and database-family detection
- User crontab count
- Destination free-space comparison and transfer/capacity estimates

SSH host keys should be supplied through `source_ssh_known_hosts`; strict host-key checking remains enabled. Private keys are accepted only when `STEPANEL_ACCOUNT_KEY` is configured, because the queued job must encrypt the durable payload.

### Planned application analysis

- WordPress core/plugin compatibility check
- Custom code static analysis (deprecated functions, compatibility)
- Database schema validation (storage engines, collations)
- Large file detection (may require special handling)
- Symlink detection in document root

### Migration Impact Assessment

- Estimated migration time based on measured source data where available
- Required downtime window recommendation
- Network bandwidth requirements
- Storage space needed on target

### Planned Capability Matrix

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
- Export as PDF or JSON for stakeholder review is planned

## Implementation Timeline

- **v0.8.0**: Basic server analysis (PHP, DB, storage)
- **v0.9.0**: Application-level analysis (WP plugins, code)
- **v1.0.0**: Pre-migration recommendations engine

## Related Documents

- [ARCHIVE_IMPORTER.md](./ARCHIVE_IMPORTER.md) — Generic archive import workflow
- [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) — Migration capabilities by workflow
- [SECURITY.md](./SECURITY.md) — Security considerations during migration
