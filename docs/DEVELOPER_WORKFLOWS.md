# Developer workflows

StePanel keeps application code execution outside the control-plane process.
Site configuration and lifecycle actions are authenticated, audited control
plane operations; package installs and source builds run as the isolated site
identity or inside the dedicated Podman runner.

## Environment and runtime

Configure encrypted variables with `PUT /api/sites/environment/{site}`. Desired
values are persisted before host application; if helper application is
interrupted, startup reconciliation retries the persisted values. Secret
values are never returned after write; `PUT` merges per-variable `set`,
`preserve`, and `delete` operations (see [Shared hosting](SHARED_HOSTING.md#site-environment-variables)). Environment deletion restores the
previous host environment if durable state removal fails. Updates render a root-owned systemd
environment file and restart managed Node, Python, and worker services. Use
`GET`/`PUT /api/sites/php/{site}` to inspect installed FPM versions and apply a
validated site profile for memory, execution, uploads, input limits, OPcache,
errors, and version-specific socket selection. PHP changes are persisted as
desired state before FPM application; helper failures remain `pending` and are
retried during startup reconciliation.

## Resource profiles (Preview/Beta)

Administrators can `GET`/`PUT /api/sites/resources/{site}` to apply an
enforceable profile for managed Node/Python application and worker processes:
CPU quota/weight, `MemoryHigh`/`MemoryMax`, I/O weight, task/PID maximum, and
isolated PHP-FPM `pm.max_children`. Plan-assigned sites also inherit a root-owned
aggregate account systemd slice, so multiple sites cannot multiply the plan's
CPU, memory, I/O, or task envelope without bound. The helper renders a root-owned per-site systemd slice and
attaches managed application/worker units to it. The desired profile is saved
as `pending` before host application and becomes `applied` only after both the
systemd slice and FPM pool validate successfully.
Scheduled-task units are also assigned to this slice, so CPU, memory, I/O, and
task ceilings apply consistently to cron-like work when a site profile exists.
If no site profile is configured, each task uses a conservative local fallback
ceiling of 100% CPU, 1 GiB memory, and 256 processes. When a site profile is
configured, those CPU, memory, and process values are propagated into each task
unit, while the site slice remains the parent boundary for all managed work.
When `disk_mb` and `inodes` are supplied, the site helper applies Linux user
quotas with `setquota`; the request fails closed unless the site filesystem is
already mounted with user-quota support. These are per-site user quotas, not a
portable filesystem-quota setup. Built-in hosting plans provide CPU, memory,
process, and PHP-worker envelopes; disk/inode plan entitlements remain
unavailable because they depend on host filesystem configuration.
Removing both values queues an explicit quota-clear operation and retries it
through reconciliation if the host mutation is interrupted.

`GET /api/sites/resources/{site}` also reports observed systemd-slice state.
Administrators can call `POST /api/reconcile/resources` to re-apply pending or
inactive desired profiles after a helper failure or host restart; each repair
is audited.

`GET /api/sites/usage/{site}` reports bounded regular-file bytes, files, and
directories without following symlinks. It reports measured usage; configured
resource profiles may additionally enforce disk/inode ceilings through the
filesystem user-quota provider described above.

Mutating environment, resource-profile, scheduled-task, and worker requests
for the same site share the site operation lock with deployments, staging, and
restore-to-staging. This prevents concurrent helper calls from overwriting
systemd units, environment files, PHP limits, or other site-level state;
unrelated sites remain concurrent. Startup and administrator reconciliation
uses the same lock before applying pending desired state.
Application lifecycle, runtime tooling, deploy-key, route conversion, and
WordPress/cPanel restore operations use the same boundary, including when a
restore runs asynchronously as a persisted job.
Managed database provisioning/deletion/credential rotation, Redis allocation,
WordPress actions, and malware quarantine are serialized by site or managed
object identity as well, so concurrent lifecycle requests cannot interleave
their helper calls.

Administrators can queue a files-only restore with `POST
/api/backups/restore-files` using `backup`, `site`, and the explicit
`confirm=RESTORE_FILES` value. The archive and signature are verified before a
recovery-journaled replacement; databases are deliberately preserved. Full
database restore remains separate planned work; database-only restore is
available below for existing managed databases. To restore a selected verified
dump into a new staging database, also supply `database`, `target_database`,
`target_user`, and `target_password`; the privileged helper refuses existing
destination objects, imports only the verified source dump, and removes the
new database if a later staging step fails.

Administrators can queue a database-only restore with `POST
/api/backups/restore-database`, supplying `backup`, `site`, `database`, and
`confirm=RESTORE_DATABASE`. The selected database must be registered to the
site and present in the verified backup. StePanel creates a new verified
pre-restore safety backup before importing the dump. Database schema rollback
is manual; application rollback does not reverse schema migrations.

When an off-site rclone target is configured, administrators can queue
`POST /api/backups/restore-offsite-files` with `backup`, `site`, and
`confirm=RESTORE_OFFSITE_FILES`. StePanel retrieves only the fixed manifest,
archive, checksum, and optional signature objects for that site/backup ID,
then performs the same verification and journaled files-only restore. Remote
database restore and off-site browsing remain unavailable.

For an off-site database dump, use `POST /api/backups/restore-offsite-database`
with `backup`, `site`, `database`, and `confirm=RESTORE_OFFSITE_DATABASE`.
This retrieves the fixed remote objects, creates a verified local safety backup,
and uses the same ownership checks and database helper as local restore.

Administrators can use `GET /api/security/center` for a consolidated,
read-only host posture view: existing authentication/privileged-helper checks,
service states, free disk/inodes, and backup schedule failures. It deliberately
does not mutate firewall, package, SSH, or Fail2Ban state.

This does **not** yet impose plan-level disk/inode/project entitlements,
network/bandwidth limits, database limits, or Redis ACL/memory enforcement.
Those still need host/provider-specific controls before they can be presented
as shared-hosting tenant limits.
FPM Lens remains review-first: use its evidence-backed target/max recommendation
as an administrator input; StePanel does not auto-apply a Lens plan.

## Composer and Node

`GET /api/composer/{site}` reports Composer, `composer.json`, `composer.lock`,
and the last successful operation. `POST /api/composer/{site}/install` runs a
fixed, non-interactive install as the site user. It disables scripts and
plugins; use the build runner for repository-controlled build behavior.

`POST /api/node/tooling` supports fixed npm, Yarn, and pnpm install/build
actions as the site user. Node application start/restart remains managed by the
application lifecycle helper.

## Sandboxed builds (Shipped)

`POST /api/runner/build` accepts a site, immutable OCI image digest, and up to 16 bounded build
commands. Image tags are rejected; use an image reference ending in
`@sha256:<64 lowercase hex characters>`. The rootless Podman runner mounts a
private copy of the source read-only (never the live document root) and a
fresh per-build output directory writable, drops capabilities, uses a
read-only container filesystem and a separate network namespace. A build
replaces the site's `.stepanel-artifact` only when it succeeds, so a failed
build keeps the last good artifact. Podman storage lives in
`/var/lib/containers/stepanel-runner/<site user>`, outside the web tree; on
SELinux hosts containers run as `container_t` and the artifact is restored to
the site's label before publication. Storage from earlier versions in
`/var/www/sites/<site>/.local/share/containers` is no longer used and can be
removed with `podman system reset` as the site account. CPU, memory, and PID ceilings are taken from the
site resource profile (with conservative defaults for legacy sites). It does
not activate a release: review the artifact
and use the existing atomic deployment workflow for activation/rollback.
`GET /api/deployments?site={site}` provides durable, site-scoped build and
activation records (commit, artifact path, preserved release, stage, outcome,
and timestamp). This is the release-object foundation; fully automated
build-to-activation and database migration orchestration remains Preview work.

## Release pipeline (Preview/Beta)

`POST /api/deployments/run` connects the safe pieces in one authenticated
operation: it performs an allowlisted Git checkout into a non-live release
directory, optionally creates a verified files-and-managed-database backup,
runs the supplied bounded commands in the rootless runner, verifies the build
artifact, and atomically activates it while preserving the previous release.
Same-site Git and pipeline operations are serialized, while unrelated sites
remain concurrent. The response includes a stable `deployment_id`; checkout,
backup, build, and activation records for the request share that ID and can be
viewed as one release timeline. Staging creation and restore use the same destination lock
so they cannot race with one another or with an active release operation.

Build commands must write the complete deployable release tree to `/artifact`.
The checked-out source is mounted read-only, so a build cannot modify it in
place. A failed checkout, backup, build, or artifact validation leaves the live
release untouched. Application rollback restores the previous files only; it
does **not** reverse database migrations. Declarative database migration and
post-activation health-check stages remain planned before this endpoint is
appropriate for unattended production deployment.

Git activation and rollback retain the newest previous release and prune older
validated StePanel release directories according to
`STEPANEL_GIT_RELEASE_RETENTION` (default `3`, range `1..100`). Retention
never considers the active `public` tree for deletion.

## Git deploy keys (Shipped)

`POST /api/sites/git-key/{site}` creates a read-only per-site ED25519 deploy
key and returns its public half. Add that public key to GitHub, GitLab, or
Bitbucket, then deploy an allowlisted `git@host:owner/repository.git` source.
The private half never enters Go state, the API response, or a build container;
root-owned `stepanel-gitctl` uses it only for a non-interactive clone.

## Scheduled tasks (Shipped)

`GET`/`PUT`/`DELETE /api/tasks/{site}/{name}` manages a bounded site-identity
systemd service and timer. Use a systemd `OnCalendar` expression (for example,
`*-*-* *:*:00` for each minute), select PHP/Node/Python/shell, provide a
timeout, minimum interval, missed-run policy, CPU/memory/process limits, and an
optional HTTPS completion webhook. Webhooks follow the shared outbound policy
(`internal/safehttp`): only public internet addresses are reachable, the check
runs on the address actually connected to at delivery time (so DNS changes
after saving cannot redirect it to loopback, private, or metadata ranges), and
redirects are not followed. Each task has a single-run concurrency
limit; systemd terminates it at its configured timeout. The UI exposes the
current run, a manual stop action, consecutive failures, and the last 20
execution results. Ten consecutive failures automatically disable the timer.
Task scripts and history are root-owned under `/var/lib/stepanel/tasks/{site}`
so the site identity can execute but cannot silently rewrite the command or
history. This is deliberately not a writable server crontab.
If a helper or persistence step is interrupted, the task remains `pending` and
is retried during startup or through administrator-only
`POST /api/reconcile/tasks`.

## Staging (Preview/Beta)

`POST /api/staging` creates a recovery-journaled staging site and route from
safe regular-file copies. It may copy non-secret environment variables, but
never production secrets. Staging applies `X-Robots-Tag: noindex, nofollow` by
default through the managed webserver route; send `"no_index":false` only for
an explicitly reviewed exception. Set `basic_auth:true`, `auth_user`, and an
initial `auth_password` to protect the route with Basic Auth when using Caddy
or Apache; only a bcrypt
hash is persisted by the Apache/Caddy helper and the password is discarded
after the request. Database promotion, schema rollback, and outbound-email
blocking remain planned integrations.

`POST /api/backups/restore-to-staging` restores a fully verified backup's
regular site files into a new isolated site and validated route. It can also
restore one selected verified database dump into a newly provisioned managed
staging database when the target database credentials are supplied. It
deliberately refuses existing destinations and cleans up a newly created target
database if the import or route transaction fails.
`POST /api/backups/restore-offsite-to-staging` accepts the same fields plus a
required `source_site`; it downloads the named backup from the configured
rclone layout before entering the same verification and rollback flow.

## Access, logs, and workers

Site access APIs manage validated SSH public-key fingerprints plus SFTP/shell
policy. Keys are never private-key material. Site logs are available through
`GET /api/sites/logs/{site}` with source allowlisting, filtering, bounded line
counts, and download support. Worker definitions create fixed-command systemd
units for Laravel queues/Horizon, Node, Celery, or RQ with restart and resource
limits.

## WordPress and Redis (Preview/Beta)

WordPress operations use a closed WP-CLI action set for updates, maintenance
mode, and due cron execution. Redis/Valkey allocations persist logical database,
namespace, memory-policy, and eviction metadata; host ACL and quota enforcement
requires the privileged provider integration before untrusted tenancy.
