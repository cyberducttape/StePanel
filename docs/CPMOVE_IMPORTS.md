# cpmove imports

StePanel accepts gzip-compressed tar archives produced by cPanel, commonly named `cpmove-<account>.tar.gz`. The import form requires an account username and the literal confirmation `IMPORT`.

For retry-safe automation, send an `Idempotency-Key` header (1–128
characters; letters, numbers, `.`, `_`, `:`, and `-`). Reusing the same key
for the same account returns the original durable restore job instead of
starting a second restore, including after a panel restart. Use a new key for
a distinct archive or an intentional retry after reviewing the prior job.

## Recommended procedure

1. Take a filesystem and database snapshot of the destination.
2. Upload the archive through an authenticated HTTPS connection.
3. Let the migration card inspect the archive, then confirm the detected
   website files, database dumps, mailbox count, and destination username.
4. Start the import and watch the job status until it is `completed` or
   `failed`.
5. Restore website files first and verify permissions and application configuration.
6. Restore SQL only when the database dump is trusted and compatible.
7. Review the staged archive and application logs before removing it.

Website files are restored into a hosting account named after the cPanel user
(see [ADR-0007](adr/0007-home-account-layout.md)), the same layout cPanel uses:

| In the backup | Restored to |
| --- | --- |
| `homedir/public_html` (main domain) | `/home/<username>/public_html` |
| An addon domain | its own site at `/home/<username>/<addon-domain>` |
| A subdomain | its own site at `/home/<username>/<sub.domain>` |
| A parked domain | listed as an alias of the main site |

StePanel reads the domains and their document roots from the backup's
`userdata` files. Addon and subdomain files come from each domain's original
document root, so both the classic layout (`public_html/<addon>`) and the
current one (`/home/<user>/<addon>`) restore correctly. A nested document root
is moved to the domain's own directory rather than kept as a second copy
inside `public_html`. A document root outside the account's home is refused.
Each restored domain is created and cleaned up as a unit. A domain that cannot
be restored, for example because another site already serves that directory,
is reported in the job's `domain_errors` and leaves nothing behind. Routes are
not published automatically; publish each domain once its DNS points at this
server.

The cPanel username must be usable as a Unix account name: it starts with a
lowercase letter and is not a system or reserved name. An account that
already exists as a legacy site under `/var/www/sites/<username>` keeps that
layout, and its files are restored to `/var/www/sites/<username>/public`.

SQL files found
under the archive's `mysql` directory, including nested dump directories, are
restored to databases named `<username>_<database>`. If the dump name already
starts with the selected account prefix, StePanel preserves that database name
instead of duplicating the prefix. Existing files at the destination can be
overwritten.

For a local database, the root-owned database helper creates a temporary
importer with privileges only on the new target schema, streams the dump through
that account, removes the importer, and registers the resulting database for
restricted cleanup. Remote database restores use the operator-supplied account.
Managed database names are written to the site recovery transaction before
creation, allowing startup to remove completed imports if the wider site restore
was interrupted before commit.

## Mail restoration

When `STEPANEL_INSTALL_MAIL=1` is used, the installer installs Exim, Dovecot,
and SpamAssassin and creates a private mail staging root. cPanel mailbox data found
under `homedir/mail` is preserved under:

```text
/var/lib/ste-panel/mail/<account>/mail/
/var/lib/ste-panel/mail/<account>/etc/
```

The restore result reports staged mailboxes. cPanel's Exim and Dovecot files
are host-specific and are not copied into `/etc`; activating mail requires a
domain/mailbox mapping step, credentials, DNS records, TLS, and transport
policy on the destination.

SpamAssassin is installed and its daemon is started for mail installations, but
the destination administrator must still connect it to the chosen Exim
transport and review scoring, relay, and training policy before accepting mail.

## Compatibility

The importer is intentionally conservative. It handles regular files and
directories inside `.tar.gz` archives, with or without a top-level
`cpmove-<account>/` or cPanel full-backup directory. Website files, SQL dumps,
and staged mailbox data are restored or preserved. Symlinks, device nodes,
unusual cPanel metadata, DNS zones, and account-level quotas are not restored
automatically.
