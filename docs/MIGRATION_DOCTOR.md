# Migration Doctor

**Status:** Planned for v0.8.0+

Migration Doctor is a pre-migration analysis tool that evaluates source server compatibility and readiness before beginning a site migration to StePanel.

## Purpose

Before migrating a production site, operators need confidence that:
- Source server meets StePanel requirements
- Application dependencies are compatible
- Database schema is supported
- Custom code won't break in StePanel environment
- Migration will complete within acceptable timeframe

Migration Doctor provides automated analysis to catch issues early.

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

### Capability Matrix

| Workflow | Supported | Notes |
|----------|-----------|-------|
| WordPress sites | ✅ Full | All WP versions 5.0+ |
| cPanel accounts | ✅ Full | Via cpmove importer |
| Generic archives | ⚠️ Analysis only | Files + manual DB restore |
| Docker apps | ⚠️ Limited | Requires custom deployment config |
| Static sites | ✅ Full | Minimal dependencies |

## Integration Points

- `POST /api/admin/migration-analysis` — Analyze remote server
- `GET /api/admin/migration-analysis/:id/status` — Check analysis progress
- `GET /api/admin/migration-analysis/:id/report` — Retrieve findings
- Export as PDF or JSON for stakeholder review

## Implementation Timeline

- **v0.8.0**: Basic server analysis (PHP, DB, storage)
- **v0.9.0**: Application-level analysis (WP plugins, code)
- **v1.0.0**: Pre-migration recommendations engine

## Related Documents

- [ARCHIVE_IMPORTER.md](./ARCHIVE_IMPORTER.md) — Generic archive import workflow
- [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) — Migration capabilities by workflow
- [SECURITY.md](./SECURITY.md) — Security considerations during migration
