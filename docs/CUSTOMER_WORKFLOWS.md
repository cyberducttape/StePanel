# StePanel Customer Workflows

This guide describes self-service workflows available to customers in the shared-hosting beta.

## Restore-to-Staging: Test Before Going Live

The restore-to-staging feature lets you safely test restoring your site from a backup **before applying the restore to your production site**. This is the safest way to verify your backups actually work.

### How It Works

1. **Create an isolated staging site** — A temporary, non-indexed copy of your site created specifically for testing
2. **Restore files and databases** — Your backup is extracted into the staging site, completely isolated from production
3. **Test the restored site** — Browse your site at the staging domain, verify all content, run tests
4. **Approve or discard** — If the restore looks good, you can promote it to production; otherwise, discard the staging site

### Step-by-Step Workflow

#### 1. Identify Your Backup

List available backups for your site:

```bash
curl -X GET "https://panel.example.com/api/backups?site=mysite" \
  -H "Authorization: Bearer YOUR_API_TOKEN"
```

Response includes backup names like `20260919-143022.000000000-mysite`.

#### 2. Start a Restore-to-Staging Operation

Create a staging restore by POSTing a restore request:

```bash
curl -X POST "https://panel.example.com/api/backups/restore-to-staging" \
  -H "Authorization: Bearer YOUR_API_TOKEN" \
  -H "X-CSRF-Token: YOUR_CSRF_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "backup": "20260919-143022.000000000-mysite",
    "site": "mysite",
    "domain": "staging.example.com"
  }'
```

**Required fields:**
- `backup` — The backup name (from the list above)
- `site` — Your site name (must be assigned to your account)
- `domain` — The domain to serve the staging site from (must point to the same server)

**Optional fields for database restore:**
- `database` — Source database name in the backup
- `target_database` — New database name to create (e.g., `mysite_staging`)
- `target_user` — New database user to create
- `target_password` — Password for the new user

#### 3. Verify the Restore Completed

The API returns a job ID. Monitor the job status:

```bash
curl -X GET "https://panel.example.com/api/jobs?id=JOB_ID" \
  -H "Authorization: Bearer YOUR_API_TOKEN"
```

When the job completes with status `done`, your staging site is ready to test.

#### 4. Test Your Site

1. Point your test domain to the staging site
2. Visit `https://staging.example.com` and verify:
   - All pages load correctly
   - Database queries work
   - Forms submit successfully
   - Search functions work
   - Third-party integrations function
   - Cron jobs and background tasks execute

If you notice any issues, you can discard the staging restore and investigate the backup or your site configuration.

#### 5. Promote Staging to Production or Discard

**To promote the staging restore to production:**

```bash
curl -X POST "https://panel.example.com/api/sites/mysite/restore-complete" \
  -H "Authorization: Bearer YOUR_API_TOKEN" \
  -H "X-CSRF-Token: YOUR_CSRF_TOKEN"
```

**To discard the staging site without affecting production:**

```bash
curl -X DELETE "https://panel.example.com/api/sites/mysite/staging" \
  -H "Authorization: Bearer YOUR_API_TOKEN" \
  -H "X-CSRF-Token: YOUR_CSRF_TOKEN"
```

### Best Practices

1. **Always test on staging first** — Never restore directly to production without testing
2. **Test with production data** — Use realistic data volumes and content to catch performance issues
3. **Document what you test** — Keep a checklist of critical features to verify after each restore
4. **Keep backups recent** — Schedule regular automated backups so staging tests use current data
5. **Monitor error logs** — Check application logs for errors that might not be visible in the UI

### Restore Verification Checklist

After a restore-to-staging operation, verify:

- [ ] Homepage loads without errors
- [ ] All pages accessible (navigation, menus work)
- [ ] Database connectivity confirmed
- [ ] Search functionality works
- [ ] Forms can be submitted
- [ ] File uploads work
- [ ] Images and assets load
- [ ] Third-party APIs/integrations function
- [ ] Admin/backend interfaces work
- [ ] Application performance is acceptable
- [ ] Error logs show no critical issues

### Common Issues and Solutions

**Q: "Staging destination already exists" error**
- A staging site from a previous restore still exists
- Discard the old staging site first, then retry

**Q: "Backup verification failed" error**
- The backup archive may be corrupted
- Contact support if backups consistently fail verification

**Q: Database restore fails**
- Verify the source database name exists in the backup
- Ensure target database credentials meet system requirements
- Check for SQL mode compatibility warnings

**Q: Staging site loads slowly**
- Large restores may take time to extract files
- Monitor job status and wait for completion
- Check server disk space (20GB+ recommended for large backups)

## Rate Limiting and API Quotas

API requests are rate-limited to prevent abuse:

- **Authenticated endpoints**: 100 requests per minute
- **Restore operations**: Limited to 1 concurrent restore per site
- **Backup list**: 1000 results maximum

If you hit rate limits, wait a few seconds and retry.

## Support and Escalation

For issues with restore-to-staging:

1. Check the error message and common solutions above
2. Review your application logs for errors
3. Verify your backup verification status (all backups should show `archive_verified: true`)
4. Contact support with:
   - Your site name
   - The backup name you're trying to restore
   - The exact error message
   - Your API request (with sensitive credentials redacted)

## Backup and Restore Assurance

Before relying on any backup, you should verify it actually restores. StePanel automatically verifies every backup:

```bash
curl -X GET "https://panel.example.com/api/backups?site=mysite" \
  -H "Authorization: Bearer YOUR_API_TOKEN"
```

Each backup result includes:

- `verified_at` — When the backup was last verified
- `archive_verified` — Whether the archive archive integrity is confirmed
- `database_dump_verified` — Whether database dumps are readable
- `manifest_signed` — Whether the backup manifest is cryptographically signed

A backup is only safe to rely on if `archive_verified` is `true` and `verified_at` is recent (within 7 days).

## Next Steps

- [Set up automated backups](OPERATIONS.md#backup-scheduling)
- [Monitor backup status](OPERATIONS.md#backup-verification)
- [Configure offsite backup retention](INTEGRATIONS.md#offsite-backup)
