# Operations runbook

Documentation version: `main / unreleased`; use the matching release tag when
operating a version older than the current branch.

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
metadata, safe configuration posture, readiness results, production checks,
Prometheus metrics, and job summaries. The bundle deliberately excludes
passwords, encryption keys, TOTP material, job payloads and outputs, database
contents, and raw customer logs. Treat site names and operational metadata as
confidential even though credentials are not included.

For recovery confidence without changing customer state, use the backup
`Rehearse restore` action (or `POST /api/backups/rehearse`). The durable job
verifies the signed archive, extracts it into a temporary isolated directory,
checks the site tree and every recorded database dump, reports counts and
bytes, and removes the temporary directory. It intentionally does not activate
a site or run a database engine; use restore-to-staging for that stronger
end-to-end rehearsal.

Every rehearsal, passed or failed, is recorded with its timing, and each
site's **Recovery status** (Backups tab, or `GET /api/sites/recovery/{site}`)
summarizes the evidence:

- **Confidence**: `verified` (a rehearsal passed recently), `degraded` (proof
  is stale, the newest backup is old or unsigned, or its offsite copy is
  missing), `failing` (the most recent rehearsal failed, even if an older one
  passed), `unverified` (never rehearsed), or `no_backup`.
- **Measured recovery time**: how long the last passing rehearsal took to
  verify, decrypt, and extract the backup. This is a recovery time for the
  archive only; database import and application start are not yet rehearsed,
  and the status says so.
- **Recovery point**: the age of the newest backup, which is how much recent
  change a restore would lose.
- **Encryption key**: `proven` once a rehearsal has decrypted an encrypted
  backup with the current key.

A successful scheduled backup triggers an automatic rehearsal of that backup
when the site has not been rehearsed within `STEPANEL_REHEARSAL_INTERVAL_HOURS`
(default 24; `0` turns automatic rehearsals off). Sites without a backup
schedule are rehearsed only on demand. Rehearsals need the configured free-space
reserve under the import root and refuse to start without it.

## Health check

```sh
curl -i http://127.0.0.1:8090/livez
curl -fsS http://127.0.0.1:8090/readyz | jq
curl -fsS http://127.0.0.1:8090/api/health | jq
```

`/livez` reports only that the process can serve HTTP and should be used for
restart decisions. `/readyz` returns `503` when the durable SQLite control
plane fails its integrity check, persistent job state has failed
or the import, backup, or recovery filesystem is unavailable or below
`STEPANEL_MIN_FREE_BYTES`; use it for traffic and post-upgrade checks.

Dead-letter jobs intentionally keep readiness failed until reviewed. After
remediating the underlying fault, an administrator can requeue one with
`POST /api/jobs/<job-id>/retry`; the action resets its attempt counter, is
durably compare-and-set against the dead-letter state, and is audit logged.
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

## Safe maintenance

`systemctl stop stepanel stepanel-worker` stops accepting HTTP work and durable
job claims, and waits for active restore and certificate jobs for up to two
hours before systemd forces termination. Check both units after maintenance:
`systemctl is-active stepanel stepanel-worker`.
Readiness is intentionally failed while any durable job is in `dead-letter`
state. Review the job output and audit trail, then resolve or replay it through
the operator workflow before treating the host as healthy.
Check `stepanel_restore_jobs_active` before package upgrades or planned reboots.
Database monitoring also exports `stepanel_database_diagnostics_up`, connection,
long-transaction, blocking, deadlock, and allocated-byte gauges. Scheduled
backup RPO signals are available as `stepanel_backup_oldest_age_seconds`,
`stepanel_backup_schedules_without_success`, and
`stepanel_backup_consecutive_failures`.
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
application maintenance mode or storage/database snapshots when a point-in-time
consistent backup is required. Staging retention never deletes published
backups. A scheduled backup's `keep_last` policy prunes its oldest verified
local copies only after a replacement succeeds; safety dumps made before
database deletion remain under `.database-deletions` for explicit DBA review.

Backups are classified explicitly as `crash-consistent / logical backup`:
the archive and any logical database dump are verified, but the application is
not quiesced and no filesystem snapshot is taken. If
`STEPANEL_BACKUP_ENCRYPTION_KEY` encrypts each new archive payload with
streaming AES-256-GCM before publication; the key is never placed in the
manifest or offsite object. `STEPANEL_BACKUP_SIGNING_KEY` is configured,
publication also creates
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
`/var/www/sites/.stepanel-recovery`. On startup, StePanel marks interrupted jobs
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
