# Operations runbook

Documentation version: `main / unreleased`; use the matching release tag when
operating a version older than the current branch.

## Managed-site backup filesystem policy

StePanel-managed site trees must contain only regular files and directories.
Backups reject symbolic links and special files before creating a staging
directory or starting database work. This prevents a backup or restore from
depending on paths outside the site tree, and matches the no-follow policy used
by archive import, site lifecycle, and privileged path validation.

Applications that use paths such as `public/storage -> ../storage` must be
configured with a regular directory inside the managed site or with storage
mounted at that directory. StePanel does not dereference or preserve arbitrary
symlink targets.

## Command reference

The `stepanel` binary (`/opt/stepanel/stepanel` on installed hosts) runs the
panel when started without arguments. Its subcommands:

| Command | Purpose |
|---------|---------|
| `stepanel setup [--output FILE] [--force]` | Guided first-time setup; writes installer settings (see [INSTALLATION.md](INSTALLATION.md#guided-install-recommended)). `init` is an alias |
| `stepanel worker` | Run the durable job worker as a separate process (`STEPANEL_WORKER_MODE=external`, the `stepanel-worker` service) |
| `stepanel version` | Print version, commit, and build date |
| `stepanel hash-password` | Read a password on stdin and print its stored hash |
| `stepanel verify-backup DIR` | Verify a backup's manifest, signature, and archive offline |
| `stepanel verify-audit LOG` | Verify an audit log's HMAC chain offline |
| `stepanel dr-check` | Print a secret-safe control-plane disaster-recovery inventory |
| `stepanel offsite-recovery-proof` | Select a random remote backup and run the full independent recovery proof |
| `stepanel backup-control-plane DEST` | Write a verified backup of the SQLite control plane |
| `stepanel restore-control-plane SOURCE --dry-run` / `--replace` | Validate, then restore, a control-plane backup |
| `stepanel convert-htaccess` | Translate `.htaccess` rules on stdin to Caddy directives |
| `stepanel task-webhook ...` | Internal: delivers scheduled-task completion webhooks from task units under the outbound policy; not for manual use |

The commands that read configuration (`dr-check`, `verify-backup`,
`backup-control-plane`, `restore-control-plane`) need the installed
environment; run them as shown in the sections below.

## Control-plane disaster recovery

See [`STATE.md`](STATE.md) for the authoritative state inventory and recovery
contract used by `dr-check` and the release acceptance process.

Run `stepanel dr-check` during change review and after adding an integration:

```sh
sudo bash -c 'set -a; . /etc/ste-panel.env; set +a; exec /opt/stepanel/stepanel dr-check' > /root/stepanel-dr-manifest.json
```

Required recovery artifacts, including the control-plane database, audit
continuity state, and audit key, must not be group- or world-readable; `dr-check`
fails when those permissions are too broad.

The output is safe to retain as an inventory: it contains paths and statuses,
not passwords, TOTP seeds, encryption keys, deploy-key contents, or rclone
credentials. Preserve the artifacts enumerated in [`STATE.md`](STATE.md):
`/etc/ste-panel.env`, the control-plane database and its SQLite WAL/SHM files,
audit log/state/key, recovery journals, site data, verified backups, and
relevant encryption/signing keys through the host's encrypted DR system. Legacy
JSON files are migration inputs, not authoritative production state. Git deploy
keys may be preserved after access review or deliberately regenerated and
reinstalled at providers. Privileged helpers and systemd units should be
recreated from the verified release package and installer.

Production jobs, customer accounts, site ownership, and revocable sessions are
stored in `STEPANEL_CONTROL_PLANE_DB`; the legacy JSON paths are imported only
when the corresponding database tables are empty. With the control-plane DB
configured, the legacy job JSON is optional and is not a DR gate. Create a consistent backup
and verify it on a disposable host:

```sh
sudo bash -c 'set -a; . /etc/ste-panel.env; set +a; exec /opt/stepanel/stepanel backup-control-plane /root/stepanel-control-plane.db'
sudo bash -c 'set -a; . /etc/ste-panel.env; set +a; exec /opt/stepanel/stepanel restore-control-plane /root/stepanel-control-plane.db --dry-run'
```

The dry-run performs SQLite integrity and schema checks. A live restore remains
an operator-controlled change. With both services stopped, use
`stepanel restore-control-plane SOURCE --replace`; it acquires both service
locks, verifies the source and replacement, atomically publishes the database,
and preserves the prior database as a `.pre-restore-*` file. Run normal startup
reconciliation and retain that prior copy until health and job recovery are
confirmed.
External audit anchoring and a full disposable-host restore drill remain
required release gates.

Administrators can download a redacted incident artifact from
`GET /api/admin/support-bundle`. It is a streamed ZIP containing runtime
metadata, safe configuration posture, readiness results with remediation
details, production checks, Prometheus metrics, and job summaries. Failed jobs
include coarse failure categories without raw error messages. The bundle
deliberately excludes passwords, encryption keys, TOTP material, job payloads
and outputs, database contents, and raw customer logs. Treat site names and
operational metadata as confidential even though credentials are not included.

Site termination first requests a read-only preview. It lists the canonical
site root, managed databases, virtual hosts, proxy routes, and the service and
site-configuration cleanup expected. If the database, route, or proxy inventory
cannot be read, the UI will not offer the typed confirmation. The preview is a
snapshot: the worker rechecks state under the site mutation lease before it
changes host resources. Termination cannot be rolled back automatically; its
final verified backup is the recovery path.

For recovery confidence without changing customer state, use the backup
`Rehearse restore` action (or `POST /api/backups/rehearse`). The durable job
verifies the signed archive, extracts it into a temporary isolated directory,
reserves the manifest's expanded file size against concurrent staging work,
checks the site tree and every recorded database dump, reports counts and
bytes, and removes the temporary directory. The reservation is released only
after the staging tree has been removed.

**WordPress application proof (built in).** For a WordPress backup, the
rehearsal then restores it into a throwaway site through the production
lifecycle path and records the `application` level only if the restored site
serves real requests:

1. a site named `stepanel-proof-<id>` is created with its own Unix account and
   PHP-FPM pool, and the backed-up files are published into it;
2. `stepanel-dbctl restore-wordpress` creates a fresh database and user and
   imports the backed-up database as that user;
3. `wp-config.php` is rewritten to those credentials, `DB_HOST` `localhost`,
   and `WP_HOME`/`WP_SITEURL` `<id>.localhost` (Caddy: `https://`, Apache and
   OpenLiteSpeed: `http://`), so the proof never answers for a customer domain;
4. the web server publishes a route for that private hostname, and loopback
   requests to `/` and `/wp-login.php` must return WordPress output. A
   database connection error, the WordPress installer (missing tables), or a
   redirect to any other host fails the proof.

The proof site, database, and route are always removed afterwards. They are
tracked in an uncommitted site transaction, so if the worker dies mid-proof,
startup recovery removes them. The `stepanel-proof-` site name prefix is
reserved for this purpose. The proof is recorded in the security ledger as
`recovery.proof.site`.

Backups the proof cannot exercise stay at archive level, and the rehearsal
result's `application_proof_skipped` field gives the reason: no
`wp-config.php` or WordPress core, a `DB_NAME` database missing from the
backup, credentials computed at runtime (for example with PHP `getenv`), or a
PostgreSQL host. `STEPANEL_WORDPRESS_RECOVERY_PROOF` controls when the proof
runs: `manual` (default) for operator-requested rehearsals, `always` to also
prove every automatic post-backup rehearsal (each proof briefly adds and
removes a route and reloads the web server), or `off`.

For other applications, configure `STEPANEL_RECOVERY_PROOF_COMMAND`; it takes
precedence over the built-in proof. The command receives the site name and
temporary stage path and owns database restore, configuration generation,
service activation, and an HTTP health probe. Its successful exit is required
before the rehearsal is recorded at the `application` level.

Every rehearsal, passed or failed, is recorded with its timing, and each
site's **Recovery status** (Backups tab, or `GET /api/sites/recovery/{site}`)
summarizes the evidence:

- **Confidence**: `verified` (a rehearsal passed recently), `degraded` (proof
  is stale, the newest backup is old or unsigned, or its offsite copy is
  missing), `failing` (the most recent rehearsal failed, even if an older one
  passed), `unverified` (never rehearsed), or `no_backup`.
- **Measured recovery time**: how long the last passing rehearsal took. An
  archive-level result measures verification, decryption, and extraction;
  an application-level result also includes the restore, service, and HTTP
  proof.
- **Recovery point**: the age of the newest backup, which is how much recent
  change a restore would lose.
- **Encryption key**: `proven` once a rehearsal has decrypted an encrypted
  backup with a configured key.

A successful scheduled backup triggers an automatic rehearsal of that backup
when the site has not been rehearsed within `STEPANEL_REHEARSAL_INTERVAL_HOURS`
(default 24; `0` turns automatic rehearsals off). Sites without a backup
schedule are rehearsed only on demand. Rehearsals need the configured free-space
reserve under the import root and refuse to start without it.

### Independent offsite recovery proof

An upload marker or a successful remote `rclone` read proves storage access,
not disaster recovery. On a separate disposable VM or recovery host with no
access to the primary host's local backup directory, install the same StePanel
release, provide the offsite `RCLONE_CONFIG`, signing/encryption keys, and
`STEPANEL_RECOVERY_PROOF_COMMAND`, then run periodically:

```sh
stepanel offsite-recovery-proof
```

The command lists the remote repository, selects a random valid backup
manifest, downloads only that backup, verifies its signature/checksum, decrypts
and extracts it, validates database dumps, and runs the application proof
command. A passing result is evidence about the remote repository itself; it
does not rely on the primary host's backup index or local backup tree. Schedule
it externally (for example with a systemd timer) and retain its JSON output and
logs as DR evidence.

## Health check

```sh
curl -i http://127.0.0.1:8090/livez
curl -fsS http://127.0.0.1:8090/readyz | jq
curl -fsS http://127.0.0.1:8090/api/health | jq
```

`/livez` reports only that the process can serve HTTP and should be used for
restart decisions. `/readyz` returns `503` when the lightweight durable SQLite
query fails, the capacity-reservation schema is unavailable, persistent job
state is degraded, or the import, backup, or recovery filesystem is unavailable
or below `STEPANEL_MIN_FREE_BYTES`; use it for traffic and post-upgrade checks.

In production, `/readyz` also fails closed when the configured root broker or
required offsite target is unavailable. A fresh install can become ready before
its first backup. The authenticated `/api/health/operational` endpoint returns
`503` with `ok:false` when any operational check is degraded. Its offsite
status includes the age of the latest uploaded backup, latest verified restore,
and oldest unreplicated backup; remote object connectivity alone is not a
recoverability proof.

### Startup recovery barrier

After a restart the panel listens at once, but it recovers interrupted
operations before anything else may change state. Recovery covers database
helper records, site recovery journals, application activations and
WordPress maintenance. Until it succeeds:

- `/readyz` reports `startup_state` as not ready.
- Mutating HTTP requests (`POST`, `PUT`, `PATCH`, `DELETE`) get `503` with
  `Retry-After: 5` and `"retryable": true`. Reads, `/livez`, `/readyz`,
  `/login` and `/logout` stay available.
- Durable workers, in the panel and in `stepanel-worker`, do not claim jobs.
  The panel records each startup epoch in the control-plane database
  (`startup_barrier`), and the external worker reads it, so a job queued
  before the restart waits for recovery. A job already running in the
  external worker when the panel restarts keeps its site lock and finishes.

If recovery fails, the barrier stays closed: mutations return `503` with
`"retryable": false` and workers stay paused. Resolve the failure shown by
`/readyz` and restart the panel. Host reconciliation failures (routes, SSH
access, workers, PHP, Python, tasks, resource profiles, environments) fail
`/readyz` too, but they do not close the barrier. They are retried in the
background, and readiness recovers when they apply.

Dead-letter jobs intentionally keep readiness failed until reviewed. After
ordinary job cleanup does not prune unresolved dead-letter records, even after
the normal completed-job retention period; the record survives a panel restart
until an administrator explicitly requeues it. After remediating the
underlying fault, an administrator can requeue one with
`POST /api/jobs/<job-id>/retry`; the action resets its attempt counter, is
durably compare-and-set against the dead-letter state, and is audit logged.

### Operator notifications

Failed and dead-letter job transitions are recorded in a durable SQLite
outbox. Configure at least one destination in `/etc/ste-panel.env` and restart
the panel:

```sh
STEPANEL_ALERT_WEBHOOK_URL=https://alerts.example.net/stepanel
STEPANEL_ALERT_WEBHOOK_SECRET="replace-with-at-least-32-random-characters"
# or use SMTP (STARTTLS is mandatory; implicit-TLS SMTP is not currently supported)
STEPANEL_ALERT_EMAIL_TO=oncall@example.net
STEPANEL_ALERT_EMAIL_FROM=stepanel@example.net
STEPANEL_ALERT_SMTP_HOST=smtp.example.net
STEPANEL_ALERT_SMTP_PORT=587
STEPANEL_ALERT_SMTP_USER="optional-service-account"
STEPANEL_ALERT_SMTP_PASSWORD="optional-service-password"
```

Webhook receivers get a JSON event with `X-StePanel-Event-ID` and an
`X-StePanel-Signature: sha256=<hex>` HMAC-SHA256 header over the exact body;
use the configured secret and constant-time comparison when validating it.
Events are delivered at least once, so receivers should deduplicate by event
ID. Retries use bounded exponential backoff. SMTP and webhook channels track
delivery independently. Error details and job payloads are deliberately not
sent; delivery status and redacted failure categories appear in the
admin-only `/api/health/operational` endpoint and Health view. Alert delivery
degradation is not allowed to stop the panel or customer sites, but it makes
operational health fail until delivery recovers or the outbox is resolved.

Each alert records which destinations were configured when the job entered a
terminal state. A webhook-only installation does not wait for SMTP delivery,
and vice versa; events created with no destinations are not unexpectedly sent
if an alert channel is configured later. Successfully completed events are
retained for 90 days and deleted in batches of at most 500 during dispatcher
flushes, including when no destinations are currently configured. Events with
any required channel still undelivered are retained regardless of age. During
the schema upgrade, a legacy channel is considered required only when the old
row contains evidence of delivery or an attempted delivery; ambiguous legacy
rows are not replayed to newly configured destinations.

Webhook destinations must pass the outbound public-address policy. Do not put
credentials in the webhook URL; use the HMAC secret. Treat the SMTP password
and webhook secret as production credentials and keep the environment file
root-readable only.

#### Alert-delivery incident procedure

When an external monitor or the Health view reports notification degradation:

1. Check the admin-only `/api/health/operational` result and record the
   pending count, failed-delivery count, last failure time, and current
   StePanel version. Do not copy notification secrets or event payloads into
   an incident ticket.
2. Inspect recent panel messages with
   `journalctl -u stepanel --since '-30 minutes'`; delivery errors are
   intentionally redacted. Check DNS, outbound firewall rules, TLS
   certificate validity, SMTP STARTTLS availability, and destination
   authentication with the destination operator.
3. Correct `/etc/ste-panel.env` if necessary, preserve mode `0600` and
   root ownership, then restart `stepanel` to load the updated environment.
   Do not delete or edit notification outbox rows to make health green.
4. The dispatcher retries undelivered channels with bounded exponential
   backoff. Recheck operational health after the retry window: pending and
   failed counts should return to zero for configured channels. Confirm
   receipt at the destination using the event ID; webhook receivers must
   deduplicate repeated event IDs because delivery is at least once.
5. If delivery remains degraded, retain the outbox, escalate to the
   destination or StePanel operator, and route critical alerts through the
   independent monitoring system. Do not rely on StePanel to notify you that
   StePanel itself or its host is unreachable.

Before production, test the configured destination and its outage/recovery
behavior on the installed host; unit tests or a green connectivity probe alone
do not prove an alert was received and acknowledged by the operator.

Pending resource enforcement shows in each site's resource profile
(`GET /api/sites/resources/{site}`, any `state` other than `applied`);
`POST /api/reconcile/resources` re-applies pending profiles and reports the
sites that still fail. Do not unsuspend affected accounts until helper state is
applied and verified.

## Logs

```sh
journalctl -u stepanel --since today
journalctl -u caddy --since today       # Caddy (default)
journalctl -u apache2 --since today
journalctl -u mysql --since today       # MySQL
journalctl -u mariadb --since today     # MariaDB
journalctl -u postgresql --since today  # PostgreSQL
```

Audit records include distinct `actor`, `target`, sequence, previous-hash, and
HMAC fields. Unsafe authenticated requests are recorded before their handlers;
the control plane returns `503` instead of mutating state when that preflight
record cannot be persisted. Verify the active audit segment with:

```sh
sudo /opt/stepanel/stepanel verify-audit /var/lib/ste-panel/audit.jsonl
```

The HMAC chain continues across log rotation through `audit.jsonl.state`.
Preserve rotated logs, the state file, and `/etc/stepanel-audit.key` together in
independently controlled storage. On the first upgraded write, an unsigned
legacy log is preserved as `audit.jsonl.legacy-TIMESTAMP` before the signed
chain begins. Do not rotate or replace the audit key in place: doing so makes
the existing chain unverifiable, and the installer refuses the replacement
while the key file exists.

On RHEL-family systems, the Apache and database unit names may be `httpd` and
`mariadb`. Use `caddy` for the default webserver on either distribution.

## Capacity and retention

```sh
df -h /var/lib/ste-panel /var/www/sites
du -sh /var/lib/ste-panel/imports /var/lib/ste-panel/mail /var/lib/ste-panel/quarantine
du -sh /var/www/sites/*/.stepanel-previous-* 2>/dev/null
logrotate --debug /etc/logrotate.d/stepanel
```

Keep the configured free-space floor above the largest expected compressed
upload plus extraction and database working space. Audit logs rotate daily,
at 50 MiB, and retain 30 compressed rotations. Interrupted upload files and
expired restore stages are removed by the control-plane retention loop.
Restore admission checks both staging and destination filesystems. Configure
`STEPANEL_MAX_UPLOAD_BYTES`, `STEPANEL_MAX_ARCHIVE_ENTRIES`, and
`STEPANEL_MAX_CONCURRENT_JOBS` to match I/O and memory capacity; one long-running
job per site is enforced independently of the global limit.

The control-plane SQLite database has a fixed 10 GiB logical page-allocation
ceiling. Both the panel and root broker apply the limit to their own SQLite
connections; reaching it makes new database growth fail rather than consume
unbounded space. This is not a physical-byte ceiling: WAL files and transient
checkpoint/journal overhead can make on-disk usage higher. Monitor
`stepanel_control_plane_database_limit_ratio` and
`stepanel_control_plane_database_physical_bytes`; investigate sustained growth
well before the warning threshold. The signed audit log is the long-term audit
evidence source; delivered outbox rows are retained for 90 days and pruned in
bounded batches, while undelivered rows are never pruned. Required notification
deliveries have independent retention and oldest-pending alerts.

Git checkouts require at least 2.5 GiB of currently available memory, share a
single in-process clone admission slot, and run with a 2 GiB virtual-address
space ceiling. The installer provides `prlimit` from util-linux. These bounds
reduce clone pressure but do not impose a host-wide memory quota; use cgroups
and filesystem quotas for hard containment, especially when other worker
processes are active.

Remote archive inspection and ZIP-import spools acquire a progressive SQLite-
backed capacity reservation before writing to the import filesystem. The hold
tracks bytes as they land and remains until the spool is closed, so a concurrent
panel or worker operation sees the remaining commitment. Extraction into a
site's staging tree separately performs a free-space preflight; this check is
not a certified guarantee against unrelated host processes consuming storage.
Prometheus exports the active reservation count, held bytes, oldest hold age,
and held bytes per filesystem device without exposing site paths or workflow
identifiers. Alert on `stepanel_capacity_reservations_up == 0`; if the ledger
cannot be read, inspect SQLite health before retrying storage-consuming work.

## Safe maintenance

`systemctl stop stepanel stepanel-worker` stops accepting HTTP work and durable
job claims, and waits for active restore and certificate jobs for up to two
hours before systemd forces termination. Check both units after maintenance:
`systemctl is-active stepanel stepanel-worker`.
Readiness is intentionally failed while any durable job is in `dead-letter`
state. Review the job output and audit trail, then resolve or replay it through
the operator workflow before treating the host as healthy.
Check `stepanel_restore_jobs_active` before package upgrades or planned reboots.
Archive uploads export `stepanel_upload_rejections_total{reason}`; a rising
`capacity` count means hosts are refusing migrations for lack of disk, and
`stalled` points at client or network problems. `/api/admin/resources/status`
shows the bytes currently reserved by in-progress uploads.
Database monitoring also exports `stepanel_database_diagnostics_up`, connection,
long-transaction, blocking, deadlock, and allocated-byte gauges. Scheduled
backup RPO signals are available as `stepanel_backup_oldest_age_seconds`,
`stepanel_backup_schedules_without_success`, and
`stepanel_backup_consecutive_failures`. Per-site
`stepanel_backup_last_success_age_seconds` and
`stepanel_backup_interval_seconds` drive the `StePanelBackupOverdue` alert,
which names the site whose last successful backup is more than an hour past
its schedule.
Before upgrading, back up `/etc/ste-panel.env`, the database server,
`/var/www/sites`, and the complete state inventory in [`STATE.md`](STATE.md).
Customer credentials and encrypted TOTP material are stored in the
control-plane database when configured; keep the database and
`STEPANEL_ACCOUNT_KEY` only in encrypted control-plane backups and never place
either in support bundles or public backup artifacts.

For an in-place upgrade, build the candidate binary, ensure no restore or backup
job is active, then run `install.sh` without re-supplying secrets. The installer
loads the existing root-owned environment, snapshots StePanel-owned files,
waits for the old service to stop, and health-checks the candidate. If core
configuration or startup fails it restores the previous files and service
state. Package-manager and optional integration changes are not automatically
reverted; retain the host snapshot and inspect the package transaction log.

## Shared-hosting customer accounts

Create a customer only after its managed site already exists. From an
administrator session, provision an explicit plan and explicit assignments:

```json
{
  "username": "acme",
  "password": "a unique password with at least 20 characters",
  "totp_secret": "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP",
  "plan": "starter",
  "sites": ["acme-site"]
}
```

Send that body to `POST /api/accounts` with the normal authenticated session
and CSRF token. Generate a unique, unpadded Base32 TOTP seed of at least 20
random bytes for every customer; deliver the seed and initial password through
separate secure channels. `GET /api/accounts` deliberately never returns TOTP
seeds or password hashes.

After provisioning, sign in as the customer and verify that the assigned site
appears in `/api/sites/overview`, its own backup/job history is visible, and
`/api/services` returns `403`. Keep a record of the assignment and recovery
contact outside the panel. Administrators can suspend/unsuspend panel access
and remove customer login records; suspension revokes active sessions, while
login removal deliberately retains workloads. Use the administrator-only
`POST /api/sites/terminate` workflow to queue confirmation-gated termination
of a managed site; it retains a verified backup before local cleanup. Customer
credential recovery is available through the documented administrator recovery
and customer completion endpoints. Billing, mail, DNS, bandwidth, and external
provider teardown remain outside this local workflow.
Do not represent `starter`, `professional`, or `agency` as complete hosting
resource or support entitlements. They enforce 1, 5, or 25 assigned sites plus
aggregate account/per-site application CPU, memory, process, and PHP-worker
ceilings. Disk and inode ceilings are enforced when the site filesystem is
mounted with the required quota support; otherwise the account remains pending
and fails closed. Bandwidth, database, Redis, and backup-storage limits remain
unavailable as plan entitlements.

`STEPANEL_ACCOUNT_STATE` names the legacy one-time account import source. With
the control-plane database configured, customer accounts, session generations,
and encrypted TOTP material are authoritative in SQLite. Preserve the database
and account key according to [`STATE.md`](STATE.md); revoke sessions or rotate
credentials if their confidentiality may have been lost.

## Verified site backups

Queue a filesystem-only backup, or include databases registered to the site by
the local database helper:

```sh
curl -fsS -X POST -H 'Content-Type: application/json' \
  --data '{"site":"ACCOUNT","include_databases":true}' \
  http://127.0.0.1:8090/api/backups
/opt/stepanel/stepanel verify-backup /var/backups/stepanel/TIMESTAMP-ACCOUNT
sha256sum -c /var/backups/stepanel/TIMESTAMP-ACCOUNT/backup.tar.gz.sha256
```

API calls require the normal authenticated session and CSRF token; the command
above illustrates the request body. A backup is published only after every tar
entry and its whole-archive checksum verify. Copy the complete timestamped
directory to off-host or immutable storage and perform scheduled restore drills.
Live file writes and nontransactional database tables are not quiesced, so use
storage/database snapshots or an independently verified recovery procedure
when a point-in-time consistent backup is required. WordPress maintenance mode
is recorded only as `web-requests-quiesced`; it does not stop cron, WP-CLI,
PHP workers, or independent database writers. Staging retention never deletes published
backups. A scheduled backup's `keep_last` policy protects the newly completed
archive-verified backup, then orders other local candidates by manifest
creation time and prunes the oldest after replacement succeeds. Older retained
archives are not all re-verified during pruning; run strict verification or a
restore rehearsal to establish their recoverability. Safety dumps made before
database deletion remain under `.database-deletions` for explicit DBA review.

Backups expose separate `filesystem_consistency`, `database_consistency`, and
`application_consistency` fields. The normal classification is
`crash-consistent / logical backup`: the archive and any logical database dump
are verified, but no filesystem snapshot or application-wide write freeze is
claimed. A WordPress maintenance-mode run is classified as
`web-requests-quiesced`, not application-quiesced.
`STEPANEL_BACKUP_ENCRYPTION_KEY` encrypts each new archive payload before
publication (see [Rotating the backup encryption key](ENCRYPTION_KEYS.md#rotating-the-backup-encryption-key));
the key is never placed in the manifest or offsite object, only its key id.
If `STEPANEL_BACKUP_SIGNING_KEY` is configured, publication also creates
`manifest.sig`, an HMAC-SHA256 signature kept beside the manifest but verified
with a key held outside the backup root. Keep that key in the host's secret
store and escrow it separately from backup copies. A signature proves the
manifest was produced by the configured panel key; it does not provide
immutability, so use object-lock/immutable retention at the off-site provider.

Verify without restoring or extracting through the CLI or API:

```sh
/opt/stepanel/stepanel verify-backup /var/backups/stepanel/TIMESTAMP-ACCOUNT
curl -fsS -X POST -H 'Content-Type: application/json' \
  --data '{"site":"ACCOUNT","backup":"TIMESTAMP-ACCOUNT"}' \
  http://127.0.0.1:8090/api/backups/verify
```

The administrator restore-to-staging endpoint extracts only a verified backup's
files into a new isolated, no-index route. It can optionally provision a new
managed destination database and import one selected verified dump. The staging
creation endpoint can also logically clone one managed source database into a
new destination after ownership checks. Database schema rollback, off-site
browsing, and promotion semantics remain unavailable. Administrator-only
files-only in-place and database-only restores are also available through their
explicit APIs.

Git rollback releases are automatically retained per site. The count limit is
controlled by `STEPANEL_GIT_RELEASE_RETENTION` (default 3), while
`STEPANEL_GIT_RELEASE_MAX_AGE_HOURS` (default 168 hours) and
`STEPANEL_GIT_RELEASE_MAX_BYTES` (default 5 GiB) bound stale storage. The
current release and newest previous release are never removed. Cleanup is
serialized with deployment and rollback, and runs at startup and periodically
so abandoned releases are collected without waiting for another deployment.
Prometheus exposes `stepanel_git_release_bytes` and
`stepanel_git_releases_total`. Use the authenticated site workspace and
`/api/audit/events` to correlate releases with process, proxy, and database
changes. See [`GIT_DEPLOYMENTS.md`](GIT_DEPLOYMENTS.md).

Each checkout and build artifact is also limited by
`STEPANEL_GIT_RELEASE_MAX_TREE_BYTES` (default 5 GiB). The configured amount is
reserved during checkout and the unpacked tree is checked before activation.
Before cloning, the root broker also assigns the stage a filesystem project
quota of three times that limit, covering the packed object store, checkout,
and metadata together; `RLIMIT_FSIZE` separately caps each file. Staging is
under a panel-owned, non-tenant-writable parent, and ACLs grant the site and
configured web-server group the access needed after activation. Git deployment
fails closed unless project quotas are active on a supported ext4 or XFS
filesystem. The temporary quota must be removed before activation. This
implementation still requires installed-host aggregate-write and ENOSPC
evidence before it can be considered production-certified.

Use the authenticated database endpoints during an incident:

```text
GET    /api/database/diagnostics
GET    /api/database/sessions
GET    /api/database/settings
DELETE /api/database/sessions/SESSION_ID
```

Session output deliberately excludes SQL text and termination requires an exact
confirmation phrase. Use an engine-native DBA client when query text or plans
are necessary, and apply the normal sensitive-data handling policy.

Job records, sessions, customer accounts, API tokens, resource profiles, and
other control-plane state are persisted in `STEPANEL_CONTROL_PLANE_DB`. Legacy
`jobs.json`, `sessions.json`, and `accounts.json` paths are imported only for
migration and are not the live source of truth. Back up the complete inventory
in [`STATE.md`](STATE.md) and never publish secret-bearing artifacts. Site overwrites
move the previous document root into a journaled transaction under
`/var/www/sites/.stepanel-recovery`. Privileged root-broker journals are stored
separately under `/var/lib/stepanel/recovery` and must remain root-owned. On startup, StePanel marks interrupted jobs
failed, removes databases recorded by uncommitted restore transactions, and
then rolls back their site files. If any recovery step fails, readiness remains
failed until the operator resolves the journal and restarts the service;
mutating requests must not be accepted while recovery is unresolved. Committed and
rolled-back transactions remain available for the configured staging-retention
period; preserve them before that deadline when investigating an incident.

Local database operations are registered under
`/var/lib/stepanel-privileged/db-managed`. Entries prefixed with `pending-` are reconciled
by the database helper before transaction-journal recovery and HTTP startup. Do not edit this root-only
registry manually; preserve the root-only `/var/lib/stepanel-privileged` tree
with system backups.

Host restores create site identities named from the site plus a stable hash.
Inspect their ownership, ACL, and PHP-FPM pools with:

```sh
getent passwd 'sp-*'
getfacl /var/www/sites/ACCOUNT
find /etc/php /etc/php-fpm.d -name 'stepanel-ACCOUNT.conf' -print 2>/dev/null
systemctl status 'stepanel-app-ACCOUNT.service'
```

Activate a restored PHP document root only after verification by calling
`POST /api/sites/deploy` with its site and domain. Managed vhosts live under
`/etc/caddy/stepanel.d` by default, or `/etc/apache2/stepanel-sites` and
`/etc/httpd/conf.d/stepanel-sites` on Apache. Caddy and Apache configuration
changes are serialized, syntax-tested, and rolled back on reload failure.
OpenLiteSpeed proxy changes use the same validated, rollback-aware pattern in
its helper. A domain already present in an existing vhost or Node proxy is
refused. Use the dashboard or `stepanel convert-htaccess` to review an Apache
`.htaccess` conversion before applying it to a Caddy PHP route.

Apache certificate issuance uses the same Apache lock; Caddy manages HTTPS
certificates automatically. Route deletion refuses to
proceed while another vhost (including a TLS companion) still serves the domain;
remove that external or certificate-managed vhost first.

Upgrades from the legacy writable proxy directory disable its Apache include.
Re-deploy each managed proxy through the panel so the validated helper creates
the corresponding root-owned snippet.

## Recovery

If an import fails, preserve the timestamped staging directory, inspect the service logs, and restore from the pre-import snapshot. Do not repeatedly retry against a live destination without identifying the failure mode.

### Resolving a retained recovery intervention

If startup finds a site journal with `recovery_required`, it keeps readiness
degraded and will not prune or automatically replay that journal. Inspect the
journal and host state first; reconcile the site tree, database, service
identity, PHP-FPM pool, and webserver route as applicable. The command below
only acknowledges that operator reconciliation—it does not repair host state.
Use it only after verifying the affected resources, and keep the journal for
incident evidence.

Stop both processes, then run the audited resolution command as the configured
StePanel service account (or root):

```sh
sudo systemctl stop stepanel-worker.service stepanel.service
sudo bash -c 'set -a; . /etc/ste-panel.env; set +a; exec /opt/stepanel/stepanel resolve-recovery TRANSACTION_ID --confirm-host-reconciled'
sudo systemctl start stepanel.service stepanel-worker.service
```

Replace `TRANSACTION_ID` with the ID shown in the retained recovery journal.
The command refuses to run while either process lock is held, requires the
explicit confirmation flag, and records `recovery.intervention-resolution-attempted`
in the required audit log before clearing the marker. Startup reconciliation
then processes any remaining nonterminal transaction state. If it fails,
readiness remains degraded; do not remove the journal to silence the alert.
