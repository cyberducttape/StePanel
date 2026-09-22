# Archive Importer

The Archive Importer allows administrators to inspect and import websites from compressed archives (`.tar.gz` or `.zip` files) stored in cloud storage or HTTP endpoints.

## Overview

The Archive Importer is useful for:
- Restoring sites from previous exports (StePanel backups)
- Importing sites from other hosting providers
- Bulk migration workflows
- Testing site imports before committing

The importer workflow:

1. **Phase 1 (Complete)**: Archive inspection, file extraction, and database dump identification
2. **Phase 2 (In Progress)**: Automated database restoration when credentials provided
3. **Phase 3 (Planned)**: UI dashboard with import status tracking

**Current status**: File extraction and database dump location complete. Operators receive clear restoration instructions. Phase 2 will automate this step when database credentials are provided in the request.

## API Endpoints

### Inspect Archive

Analyzes an archive without importing it. Returns site type, PHP version, database requirements, and any issues.

**Request:**
```bash
curl -X POST https://panel.example.com/api/admin/archive/inspect \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://s3.amazonaws.com/backups/site-backup.tar.gz",
    "config_path": "wp-config.php"
  }'
```

**Response:**
```json
{
  "status": "done",
  "inspection": {
    "url": "https://s3.amazonaws.com/backups/site-backup.tar.gz",
    "archive_type": "tar.gz",
    "size_bytes": 1073741824,
    "config_path": "wp-config.php",
    "config_type": "wordpress",
    "requirements": {
      "php_version": "8.0+",
      "database_type": "mysql",
      "database_name": "mysite_db",
      "database_user": "mysite_user",
      "extensions": ["mysqli", "curl", "gd", "mbstring", "zip"],
      "estimated_storage_gb": 5,
      "estimated_database_gb": 2
    },
    "structure": {
      "total_files": 25643,
      "total_dirs": 1243,
      "largest_files": ["wp-content/backups/backup.sql", "media/video.mp4"],
      "has_wordpress_core": true,
      "has_database": true
    },
    "issues": []
  }
}
```

**Parameters:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `url` | string | Yes | URL to the archive (supports S3, HTTPS, etc.) |
| `config_path` | string | Yes | Path to config file within archive (e.g., `wp-config.php`, `config/database.php`) |

### Start Archive Import

Begins the import process after reviewing inspection results. This endpoint queues an async job to extract and import the site.

**Request:**
```bash
curl -X POST https://panel.example.com/api/admin/archive/import \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://s3.amazonaws.com/backups/site-backup.tar.gz",
    "config_path": "wp-config.php",
    "site_name": "imported-site",
    "skip_analysis": false,
    "extract_directory": "wordpress"
  }'
```

**Response (Phase 1 Complete):**
```json
{
  "job_id": "import-12345",
  "status": "done",
  "site_name": "imported-site",
  "files_imported": 25643,
  "database_found": true,
  "database_location": "/var/backups/imported-site/database.sql",
  "next_steps": [
    "Verify site loads correctly and all content is present",
    "Restore database: mysql -u dbuser dbname < /var/backups/imported-site/database.sql",
    "Test site functionality and update DNS to point to this panel"
  ]
}
```

**Parameters:**

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `url` | string | Yes | URL to the archive |
| `config_path` | string | Yes | Path to config file within archive |
| `site_name` | string | Yes | Name for the new site in StePanel |
| `database_password` | string | No | Database password for Phase 2 automated restoration |
| `database_host` | string | No | Database host (default: localhost) |
| `auto_restore_db` | boolean | No | Automatically restore database if credentials provided (Phase 2) |

### Check Import Status (Phase 2)

Once implemented, this endpoint will return progress of an active import job.

**Request:**
```bash
curl https://panel.example.com/api/admin/archive/import/status?job_id=import-12345
```

## Archive Format Support

### tar.gz Archives

Standard compressed tar archives. Expected structure:

```
site-backup.tar.gz
├── wp-config.php           # Config file specified in request
├── wp-content/
├── wp-includes/
├── wp-admin/
├── index.php
├── backup.sql              # Optional: database dump
└── ...
```

### ZIP Archives

Standard ZIP files. Same directory structure as tar.gz.

## Config File Detection

The importer automatically detects site type from the config file:

### WordPress (wp-config.php)

Extracts:
- Database name, user (`DB_NAME`, `DB_USER`)
- Required PHP version (default: 8.0+)
- Required extensions: `mysqli`, `curl`, `gd`, `mbstring`, `zip`

Example config snippet:
```php
define('DB_NAME', 'mysite_db');
define('DB_USER', 'mysite_user');
define('DB_PASSWORD', 'password');
define('DB_HOST', 'localhost');
```

### Custom Config Files

For non-WordPress sites, manually review the config file. The importer will:
1. Report it as `config_type: "unknown"`
2. Issue a warning recommending manual review
3. Still provide archive structure analysis

## Import Issues

The importer identifies and reports issues at three levels:

