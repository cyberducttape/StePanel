# Shared-hosting beta

Documentation version: `main / unreleased`; this document describes the
current branch and is not a promise that the `v0.6.0` release contains every
listed capability.

StePanel can now provide constrained customer workspaces on one managed host.
This is a deliberate first boundary, not a claim of cPanel/Plesk parity.

## What is enforced

- An administrator creates a customer account with a `starter`,
  `professional`, or `agency` plan and explicit site assignments.
- Every customer account has an independent bcrypt password hash and a unique
  unpadded Base32 TOTP seed of at least 160 bits. Customer MFA is always
  required, including outside production mode.
- `starter`, `professional`, and `agency` cap assignments at 1, 5, and 25
  sites respectively. Built-in plans apply aggregate account and per-site
  CPU, memory, process, PHP-worker, disk, and inode ceilings to newly assigned
  sites. Disk and inode enforcement requires a filesystem mounted with user
  quotas; failed quota application remains pending, suspends the affected
  account, and blocks a false applied state until an administrator verifies
  enforcement and explicitly unsuspends it.
- Administrators can apply resource profiles to managed application, worker,
  scheduled-task, and filesystem user-quota boundaries. Database provisioning,
  credential rotation, and deletion are customer-scoped and capped by plan;
  bandwidth and mail entitlements still require provider-specific enforcement.
  Redis allocations are visible to customers, but customer mutations fail
  closed until a reviewed Redis/Valkey runtime isolation adapter is configured;
  administrator allocations remain operator-managed.
- Startup and administrator reconciliation treat pending quota/cgroup state as
  unenforced; if a managed profile cannot be reapplied, its owning account is
  suspended until enforcement is restored and explicitly verified.
- Customer sessions can view only assigned site workspaces and their matching
  backup and job records. They can create a verified backup or add a domain
  route only for an assigned site. Customer route activation first requires
  `POST /api/sites/domains/claim`, publication of the returned TXT value at
  `_stepanel.<domain>`, and `POST /api/sites/domains/verify`; this is an
  ownership proof for the panel and is revalidated before route activation,
  not registrar, DNS-zone, DNSSEC, or ACME lifecycle management.
- Cloud, SSH, service, database administration, migration, application,
  certificate, security, and account-management APIs remain administrator-only;
  customer database lifecycle is limited to the customer-scoped database API.

## Administrator API

`POST /api/accounts` is administrator-only and expects:

```json
{
  "username": "acme",
  "password": "a unique password with at least 20 characters",
  "totp_secret": "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP",
  "plan": "starter",
  "sites": ["acme-site"]
}
```

