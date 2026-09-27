# Migration Doctor

**Status:** Preview only; the endpoint is available, but remote inspection is not implemented

Migration Doctor is a planned pre-migration analysis tool. The current endpoint accepts SSH connection details but deliberately returns a synthetic, not-implemented result; it does not connect to or inspect the source server. Do not use its response as migration approval or production readiness evidence.

## Purpose

Before migrating a production site, operators need confidence that:
- Source server meets StePanel requirements
- Application dependencies are compatible
- Database schema is supported
- Custom code won't break in StePanel environment
- Migration will complete within acceptable timeframe

The future implementation is intended to provide automated analysis to catch issues early. Until then, operators must inspect and validate the source server manually.

## Planned Features

### Server Analysis

- PHP version compatibility check (7.4 - 8.3 range)
- Required PHP extensions detection (mysqli, pdo, gd, curl, etc.)
- System resource assessment (memory, storage, network)
- Database server type and version detection
- Mail system configuration scan (Exim, Postfix, Sendmail)

### Application Analysis

- WordPress core/plugin compatibility check
- Custom code static analysis (deprecated functions, compatibility)
- Database schema validation (storage engines, collations)
- Large file detection (may require special handling)
- Symlink detection in document root

### Migration Impact Assessment

- Estimated migration time based on archive size
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

- `POST /api/admin/migration-doctor` — Queue a preview response (synthetic today)
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