### Errors
Block the import and must be resolved:
- `empty_archive` — Archive has no files
- `database_name_not_found` — Cannot parse database credentials from config
- `archive_read_failed` — Cannot extract archive

### Warnings
May indicate missing data or require manual setup:
- `config_type_unclear` — Could not auto-detect site type
- `no_database_found` — No `.sql` or `.sql.gz` file in archive; database must be restored separately

### Info
Informational messages:
- `archive_contains_backup_data` — Extra backup files detected; may increase import time

## Workflow Example

### 1. Export Site from Current Host

```bash
# Export WordPress site with database
tar -czf mysite-backup.tar.gz wp-config.php wp-content wp-admin wp-includes *.php backup.sql
# Upload to S3 or HTTPS endpoint
aws s3 cp mysite-backup.tar.gz s3://my-backups/
```

### 2. Inspect Archive

```bash
curl -X POST https://panel.example.com/api/admin/archive/inspect \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://s3.amazonaws.com/my-backups/mysite-backup.tar.gz",
    "config_path": "wp-config.php"
  }'
```

Review the response to verify:
- ✅ PHP version is supported
- ✅ Database type (MySQL/PostgreSQL)
- ✅ Required extensions are available
- ✅ Storage estimate fits plan
- ✅ Database backup is included

### 3. Import Site (Phase 1 Complete)

Submit the import request:

```bash
curl -X POST https://panel.example.com/api/admin/archive/import \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://s3.amazonaws.com/my-backups/mysite-backup.tar.gz",
    "config_path": "wp-config.php",
    "site_name": "mysite"
  }'
```

The files are extracted and the site is created. If a database dump is found, its location is returned in the response.

### 4. Restore Database (Phase 1 Manual, Phase 2 Automated)

Currently, database restoration is manual:

```bash
# Use the path returned in the import response
mysql -u mysite_user -p mysite_db < /var/backups/mysite/database.sql
```

Phase 2 will allow you to provide credentials for automated restoration:

```bash
curl -X POST https://panel.example.com/api/admin/archive/import \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://s3.amazonaws.com/my-backups/mysite-backup.tar.gz",
    "config_path": "wp-config.php",
    "site_name": "mysite",
    "database_password": "dbpassword",
    "auto_restore_db": true
  }'
```

## URL Requirements

Archives can be hosted on:
- **S3**: `https://s3.amazonaws.com/bucket/file.tar.gz` or `s3://bucket/file.tar.gz`
- **HTTPS**: `https://example.com/backups/file.tar.gz`
- **Presigned URLs**: AWS S3 presigned URLs with time-limited access

### Authentication

For private S3 buckets, use presigned URLs:

```bash
aws s3 presign s3://my-bucket/site-backup.tar.gz --expires-in 3600
# Returns: https://s3.amazonaws.com/...?X-Amz-Algorithm=...&X-Amz-Signature=...
```

For HTTP endpoints, use basic auth in URL:
```
https://user:password@example.com/backups/file.tar.gz
```

## Size Limits

- **Maximum archive size**: 5 GB
- **Maximum config file size**: 1 MB (only first 1 MB is read)
- **Maximum files per archive**: 1,000,000

## Security Considerations

### Archive Validation

- ✅ Archive type verified (gzip magic number check for tar.gz)
- ✅ Size limits enforced to prevent DoS
- ✅ Config files sanitized (only first 1 MB read)
- ✅ Admin-only API endpoints

### Config Parsing

- ✅ No code execution from config files
- ✅ Regex-based extraction of safe values only
- ✅ Unknown config types flagged for review

### Database Restoration

**Phase 1 (Current)**:
- ✅ Database dump located and validated
- ✅ Path provided to operator for manual restoration
- ✅ Restoration instructions included in next steps

**Phase 2 (Planned)**:
- 🔄 Automated restoration when credentials provided
- 🔄 Database dumps will be validated before import
- 🔄 Foreign key constraints checked
- 🔄 Character set compatibility verified

## Troubleshooting

### "Config file not found"

The `config_path` you provided doesn't exist in the archive. Try:

```bash
# List files in archive
tar -tzf site-backup.tar.gz | grep -E '(config|wp-config)' | head -20
```

Use the exact path shown (relative to archive root).

### "Archive type could not be determined"

The file extension doesn't match the actual format. Check:

```bash
# Verify archive format
file site-backup.tar.gz
# Should output: gzip compressed data, last modified: ...

file site-backup.zip
# Should output: Zip archive data ...
```

### "Database name not found"

The importer couldn't parse your config file. It may use:
- Different syntax than expected
- Environment variables instead of hardcoded values
- A different configuration system

For Phase 2, you'll be prompted to enter database details manually.

## Related Documentation

- [`docs/MIGRATION_DOCTOR.md`](MIGRATION_DOCTOR.md) — Analyze source servers before migration
- [`docs/CUSTOMER_WORKFLOWS.md`](CUSTOMER_WORKFLOWS.md) — Site backup and restore workflows
- [`docs/SECURITY_CLAIMS_VERIFICATION.md`](SECURITY_CLAIMS_VERIFICATION.md) — Audit trail for security claims
