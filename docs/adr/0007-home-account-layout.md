# ADR-0007: Hosting accounts own a Unix home with cPanel-style document roots

- Status: Accepted
- Date: 2026-10-21

## Context

StePanel gave every site its own Unix account and stored it at
`/var/www/sites/<site>`, serving `/var/www/sites/<site>/public`. A customer
account was only a panel login that owned a list of sites. Operators moving
from cPanel expect the opposite shape: one hosting account is one Unix user,
the main domain is served from `/home/<user>/public_html`, and a cpmove backup
for user `alice` restores into `/home/alice`. Customers also expect a panel of
their own (domains, files, databases, backups, PHP, cron, logs) rather than
the operator page with sections hidden.

## Decision

A **hosting account is a Unix user** named after the account, with home
`/home/<user>`. Team members (managers, developers, viewers) keep their own
panel logins but act on the owner's Unix user.

A **site is a document root inside an account**. Its placement records the
owning account, the Unix user, a kind, and a directory:

| Kind | Directory | Document root |
| --- | --- | --- |
| `main` | `public_html` | `/home/<user>/public_html` |
| `addon` | the domain | `/home/<user>/<domain>` |
| `subdomain` | the domain | `/home/<user>/<sub.domain>` |

Each account has at most one main site, and a directory belongs to at most
one site. Addon and subdomain roots sit next to `public_html`, never inside
it, so one domain's files are not reachable through another domain.

Site names stay the internal key for APIs, jobs, journals and locks.

### Data model

Two control-plane tables carry the layout:

- `account_identities(account, unix_user, created_at)`: the Unix user
  allocated to an account. A Unix user name is never reassigned, even after
  the account is deleted, matching the tombstone rule of `site_identities`.
- `site_placements(site, account, unix_user, kind, directory, created_at)`:
  one row per site in the new layout.

Sites without a placement keep the legacy `/var/www/sites/<site>` layout.
There is no automatic migration; StePanel is pre-production and lab hosts are
reinstalled.

### Privilege boundary

The panel database is writable by the unprivileged panel, so the root side
never takes a path from it. The broker resolves a site's placement and passes
only the Unix user and directory name. Each helper then rebuilds the path and
refuses to act unless:

- the user's passwd home is exactly `/home/<user>`;
- the user is a member of the root-managed `stepanel-users` group, which the
  installer creates and only `stepanel-userctl` adds members to;
- the directory matches `public_html` or a validated domain name;
- the path is not a symlink at any component.

A request therefore cannot steer root at `/root`, a system account, or an
unmanaged home.

### Filesystem

- `/home/<user>`: mode 0711, owned by the user, so the web server can reach
  document roots without listing the home.
- Document roots: mode 2750, owned by the user with the web server group, as
  site roots are today.
- PHP-FPM pools stay per site (`stepanel-<site>`), each running as the
  account's Unix user.
- User and project quotas move from `/var/www` to the filesystem holding
  `/home`; an account's quota covers all of its sites.
- Manager staging for the new layout lives on the `/home` filesystem
  (`/home/.stepanel-manager-staging`) so activation remains an atomic rename.

## Alternatives considered

- **Keep one Unix user per site and only move files to `/home/<site-user>`.**
  Preserves isolation between an account's sites, but home names would not
  be the customer's username and a cpmove account could not keep its name.
- **Addon domains inside `public_html`** (classic cPanel). Matches older
  backups, but serves every addon domain's files through the main domain.
- **Migrate existing `/var/www/sites` sites automatically.** Adds a large,
  risky durable migration for hosts that do not exist yet.

## Consequences

- Sites in one account share a Unix user, so they are no longer isolated from
  each other; isolation is between accounts. The `site_identities` uniqueness
  rule continues to apply only to legacy sites.
- Quotas, SSH/SFTP and FTPS become account-level.
- The panel, worker and broker units can no longer use `ProtectHome=true`:
  they need `/home` writable. Ordinary permissions still keep the
  unprivileged panel account out of homes StePanel does not manage, and the
  root helpers act only on `stepanel-users` members.
- Production requires user quotas on the filesystem holding `/home` as well
  as `/var/www`.
- The rollout is phased so CI stays green: placements and path resolution,
  then the site manager and broker, then the helpers and installer, then the
  cpmove restore, then the customer panel.
