# StePanel Customer Workflows

This guide describes self-service workflows available to customers in the shared-hosting beta.

## The Customer Panel

Customers sign in at the panel address and land on `/panel`, a page of their
own (administrators keep the operator dashboard at `/`). Its sections are:

- **Home:** the hosting account, its system user and home directory
  (`/home/USER`), and each website with its document root.
- **Websites:** add a website and open a website's workspace for domains,
  PHP, deployments, databases, backups, logs, workers, scheduled tasks,
  security and settings.
- **Files:** the file manager described below.
- **Domains & SSL:** every connected domain, with a link to connect more.
- **Databases & backups** and **PHP, cron & logs:** shortcuts into each
  website's workspace.
- **Account:** plan and limits, team members, password, MFA and API tokens.
- **Activity:** recent operations and the account's audit trail.

`GET /api/account/hosting` returns the same account summary for scripts
(token scope `site:read`).

## Hosting Account Layout

Like cPanel, a hosting account is one system user with a home directory. The
main website is served from `/home/USER/public_html`; each addon domain or
subdomain website is served from `/home/USER/DOMAIN`. PHP state for a website
lives in `/home/USER/.stepanel/sites/SITE`. Everything in the home belongs to
the account's system user, which is also the user an SFTP login, cron tasks
and PHP run as.

## Create a Site

Account owners can create a blank PHP site within their plan. The first
website becomes the main site in `public_html`; later ones are addon domains
(or subdomains) and need their domain, which also names their folder. The
site gets a PHP-FPM pool running as the account's system user and a
placeholder page, and is assigned to your account before creation starts.
Creation runs as a durable job; if it fails, nothing is left behind and the
site no longer counts against your plan.

```bash
curl -X POST "https://panel.example.com/api/sites" \
  -H "Authorization: Bearer YOUR_API_TOKEN" \
  -H "Idempotency-Key: create-mysite" \
  -H "Content-Type: application/json" \
  -d '{"site": "mysite", "template": "php"}'
```

Add `"kind": "addon"` (or `"subdomain"`) and `"domain": "shop.example.org"`
to create a website in its own folder; `"kind": "main"` asks for
`public_html` explicitly. Without a kind, the first website is the main one
and later ones are addon domains.

The response is `202 Accepted` with a `job_id` and a `status_url` to follow.
An API token needs the `site:deploy` scope.

- Only the account **owner** of an active (not suspended) account can create
  sites; members with other roles receive `403`.
- A request that would exceed the plan's site limit, or uses a name that is
  already taken, receives `409`.
- Creating a site does not publish a domain. Connect a domain afterwards; it
  must pass domain ownership verification before the route goes live.

WordPress and Node site templates are not available yet. Existing sites can
use the tenant-scoped Git deployment workflow described in
[Git deployments](GIT_DEPLOYMENTS.md); the repository and ref are validated,
the deployment runs as a durable job, and the site must have a configured
deploy key or use a public repository allowed by the host policy.

## Manage Files

The file manager browses and edits a website's document root. Every change
runs as the account's system user, exactly as over SFTP, under the site's
mutation lock, and is recorded in the account's audit trail
(`site.files.write`, `site.files.mkdir`, `site.files.rename`,
`site.files.delete`).

- Open files up to 2 MiB in the editor; save or upload files up to 4 MiB.
  Use SFTP for larger files.
- Saving replaces the file atomically and keeps its permissions; new files
  are created with mode 0640 and new folders with 0750.
- Renames never replace an existing file. Deleting a folder deletes
  everything in it.
- Paths outside the document root, `..`, and symbolic links are refused.
- Websites from before hosting accounts (in `/var/www/sites`) are not
  available in the file manager.

API tokens need `files:read` to list, read and download, and `files:write`
to change files. Viewers can read files; developers can also change them.
The endpoints are listed under `/api/sites/files/` in the
[OpenAPI contract](openapi.yaml).

## Deploy from Git

An account owner, manager, or API token with `deploy:write` can deploy an
assigned site from Git. The operation is queued and returns a job ID, so a
browser disconnect does not cancel the deployment.

```bash
curl -X POST "https://panel.example.com/api/sites/git-deploy" \
  -H "Authorization: Bearer YOUR_API_TOKEN" \
  -H "X-CSRF-Token: YOUR_CSRF_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"site":"mysite","repository":"https://github.com/example/mysite.git","ref":"main"}'
```

Follow the returned `status_url`. Git deployment replaces the site files but
does not migrate databases or run repository-provided build scripts. Use the
release-pipeline workflow when a separately allowlisted, sandboxed build is
required.

## Restore-to-Staging: Test Before Going Live

The restore-to-staging feature lets you safely test restoring your site from a backup **before applying the restore to your production site**. This is the safest way to verify your backups actually work.

### How It Works

1. **Create an isolated staging site** — A non-indexed route for testing, separate from your production route
2. **Restore files and databases** — Your backup is extracted into the staging site, completely isolated from production
3. **Test the restored site** — Browse your site at the staging domain, verify all content, run tests
4. **Decide what to do next** — The beta workflow leaves production untouched; promotion or cleanup is an operator-managed action

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

The request completes after the verified files have been restored and the staging route has been activated. The response includes the staging domain. If the request fails, no staging route is published.

#### 4. Test Your Site

1. Point your test domain to the staging site
2. Visit `https://staging.example.com` and verify:
   - All pages load correctly
   - Database queries work
   - Forms submit successfully
   - Search functions work
   - Third-party integrations function
   - Cron jobs and background tasks execute

If you notice any issues, leave production unchanged and contact an administrator to remove the staging route or investigate the backup.

#### 5. Finish the Test

There is no customer self-service promote or discard endpoint in the shared-hosting beta. Promotion is intentionally not exposed as a tenant action because it would replace the managed site state. Ask an administrator to review the staging result and perform any required production change.

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
- A staging destination from a previous restore still exists
- Ask an administrator to clean up the previous staging route before retrying

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
