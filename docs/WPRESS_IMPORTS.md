# WordPress WPress imports

StePanel incorporates the standalone WPress Restore workflow from the sibling
`wpress_restore` project and adapts it to StePanel site roots and database
configuration.

## Host prerequisites

Install these commands on the destination host:

- `wpress-extract` from the WPress extractor package;
- WP-CLI (`wp`);
- `mariadb` or `mysql`.

Override executable paths with `STEPANEL_WPRESS_EXTRACT` and `STEPANEL_WPCLI`.
Production configurations must use absolute executable paths, and wp-cli must
be `/usr/local/bin/wp`. The root helper runs that fixed path as the site's
isolated account, so the imported site's PHP never executes with panel
privileges. The site is sealed to that account before the first wp-cli call.
Set `STEPANEL_ACCOUNT_KEY` before enabling imports: the database password is
held in an encrypted durable job payload and is never returned by job-status
APIs. Preserve this key with the control-plane database during recovery.
Use `/api/wpress/preflight` to verify availability.

## Restore procedure

1. Take a filesystem and database snapshot.
2. Open the WordPress migration card in the authenticated dashboard.
3. Select the `.wpress` archive and a site account.
4. Enter lowercase database and database-user suffixes using letters, numbers,
   and underscores, plus a new 16-128 character database password using the
   supported password-safe character set.
5. Optionally enter the destination URL and table prefix.
6. Enable overwrite only after verifying the backup.
7. Type `WPRESTORE` and wait for the job to complete.
8. Verify WordPress, permalinks, plugins, uploads, and database connectivity.

The resulting names are prefixed with the site account, for example
`example_wordpress` and `example_wpuser`. The restore creates the database and
local database user through the restricted local helper (or the explicitly
configured remote administrative connection), imports `database.sql`, and uses WP-CLI for serialized-safe table
prefix and URL replacement.

When present, `package.json` metadata is applied after the database and files
are restored: the archived active plugin list, theme, and stylesheet are
updated through WP-CLI, and the base64-encoded `Server[".htaccess"]` payload is
decoded into the site's document root. Invalid metadata is rejected and the
site transaction is rolled back.

Uploads stream once into a private staged object; free space for the
archive, its extracted tree, the site staging tree, and
`STEPANEL_MIN_FREE_BYTES` is admitted before the body is read. API clients
must send the `backup` file part after every other form field.

Archives are extracted into a private staging directory. Symlinks are rejected,
the database dump is not copied into the public site, and a failed overwrite
attempt restores the previous site directory. WordPress code and plugins are
untrusted after extraction; scan and update them before placing the site on
public traffic.

On local installations, uploaded SQL runs as the new site database user and
therefore has privileges only on the new target schema. The root-only managed
database ledger supports cleanup of interrupted provisioning.
The database and user are also recorded in the site recovery transaction before
creation, so an unclean shutdown removes them before restoring the prior files.