`GET /api/accounts` returns account metadata and plan limits, never password
hashes or TOTP seeds. Administrators can update a customer's plan and site
assignments transactionally with `PATCH /api/accounts/{username}` using
`{"plan":"professional","sites":["acme-site"]}`. The API verifies that
each assigned document root exists and that no site is owned by another account
before persisting the change. Accounts also expose a `suspended` lifecycle flag;
`{"suspended":true|false}` immediately rejects active sessions and revokes
persisted sessions for that customer. It suspends **panel access**, not hosting
workloads: sites, services, data, backups, and external resources remain intact.
`DELETE /api/accounts/{username}` removes only the customer login record and
its sessions after all managed sites have been detached. It returns `409` while
sites remain assigned, preventing an orphaned hosting workload; it is not a
hosting-account teardown. Use the administrator-only durable site termination
workflow before removing a login.
When a plan or assignment changes, StePanel persists the affected resource
profiles as pending desired state, clamps ceilings that exceed the new plan,
preserves stricter operator settings, removes the old account aggregate from
unassigned sites, and attempts cgroup/PHP reconciliation. A helper failure
returns `202` with `pending_sites`; retry `POST /api/reconcile/resources` after
the host is healthy.
The endpoint is intentionally documented as **login removal**; no current API
claims to terminate the associated hosting workloads.
Account creation also requires each assigned site document root to already
exist, and a site can be assigned to only one customer account. This prevents
two customer identities from receiving authorization to the same site.
Account data is authoritative in `STEPANEL_CONTROL_PLANE_DB` and must be
included in host backups with `STEPANEL_ACCOUNT_KEY`. With that key, customer
TOTP secrets are AES-GCM encrypted at rest and never serialized as plaintext.
`STEPANEL_ACCOUNT_STATE` is a mode-`0600` legacy import source; existing
plaintext records are imported and encrypted on the next account write. The
default legacy path is beside the session state as `accounts.json`.
Administrators
can regenerate a customer's MFA secret with `POST
/api/accounts/{username}/mfa`; the new seed is returned once, existing sessions
are revoked, and the seed must be delivered through a secure channel.
Administrators can generate one-time recovery codes with `POST
/api/accounts/{username}/recovery-codes`; only bcrypt hashes are stored and the
codes are returned once.
For a full recovery, administrators use `POST /api/accounts/{username}/recover`.
It rotates the temporary password, TOTP seed, and recovery codes, marks both
`password_reset_required` and `mfa_enrollment_required`, and revokes sessions.
The customer completes the two steps through `/api/account/password` and
`/api/account/mfa`; each endpoint revokes the current session after success.

## Site environment variables

Production requires `STEPANEL_ENVIRONMENT_KEY` (at least 32 characters) for
encrypted site environment storage. Keep it stable and back it up with the
control-plane database.
Use `GET`, `PUT`, and `DELETE /api/sites/environment/{site}` to inspect metadata,
replace variables, or remove them. Secret variables are encrypted at rest and
are returned only as metadata; values are never returned after they are written.
Back up the environment state file together with the encryption key.
Updates render a root-owned systemd environment file and restart managed Node,
Python, and worker services so new values take effect. PHP applications should
consume environment values through their application configuration rather than
the global FPM process environment.

## Signed Git webhooks

Configure a distinct webhook secret for each site in the control-plane
webhook configuration, then use `POST /api/sites/git-webhook/{site}`.
Send `X-StePanel-Signature: sha256=<hex HMAC-SHA256>` with a JSON payload
containing `site`, `repository`, and an optional `ref`. The URL site, secret,
repository allowlist, and ref allowlist are all checked before deployment.
The legacy `STEPANEL_GIT_WEBHOOK_SECRET` shared-secret setting is retained only
for configuration compatibility and is not accepted for deployment.

## Redis / Valkey allocations

`GET`, `PUT`, and `DELETE /api/sites/redis/{site}` expose a site’s logical
Redis/Valkey allocation. The contract includes `database` (0–15), `namespace`,
`memory_mb`, and `eviction` (`allkeys-lru` or `noeviction`). The API reports
whether `redis-server` or `valkey-server` is installed. Customer `PUT` and
`DELETE` are rejected until actual ACL, namespace, and cgroup memory
enforcement is provided by a reviewed privileged adapter; administrator
allocations remain operator-managed.

## Composer

`GET /api/composer/{site}` reports Composer availability, `composer.json`,
`composer.lock`, and the last successful operation. `POST
/api/composer/{site}/install` accepts `development` and `optimize_autoloader`
booleans. It executes a fixed Composer install as the site account with
non-interactive, no-script, and no-plugin flags. Application build hooks must
remain in a separately sandboxed build runner.

## Per-site PHP runtime

`GET /api/sites/php/{site}` inventories installed PHP-FPM versions and the
site’s active profile. `PUT /api/sites/php/{site}` selects a version and sets
`memory_limit`, `max_execution_time`, `upload_max_filesize`, `post_max_size`,
`max_input_vars`, `opcache`, `display_errors`, and `error_reporting`. The
root-owned helper validates the selected pool configuration before reloading
FPM; managed Caddy and Apache routes use the selected version’s socket.

## Staging sites

`POST /api/staging` creates a distinct staging site and domain from a production
site. The request supports `files` and `environment`; only non-secret environment
values are copied, and the operation uses the site recovery journal. With
`database:true`, it can also perform a logical dump of one managed source
database and import it into a newly provisioned destination database after
explicit source/target credentials and ownership checks. New staging routes
support default no-index headers and optional bcrypt-backed Basic Auth;
outbound-email blocking, schema rollback, and promotion remain unavailable.

## Sandboxed build runner

`POST /api/runner/build` submits a site, immutable OCI image digest, and bounded command list to
the rootless Podman runner. The runner mounts source read-only and writes only
to the site artifact directory. It uses a separate network namespace, dropped
Linux capabilities, a read-only container filesystem, and a bounded temporary
area. Image tags are rejected; use an image reference ending in
`@sha256:<64 lowercase hex characters>`. The helper validates artifact
ownership and clears it without following symlinks or crossing a filesystem
boundary. Activation remains a separate StePanel atomic-release operation.

## Node developer tooling

`POST /api/node/tooling` accepts `{site, action, package_manager}`. Actions are
`install` or `build`; package managers are `npm`, `yarn`, and `pnpm`. The
privileged helper maps these to fixed commands, disables interactive prompts,
uses production environment settings, and runs them as the site user. It does
not accept arbitrary command text or install lifecycle scripts for pnpm.

## SSH / SFTP developer access

`GET`, `PATCH`, and `POST /api/sites/access/{site}` expose per-site access
policy and add validated SSH public keys. `DELETE
/api/sites/access/{site}/{label}` revokes a key. Private keys are never accepted
or stored, and responses expose fingerprints rather than private material.
Access policy and keys are applied by the root-controlled site helper with
pending-state reconciliation. The helper uses root-owned `authorized_keys`,
SFTP-only forced commands, and an explicit `nologin`/`bash` shell policy.
Operators must still configure and verify the host SSH daemon before enabling
customer access; this does not provide a chroot or replace sshd hardening.

## Background workers

Workers are managed with `GET /api/workers/{site}`, `PUT
/api/workers/{site}/{name}`, and `DELETE /api/workers/{site}/{name}`. Supported
types are `laravel`, `horizon`, `node`, `celery`, and `rq`; the helper maps
these to fixed commands and creates hardened systemd units with bounded memory,
task count, restart-on-failure, and site ownership. Arbitrary worker command
text is not accepted. Desired worker changes are persisted before helper
application; failures remain pending and are retried during startup
reconciliation. Worker logs are available through the site log API when
the host captures unit output into the site log directory.

## Site logs

`GET /api/sites/logs/{site}?source=...` reads up to 1,000 lines from an
allowlisted source: `access`, `error`, `php-fpm`, `php`, `application`,
`deployment`, `build`, `cron`, or `worker`. Use `lines=1..1000`, `filter=...`,
or `download=1`. Logs are read only from the site’s managed `logs` directory;
arbitrary paths and commands are not accepted. Helpers and application runners
should write their site-specific output there.

## WordPress operations

Authenticated users can query `GET /api/wordpress/status/{site}` and run the
closed set of operations through `POST /api/wordpress/{site}` with an action of
`status`, `update_core`, `update_plugins`, `update_themes`, `maintenance_on`,
`maintenance_off`, or `cron`. Actions require `wp-cli`, a valid `wp-config.php`,
site ownership, CSRF protection, and are bounded and audited. Arbitrary WP-CLI
arguments and repository build commands remain intentionally unsupported.

## Not yet available to customers

Do not market this beta as unrestricted shared hosting. It does not yet provide
mailbox/FTP lifecycle, browser file management, customer self-service scheduled
tasks, DNS/registrar lifecycle, provider-enforced bandwidth/mail/Redis quotas,
billing, customer-initiated restores, transactional database promotion,
support workflows, reseller roles, or a multi-host control plane. These gaps require
host-level enforcement and durable tenancy-aware state, not merely dashboard
forms. Site-scoped scheduled tasks, deploy keys, resource profiles,
Security Center, and restore-to-staging are documented in
[`FEATURES.md`](FEATURES.md) with their beta/operator boundaries.
