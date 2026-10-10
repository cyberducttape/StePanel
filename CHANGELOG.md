# Changelog

All notable changes to StePanel are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- The quota-enabled Ubuntu installation smoke test now polls cloud-init
  non-blockingly, enforces a bounded guest-readiness deadline, and reports
  cloud-init diagnostics immediately instead of repeatedly waiting on SSH
  connections until the workflow timeout.

- The dashboard now shows customer site creation only to administrators and
  active tenant owners; member and suspended-account sessions see the same
  tenant-filtered inventory without a misleading mutation control.

- The customer dashboard now exposes the existing plan-limited site creation
  workflow to authorized tenant owners, keeping user-level hosting behavior
  consistent between the API and the web interface.

- Customer site-provisioning reservations are now idempotent: retrying a timed-out
  `POST /api/sites` returns the original durable job instead of a false ownership
  conflict, with regression coverage for assignment and queue replay.

- Recorded the 2026-10-10 Rocky Linux 9 KVM certification: the production-shaped
  install and full 107-pass recovery matrix completed successfully once. The
  separate power-loss, repeated-run, and non-backup ENOSPC gates remain open.

- Recorded hosted installation smoke run `38076651823` passing on AlmaLinux 9
  and Rocky Linux 9; quota-enabled installation validation remains queued.

- **Application recovery manifest deletion now uses a no-follow directory
  descriptor**: persisted site identities are validated as single components,
  the canonical app-root path is rechecked, and recovery cannot unlink through
  a substituted pathname outside the configured app root.
- Durable restore retries now reconcile prior site journals under the site
  mutation lease before creating a new generation.

- Hardened the off-site rclone runner's fixed executable construction and made
  account-suspension recovery smoke failures report the response that caused
  the failed gate.
- Hardened native and lab installation paths so root-broker recovery journals use a root-owned `/var/lib/stepanel/recovery` directory, separate from the panel-owned workspace.
- Hardened durable capacity reservation IDs against cross-process and fallback-source collisions that could reject valid uploads.
- Fixed multi-filesystem capacity reservations by migrating the ledger to a composite reservation/device key.
- Centralized off-site rclone execution and revalidated application journal manifest paths before recovery writes or deletion.
- Routed application rollback manifest resolution through the containment-checked path helper.
- Separated the root broker's privileged journal root from the panel-owned site snapshot root, preserving safe restore transactions after journal-boundary hardening.
- **User-level site hosting is now available**: active tenant owners can
  create durable blank PHP sites within their plan, with atomic site ownership
  assignment, post-creation plan-resource reconciliation, and tenant-filtered
  `/api/sites` inventory; terminally failed customer creation jobs release
  their site reservation; administrator site provisioning remains supported.
- **Termination dry-runs now query authoritative active jobs by site**: they no
  longer rely on a 500-job recent window or claim that a future backup is an
  already verified recovery artifact; unavailable durable state blocks readiness.
- **Privileged root-broker journals now use the installer-created root-owned
  recovery directory**: the service no longer shares the panel-owned site
  recovery workspace, and journal loading rejects symlinked roots or records.
- **Offsite uploads now publish transactionally**: directory contents are
  copied to a unique temporary remote prefix, checked before promotion, and
  marked complete only after the final prefix is published. Restore requires
  the completion marker and strict validation remains the final integrity gate.
- **Offsite backup uploads now match the published backup layout**: the
  uploader transfers the backup directory contents with `rclone copy` to the
  site/backup destination expected by restore, validates the directory root,
  rejects symlinks, and applies a bounded aggregate size check.
- **Offsite restore command arguments are now constrained at the trust
  boundary**: remote identities and object names reject traversal, control
  characters, and option-like values before filesystem paths or rclone
  commands are constructed.
- **Backup staging cleanup now respects live stage ownership**: backup
  creation holds a filesystem lock for the stage lifetime, and cleanup skips
  locked stages before applying the age safeguard.
- **Backup signature verification is now memory-bounded**: malformed
  `manifest.sig` files are limited to 4 KiB before decoding.
- **Backup retention prefers durable manifest creation timestamps**: renamed
  or differently formatted backup directories no longer determine chronology
  when valid timestamps are available, with name ordering retained as a
  deterministic fallback.
- **The site manager interface now exposes only implemented lifecycle
  primitives**: durable import/restore/configuration jobs and unsupported
  suspension workflows are no longer presented as manager capabilities.
- **Offsite timeout calculation now saturates safely**: very large backup
  sizes cannot overflow `time.Duration` and accidentally receive the minimum
  deadline.
- **Offsite transfers now use size-aware bounded deadlines**: upload and
  restore transfers allow slow large objects more time, enforce connect and
  no-progress stall limits, retry transient failures, and request periodic
  rclone transfer statistics.
- **Importer PHP configuration detection now uses a bounded token-aware scan**:
  comments and quoted text are ignored, multiline definitions are supported,
  ambiguous duplicate definitions fail closed, and configuration files are
  limited to 1 MiB before parsing.
- **Importer database credentials are now emitted as escaped PHP literals**:
  quotes, backslashes, interpolation markers, control characters, and invalid
  UTF-8 cannot produce malformed or executable configuration source.
- **Importer configuration publication is crash-durable**: replacement files
  preserve mode and ownership, sync their contents before rename, and sync the
  parent directory after publication.
- **Importer update reporting no longer treats skipped function-backed values
  as changed**: `env()` and `getenv()` definitions are explicitly reported as
  skipped, while malformed definitions fail closed.
- **Cpmove inspection responses no longer return archive-derived names or paths**:
  the browser receives only the import token, bounded size/feature metadata, and
  item counts, closing the reflected XSS path reported by CodeQL.
- **Cpmove and WPress upload responses bypass the generic API passthrough
  writer**: upload admission responses remain fixed and non-reflective,
  preventing request-body data from reaching the generic HTTP response sink.
- **API response passthrough no longer uses a generic copy sink**: successful
  response bytes are written directly to the selected response writer while
  plain-text errors remain captured and normalized.
- **Recovery rollback uses bounded filesystem roots**: site existence checks
  now use Go's `os.Root`, and rollback derives journal backup paths from the
  trusted transaction directory instead of using persisted path fields.
- **Documented the API response-writer XSS boundary**: multipart uploads bypass
  the generic writer, JSON responses use HTML-escaping encoding, and untyped
  API bodies receive an octet-stream content type.
- **Application deployments now use a durable activation journal**: runtime
  application precedes manifest publication, failed applies restore the prior
  service generation, and startup replays uncommitted activation journals.
- Added regression coverage for startup replay restoring both the previous
  application runtime and manifest generation.
- **Application lifecycle operations no longer use a process-wide mutex**:
  site-scoped mutation locks remain the serialization boundary, allowing
  unrelated applications to deploy or change state concurrently.
- **Production recovery gates now require application-level proof for
  WordPress approval**: archive-only rehearsals remain explicitly lower-level
  evidence and cannot be used as production adoption evidence by themselves.
- **Unsupported workflow boundaries are now advertised explicitly**: broker
  app rollback/Git key verification, site resume, and automatic legacy-token
  email notifications report their actual unsupported or manual status through
  `/api/capabilities` rather than appearing implicitly available.

### Fixed

- Close CodeQL's recovery path-injection and multipart reflected-XSS paths by
  requiring canonical absolute recovery paths and replacing parser errors with
  fixed client-safe upload errors before response handling; capacity failures
  no longer reflect filesystem paths in upload responses, and the parser-error
  branch no longer passes request state into response handling. Archive
  inspection/import failures also use fixed client-safe response messages, and
  uploaded archive display names and archive-derived list values are
  HTML-escaped before API serialization.
- Serve Caddy sites through persistent per-site `nosymfollow` bind views so
  symlinks created after route publication cannot escape the document root.
- Exercise the live Caddy view mount and post-publication symlink request in
  the installation smoke gate.
- Fence startup recovery's brokered site rollback with the recovered site's
  durable mutation lease so readiness can return after an interrupted restore.
- Keep the application lifecycle smoke aligned with persisted collision-safe
  site identities during application deletion and cleanup.
- Accept the app helper's safe missing-identity refusal after lifecycle cleanup
  instead of requiring only its unit-missing wording.
- Generate collision-resistant capacity reservation IDs for concurrent and
  restored-VM upload admission paths.
- Add effective site and recovery-root ownership, traversal, and ACL diagnostics when the backup restore smoke gate fails.
- Route live-site recovery snapshots through the root broker so worker service confinement cannot block atomic restore journaling.
- Keep isolated unit workflow fixtures self-contained while production and lab restore paths use the brokered snapshot operation.
- Make the disposable lab root broker consume the same `/var/www/sites/.stepanel-recovery` journal root as the installed panel and worker.
- Route interrupted restore rollback through the root broker as well as snapshot creation, so startup recovery cannot fail on worker filesystem confinement.
- Keep startup-recovery unit fixtures broker-independent while production and lab recovery use the typed rollback operation.
- Route privileged site sealing through the root broker's persisted site identity so production app services receive the ownership and ACL setup required to start.
- Make the restore recovery smoke report durable job failures and worker diagnostics before declaring an injected kill boundary missing.
- Preserve explicit panel `rwx` access on each sealed site root so atomic restore snapshots can rename the public tree after ownership hardening.

### Changed

- **Preserve durable worker panic stacks**: unexpected worker panics now retain
  their Go stack trace in the failed job error, making production recovery
  failures diagnosable instead of reducing them to an opaque panic message.
- **Fix durable database-backup capacity growth**: per-dump reservations now
  grow through the shared SQLite ledger instead of writing to an intentionally
  empty process-local map, preventing worker panics during database backups.
- **Fix durable streamed-upload capacity accounting**: progressive growth and
  consumption now remain coordinated through the shared SQLite ledger instead
  of relying on an empty process-local hold map.
- **Fix app lifecycle smoke identity handling**: the durable site-creation test
  no longer re-seals with the retired derived account name after creating a
  persisted collision-safe site identity.
- **Database backup capacity is accounted for per dump**: each managed
  database reserves its inventory-based predicted dump size before execution,
  reconciles the reservation to the measured dump, and falls back to the
  conservative allowance independently for databases without a size estimate.
- **Development listeners default to loopback**: an unauthenticated process
  now binds `127.0.0.1:8080`, and startup refuses any non-loopback listener
  until administrator authentication is configured.
- **Job status reads expose control-plane degradation**: the HTTP job-status
  endpoint returns `503` when an authoritative durable read fails instead of
  presenting a stale cached record; internal compatibility callers retain
  their existing in-memory recovery behavior.
- **Root-broker journals use the configured recovery root**: the installed
  broker now receives the installer’s recovery root explicitly while keeping
  panel secrets out of the root-broker service environment; standalone broker
  invocations retain the environment/legacy fallback.
- **HSTS scope is configurable**: `STEPANEL_HSTS_INCLUDE_SUBDOMAINS` and
  `STEPANEL_HSTS_PRELOAD` default to `1`, while explicit `0` values allow
  operators to match their domain and DNS boundaries.

- **Privileged work now fails closed on fencing-database outages**: the root
  broker allows only a bounded grace period for transient lease-verification
  errors, then cancels the mutation when ownership cannot be established.
- **Capacity reservations are shared across processes**: panel and worker
  processes now coordinate filesystem reservations through the control-plane
  SQLite database, keyed by filesystem identity, instead of maintaining
  independent in-memory ledgers.
- **Off-site backup retries reuse verified local archives**: a completed local
  backup is persisted in the durable job before remote transfer, and retries
  strictly revalidate that archive rather than rebuilding it.

- **Application lifecycle smoke uses the production site workflow**: the
  disposable Node-app test now creates its site through `/api/sites`, waits
  for durable creation, and exercises application deployment with the
  persisted Unix identity required by the root broker instead of constructing
  an unsupported filesystem-only site.
- **Builds, deployments, and staging restores are durable jobs**: Git
  deploys, signed Git webhooks, release pipelines, runner builds, staging
  creation, restore-to-staging (local and offsite), Composer install, Node
  tooling, and Python deploys no longer hold an HTTP request open for up to
  60 minutes. The request is authenticated, CSRF-checked, and authorized for
  the site, then answers `202` with a `job_id`; a worker runs the unchanged
  handler as the original requester (scopes and site access are re-checked)
  with a 60-minute deadline, and the job's `output` is the response the
  endpoint used to return. Operations on one site run one at a time. A
  dropped browser no longer cancels a build. **API clients must follow the
  job** (`status_url`) instead of reading the result from the response.
- **Interrupted operations are repaired, not repeated**: if a worker dies
  mid-operation, the next claim recovers that site's interrupted release
  activation and site transactions under its lease (the same recovery panel
  startup runs, limited to the site) and reports the operation as
  interrupted instead of re-running it.
- The 60-minute HTTP class (`LongOperationPaths`) is gone; Python
  start/stop/restart keep a 3-minute synchronous class.

### Fixed

- **Capacity-ledger initialization no longer copies a mutex**: application
  startup now transfers only the shared database handle, keeping `go vet`
  clean while preserving cross-process reservations.

- **Site termination no longer dead-letters during recovery**: the
  application helper now validates the persisted site Unix identity before
  deleting a site's units, while dispatching with the normalized argument
  shape used by the helper and retaining the explicitly lab-only legacy
  invocation.
- **Signed Git webhook deployments were cut off after 30 seconds**: the
  webhook route ran the checkout inside the request on the ordinary API
  deadline. Deliveries are now queued, and the worker re-reads the site's
  webhook policy before deploying, so a webhook removed in the meantime
  stops the deployment.
- **The Composer button in the site workspace never ran**: it posted to
  `/api/composer/<site>` instead of `/api/composer/<site>/install` and sent
  `optimize` instead of `optimize_autoloader`.

- **Chunked uploads are no longer refused for the upload ceiling**: an
  upload without `Content-Length` was admitted at the full
  `STEPANEL_MAX_UPLOAD_BYTES`, so a small upload failed on a host with less
  free space than the ceiling, and a large one blocked other uploads. It is
  now reserved 256 MiB ahead of the bytes written and stopped with `507`
  (partial object removed) only when the next step no longer fits.
- **cPanel database restores check database disk space**: twice the size of
  the archive's SQL dumps is required on the local database data directory
  (`STEPANEL_DB_DATA_DIR`, default `/var/lib/mysql`) before restoring.

- **Python apps and Node builds work again on new installs**: a `nosymfollow`
  bind mount on `/var/www` blocked the symlinks that Python virtualenvs and
  `node_modules` rely on. The installer no longer creates it and removes it on
  upgrade. Python virtualenvs moved out of the document root to
  `/var/www/sites/<site>/.venv`, and the previous `public/.venv` is removed
  after a successful redeploy.
- **FTPS no longer breaks SSH/SFTP key access**: FTPS and SSH/SFTP keys share
  the site account's password field. Revoking FTPS no longer locks out
  installed keys, enabling keys no longer replaces the FTPS password, and an
  `sshd` validation or reload failure now rolls the FTPS change back. FTPS
  changes take the site mutation lock and are recorded as Class A
  `site.ftps.enabled` / `site.ftps.disabled` audit events. Site deletion
  removes the account from the FTPS allowlist.

### Features

- **FTPS management and bounded recovery downloads**: administrators can now
  enable or revoke per-site FTPS through the authenticated `/api/ftp` mutation
  workflow. Offsite restore downloads enforce explicit manifest and object
  size limits before verification and extraction.

- **Independent offsite recovery proof**: added
  `stepanel offsite-recovery-proof`, which lists the configured remote
  repository, selects a random backup manifest, downloads only that backup,
  verifies its checksum and signature, decrypts and extracts it, validates
  database dumps, and runs the configured application recovery proof. The
  downloader now follows the manifest-declared archive, including encrypted
  `backup.tar.gz.enc` artifacts, so this workflow can run from a disposable
  recovery host without access to the primary host's local backup tree.
- **Blank PHP site creation**: administrators can create a site from the
  Sites page or `POST /api/sites` (`template: php`). A durable `site.create`
  job writes a placeholder page into SiteManager staging, creates the site's
  system account and PHP-FPM pool through the root broker, activates the
  tree, and seals ownership, recording a Class A `site.created` audit event.
  A failure, or an unclean shutdown mid-creation, removes the account, pool
  and tree it created. `site.lifecycle.create` now reports the real
  capability instead of "unsupported". Routes and customer assignment keep
  their existing workflows.

### Production Readiness

- **Site Unix identities are persisted and collision-safe**: production site
  provisioning now allocates an immutable account in the control-plane
  database, migrates existing site roots, fails closed on legacy collisions,
  and passes the stored identity from the root broker to the site, app, and
  container-runner helpers. Account names are never derived from site names in
  the production privileged path.
- **Lab helper compatibility is explicitly non-production**: disposable
  install and runner drills opt into the legacy direct-helper shape with
  `STEPANEL_UNSAFE_LAB=1`; installed production broker operations always pass
  the persisted identity explicitly.
- **Existing control-plane databases receive the identity schema on upgrade**:
  migration 13 creates `site_identities` before startup backfills existing
  site roots and checks for legacy collisions.

- **Release-pipeline diagnostics and timeout certification**: durable job
  submissions are tested against the standard mutation timeout, and failed
  sandboxed builds now report their exit status, filesystem headroom, quota
  state, and staged-artifact ownership for disposable-host diagnosis.
- **Rootless runner storage selection**: transient Podman units now receive an
  explicit `CONTAINERS_STORAGE_CONF` path for the root-owned generated
  configuration, and the configuration's ownership and mode are validated
  before the container runtime starts.

- **Rootless build runner works under SELinux, and through the broker**: on
  an SELinux-enforcing Rocky Linux 9 host the runner could not pull images,
  and release pipeline builds (which run through the root broker) never ran
  on any host because `systemd-run --pipe` fails inside a system service.
  Five faults are fixed (see the October 9 lab report): unit options that set
  `no_new_privs`, podman storage in the web tree (now
  `/var/lib/containers/stepanel-runner`), a per-build runtime directory podman
  could not reuse, `--pipe` output handling, and a release tree the site
  account could not read. Builds no longer mount the live document root with
  a relabelling `:Z` (they use a private copy), artifacts are restored to the
  site's SELinux label, and a failed build keeps the last good artifact.
  `release-pipeline-smoke.sh` exercises checkout, build, and activation.

- **Slow request bodies are bounded**: route classes only set a context
  deadline, which does not interrupt a blocked body read, while the server
  read timeout is sized for 60-minute uploads. A client trickling an
  ordinary JSON body could hold a handler for up to an hour. Every non-upload
  request now gets a network read deadline matching its class.

- **Long synchronous operations are no longer cut off at 30 seconds**: the
  timeout middleware gave every unclassified API route a 30-second deadline,
  so release pipelines (20-minute budget), Git deploys, Composer/Node/Python
  tooling, runner builds, staging creation, and restore-to-staging were
  cancelled mid-work, and the 5-minute server write timeout ended any longer
  response. These routes (`LongOperationPaths`) now get a 60-minute request
  and write deadline, the browser waits 61 minutes for them, the shipped
  Apache proxy configuration allows 3700 seconds, and a test keeps the
  server and client route lists identical. They should become durable jobs.

- **Startup and rollback broker calls carry a lease**: the root broker
  rejects unfenced mutations, but the startup database reconcile, startup
  recovery of interrupted database transactions, and the rollback cleanup of
  failed site creation, cpmove imports, and WordPress restores called it
  without one. On a production host the panel started degraded ("fencing
  token required") and failed cleanups were refused. Each now holds the
  relevant lease. Read-only database actions (diagnostics, sessions,
  settings) no longer require a token, and terminating a database session
  takes one.
- Python applications need the distribution's venv support, which Debian
  and Ubuntu package separately; the installation guide says so and
  `stepanel-appctl` names the missing package.

- **First Rocky Linux 9 KVM certification run** (SELinux enforcing, full
  recovery matrix, abrupt guest loss idle and mid-backup, real ENOSPC); see
  `docs/lab-results/2026-10-09-rocky9-kvm-certification.md` and
  `deploy/lab/local-kvm-certification.sh`. It found and fixed: scheduled
  tasks and Python apps that SELinux prevented from starting; Git deploys
  broken after any site seal by an ACL mask reset; and full-matrix drill
  errors. The rootless build runner remains unsupported under SELinux.
- **Upgrades are no longer refused on Debian/Ubuntu**: the host-takeover
  guard refused to stop the Apache that PHP packages pull in, even when
  upgrading an existing installation with the same web server. That consent
  was given at first install, so upgrades proceed and say so.

- **HTTP error-rate alert fires at low traffic**: `StePanelHTTP5xxRateHigh`
  clamped its denominator to one request per second, so on a quiet panel
  10 requests with 2 failures (20%) evaluated to about 0.67% and never
  fired. It now divides by the real rate and requires 20 requests in five
  minutes; the new `StePanelHTTP5xxLowTraffic` alert covers lower volumes
  with an absolute count. `observability/alerts_test.yml` holds promtool
  rule tests, which CI now runs.
- **Browser request layer**: a malformed CSRF cookie no longer throws out of
  every mutation, and mutations time out after two minutes by default
  (uploads stay untimed and are bounded by the server).

- **Privileged operations stop when their lease is lost**: the root broker
  checked a request's fencing token only before starting it, then ran the
  helper detached from the caller. A crashed or partitioned owner's helper
  could keep mutating after another worker acquired the site. The broker now
  re-checks the token every 5 seconds and kills the helper once the lease has
  under 20 seconds left, before any other owner can acquire it.
- **WordPress is not left in maintenance mode by a failed backup**: each
  maintenance window a backup opens is recorded durably first. Deactivation
  uses its own bounded context, so a cancelled backup still ends it; a failed
  deactivation is reported in the backup error; and panel startup plus the
  15-minute maintenance tick end any window whose backup was killed
  (`wordpress.maintenance.recovered`).
- **Task webhook URLs are kept from tenant code**: the URL, which may carry
  a credential, was loaded into the tenant task's environment and passed on
  a command line visible in `ps`. A privileged `task-notify` step now reads
  it from the root-only file and hands it to the sender on stdin.
  `install.sh` rewrites existing task units.

- **Root broker no longer inherits the panel's secrets**: the broker unit
  loaded `/etc/ste-panel.env` as its environment, and every helper inherits
  it. `stepanel-appctl node-tool` runs the tenant's own build scripts as the
  site user, so tenant code could read the session secret, account, audit,
  and backup keys, the admin TOTP secret, and the database admin URL. The
  unit now passes only `-control-plane-db`, and every site-user command in
  `stepanel-appctl` (npm/yarn/pnpm, Composer, pip) runs with a cleared
  environment.
- `POST /api/ftp` with an invalid JSON body now returns 400 instead of an
  empty 200.

- **Provider pagination and startup ordering**: Linode inventory and duplicate
  detection now follow bounded provider pages through one API client, and the
  server binds its listener before startup work is released.

- **Tenant account collision and broker-state hardening**: existing site
  accounts are now accepted only when their home directory matches the
  requested site, and deletion applies the same check, preventing a
  deterministic-name collision from crossing tenant boundaries. The native
  root broker now reads the installer-authoritative
  `STEPANEL_CONTROL_PLANE_DB` and refuses to start without an absolute path.
  Linode create/add jobs validate only IDs required by their action, while
  FTPS accepts valid passwords and installs an explicit per-account SSH
  password-authentication denial policy.

- **Idempotent imported-site cleanup**: the tenant account ownership guard
  still rejects an existing account mapped to another site, while allowing a
  retry to remove an imported site whose account is already absent.

- **Installation-smoke coverage**: disposable systemd hosts now include the
  OpenSSH server and client required by the real SFTP provisioning smoke on
  Debian/Ubuntu and RHEL-family images. CI also measures the session package's
  security floor from its complete package-specific test run instead of relying
  only on the repository-wide coverage profile; the generic target checker can
  now explicitly exclude such independently measured targets.
  The SFTP smoke resolves `sshd` to an absolute path for RHEL-family launchers
  that reject relative daemon invocation and reports the daemon log when a
  restricted session cannot connect.
  SSH/SFTP access provisioning also unlocks newly-created system accounts for
  public-key authentication using a random unknown password hash and restores
  their prior shadow state on rollback.
  Revoking both SSH and SFTP access locks the account again.
  The root-owned authorized-key directory is traversable by sshd while its
  individual key files remain non-writable by customer accounts.

- **PHP runtime profile round trips**: validated runtime values are now written
  without shell-escape sequences, so `error_reporting` survives a subsequent
  site-management operation and PHP configuration regeneration.

- **Cross-distribution PHP socket selection**: the Caddy site helper now reads
  the installer-owned PHP socket root instead of hardcoding Debian's `/run/php`,
  keeping versioned PHP-FPM routes working on RHEL-family hosts, and recognizes
  RHEL's active unversioned pool when a site has a runtime version profile.
  Install-smoke diagnostics now include PHP-FPM status, journal output, and
  managed sockets.

- **Caddy redirect-aware boundary smoke**: sensitive-file and out-of-root
  symlink checks now treat the production HTTP-to-HTTPS redirect as safe while
  asserting that the first response cannot disclose either resource. The
  synthetic `.test` host no longer depends on disposable TLS issuance.

- **FTPS site identity and revocation**: vsftpd now chroots each login to
  the site account's actual home directory, accepts only explicitly
  provisioned site users, and no longer derives a site path from the hashed
  Unix username. Root-broker FTPS provisioning changes the password and
  allowlist atomically, and the disposable-host smoke proves encrypted
  upload, download, login denial after revocation, and cleanup.

- **SFTP, Python deployment, and site-permission hardening**: SFTP-only key
  staging now stays under the root-owned authorization directory and the
  generated forced-command policy is validated by `sshd`. Python app
  provisioning uses a complete hash-locked Gunicorn/packaging requirements
  file and publishes a disposable virtualenv only after installation succeeds;
  failed installs cannot publish a partial environment. Site ACLs and
  ownership are applied during initial staging/sealing, with default ACLs for
  newly-created content, while routine PHP runtime/resource changes avoid
  recursive filesystem walks.

- **Web identity and Caddy filesystem isolation**: the installer now persists
  one authoritative web-server group and PHP socket root for the root broker
  and site helper, including Caddy on Debian/Ubuntu and RHEL-family hosts.
  Managed Caddy routes deny environment, VCS, credential, archive, database
  and `node_modules` files, and the vhost helper and site sealing refuse a
  document root containing a symlink.
- **Panel becomes ready after a crash mid-site-transaction**: startup
  recovery re-sealed or removed interrupted sites through the root broker
  without a fencing token, so the broker refused it and readiness stayed
  degraded until an operator intervened. Recovery now takes each site's
  durable lease first and sends that lease's token.
- **Container image starts again**: the production path check rejected the
  image's and Kubernetes manifests' `STEPANEL_BACKUP_ROOT` under
  `/var/lib/ste-panel`, which every production unit can write. It is allowed
  again alongside `/var/backups/stepanel`.

- **wp-cli runs as the site's isolated user**: wp-cli executes the site's own
  `wp-config.php` and WordPress core. Customer WordPress actions, backup
  maintenance mode, and WPress import configuration previously ran it as the
  `stepanel` panel user, so tenant PHP (including PHP from an uploaded
  archive) ran with panel privileges. These calls now go through a typed
  `wordpress` root-broker request. The broker builds wp-cli arguments from
  named operations with fixed patterns, and `stepanel-appctl wp` runs the
  fixed `/usr/local/bin/wp` through `runuser` as the site user with a cleared
  environment, refusing global options such as `--exec`, `--require`, and
  `--path`. The `wp-config.php` database password travels on stdin. WPress
  imports seal the site to its account before the first wp-cli call.
  Production now requires `STEPANEL_WPCLI=/usr/local/bin/wp`.
- **Root broker starts on non-OpenLiteSpeed hosts again**: `/usr/local/lsws`
  is now an optional `ReadWritePaths` entry; requiring it failed systemd
  namespace setup (`226/NAMESPACE`) on Caddy and Apache hosts.

- **Fixes to today's recovery and XSS hardening**: an untyped successful
  `/api/` response now actually gets `Content-Type: application/octet-stream`.
  The earlier default was set after `WriteHeader` and never reached the
  client. WordPress backup quiescing now reads `wp maintenance-mode is-active`
  from its exit status instead of matching the word "active" in its output,
  which had produced backups falsely labeled `application-quiesced`. A
  WordPress install whose wp-cli fails now gets a backup labeled
  crash-consistent instead of no backup. The offsite recovery proof lists only
  manifests and refuses a truncated listing instead of silently sampling only
  the first backups. Recovery proof command output is bounded.

- **Production gate documentation consistency**: corrected the adversarial
  testing coverage map to reference the current CSRF test names, so the
  documentation-reference gate validates cleanly.
- **CodeQL hardening (security)**: secretbox sealing now rejects plaintext
  sizes that would overflow its allocation-capacity calculation, and API error
  normalization captures non-JSON error responses regardless of their declared
  content type. This prevents reflected HTML/script bodies from bypassing the
  JSON error envelope while preserving explicitly generated JSON errors.
- **Backup consistency and capacity admission**: WordPress backups can acquire
  the site mutation barrier and maintenance mode, and manifests distinguish
  application-quiesced backups from crash-consistent logical backups. Backup
  creation now validates the complete site tree before staging, rejects
  symlinks and special files consistently with restore/import safety policy,
  and reserves estimated site, database, encryption, and free-space capacity
  through the shared ledger.
- **Recovery and security-boundary evidence**: application recovery proofs,
  real request-level CSRF tests, staged coverage targets, and repeated recovery
  matrix documentation now strengthen the production gate. Documentation also
  explicitly scopes Unix users, ownership, PHP-FPM pools, and cgroups to
  trusted or semi-trusted shared hosting; StePanel 1.0 is single-host and is
  not a hostile-workload, high-availability, multi-host platform.
- **Archive extraction on installed hosts (fix)**: the shared archive
  extractor set directory modes with `os.Root.Chmod`, which issues the
  `fchmodat2` system call. The panel and worker units' `@system-service`
  seccomp filter rejects it with EPERM on older systemd releases (for
  example AlmaLinux 9), so every installed cPanel restore and backup restore
  failed. Directory modes are now set through an open descriptor (`fchmod`),
  as file modes already were.
- **Deterministic binary and database upgrade rollback (reliability)**: when
  a new release failed its post-install health check, `install.sh` restored
  the previous binary but not the control-plane database the candidate had
  already migrated, so the previous release then refused the newer schema
  (or could not read re-sealed secrets). The installer now snapshots the
  database file set while all services are stopped and, on failure, stops the
  candidate, keeps its database aside for diagnosis, and restores the
  pre-upgrade files exactly before restarting the previous release
  (`deploy/lib/control-plane-txn.sh`). The upgrade smoke's broken candidate
  previously failed before the installer's transaction began, so the
  rollback path was never exercised; it now delegates CLI commands to the
  real binary and damages the database when started as a service, and the
  smoke checks that the restored database verifies.
- **Durable pre-migration snapshots with retention (reliability)**: the
  control-plane snapshot taken before schema migrations was written by
  `VACUUM INTO` straight to its final name with the process umask, never
  fsynced or verified, and never pruned. A crash could leave a truncated file
  that looked like a valid recovery point, the copy was briefly readable by
  other users, and snapshots accumulated with every upgrade. Snapshots are
  now written in a private `<db>.snapshots/` directory under a temporary
  name, fsynced, verified with `PRAGMA quick_check` and their schema version,
  atomically renamed with the directory fsynced, and the newest three are
  kept (including legacy `.pre-migration-*.bak` files). See STATE.md.
- **One archive safety policy (security)**: archive imports, cPanel restores
  and backup restores now share `internal/archivesafe`. Archives that name a
  path twice (also via aliases like `a//b` or `a\b`), or create a file and a
  directory at the same path, are rejected; before, a later entry silently
  replaced an earlier one, so the copy that was inspected need not be the one
  installed. The import executor detected ZIP only from a `.zip` URL suffix
  while the analyzer also used Content-Type; both now detect the format from
  the archive's bytes and apply the same entry rules, so inspection no longer
  approves archives that extraction rejects. Extraction resolves every path
  through `os.Root` and creates files exclusively, so a symlink planted in
  the destination cannot redirect a write. ZIP archives are spooled into the
  capacity-managed import root with free space checked against the capacity
  ledger, instead of the system temporary directory; the import also checks
  disk space against the ZIP's declared uncompressed size before writing.
- **Context-bound encryption (security)**: environment secrets, customer
  TOTP seeds and durable job payloads are now sealed with a per-purpose
  HKDF-derived key and AES-GCM associated data binding each value to its
  site and variable, username, or job ID/kind/owner. Previously no associated
  data was used, so anyone able to rewrite the state database could move a
  ciphertext to another record (for example, copy a known TOTP seed onto
  another account to bypass MFA). Existing values are re-sealed on the first
  start and the legacy format is refused afterwards (`encryption_formats`).
  This migration is one-way: rolling back the binary requires the
  pre-upgrade control-plane database snapshot. Account lookups now also
  reject a stored record whose username does not match its row.
- **Legacy API token hard cutoff (security)**: unscoped legacy tokens now
  stop working at one host-wide deadline, the later of 2026-11-15 and 30
  days after the policy is first activated on the host. Previously the
  30-day clock started at each token's first use, so a dormant or stolen
  token that had not been used never expired. After the cutoff every
  unscoped token is revoked in the token store
  (`auth.api_token.legacy_revoked`), legacy tokens are refused when no
  deprecation policy is configured, and the security center reports the
  enforced deadline instead of a hard-coded date.
- **Typed broker follow-up fixes (security, reliability)**: removing the
  generic helper bridge left gaps that are now closed. Private Git clones in
  the release pipeline and WordPress database cleanup use typed broker
  operations; both previously failed in production. The broker binds a
  private clone's destination to a release staging directory of the
  requesting site and accepts only `git@host:path.git` repositories and plain
  refs. The `stepanel-sitectl` access, resources, quota and runtime actions
  share the account-mutation lock, because each can run `useradd`/`usermod`.
  Runner builds and worker operations are scoped to their site instead of
  running exclusively, so a long build no longer stalls the whole broker.
  Proxy apply/delete and vhost delete take scoped locks.
- **One audit contract (security)**: audit calls now use two explicit APIs.
  Class A security-ledger operations (access grants, credential changes,
  deletions, deployments, restores) durably record an intent before mutating
  and are refused with HTTP 503 when they cannot, then record an outcome.
  Previously several of them (site and database deletion, credential
  rotation, Git deployment, SSH and deploy keys, password changes) were
  audited best-effort after the change, and credential operations applied
  the change before failing on the audit. Revocations (token, session and
  key removal, suspension) are never blocked by the ledger. Telemetry stays
  best effort. An event persisted in the audit outbox now counts as recorded
  even if publication to the signed log is deferred. See
  `docs/AUDIT_CONTRACT.md`.
- **Accurate audit actors (security)**: audit events name the authenticated
  requester instead of the configured panel administrator, including the
  admin suspend/unsuspend endpoints that recorded every action as `admin`.
- **Audit event names**: Class A operations add `<action>.initiated` and
  `<action>.failed` events. `webhook.deploy.accepted` is replaced by
  `site.git-deployed.initiated` (actor `webhook`);
  `webhook.config.update.initiated` is now `webhook.config.updated.initiated`;
  `webhook.config.disable.initiated` is removed (disabling is a revocation);
  restore completions are `cpmove.restore`, `wordpress.restore` and
  `backup.<mode>` instead of `*.completed`.
- **Webhook deploys enforce the policy that authenticated them (security)**:
  `gitDeploy` previously re-read the site's webhook configuration and skipped
  the repository/ref allowlists when that second read failed. The verified
  site and allowlists now travel with the request as one immutable
  authorization, so a store error can no longer disable them and the policy
  cannot change between authentication and deployment.
- **Importable application package (maintainability)**: the root package is
  now `stepanel` and the binary entry point is `cmd/stepanel`. Build with
  `go build ./cmd/stepanel`; version ldflags target
  `github.com/cyberducttape/StePanel.Commit` and `.BuildDate`.
- **Site termination orchestration moved to `internal/sitelifecycle`
  (maintainability)**: the journaled, roll-forward step sequence, its backup
  gate and its audit rules are domain logic behind a `TerminationHost`
  privilege interface, with unit tests that need no host state. Journal step
  names and on-disk format are unchanged.
- **Restore-to-staging path validation is enforced at the filesystem boundary
  (security)**: the shared restore helper now revalidates the destination site
  name immediately before path construction, covering both local and offsite
  restore entry points. A traversal regression test guards the CodeQL-reported
  path flow.
- **Mixed-load CI is independent of the host's production free-space reserve
  (reliability)**: the disposable load harness uses an explicit, bounded test
  reserve so constrained runners do not reject its backup jobs with a false
  HTTP 507 while production retains its 5 GiB reserve.
- **Tighter sandbox for the panel and worker services (security)**: both units
  now drop all capabilities from the bounding set and restrict namespaces, SysV
  IPC, the hostname, and system calls to systemd's `@system-service` set. They
  reach root only through the broker socket and needed none of these.
  `systemd-analyze security` exposure falls from 5.4 (MEDIUM) to 1.5 (OK), and
  the installation smoke now fails if either unit exceeds 2.5. The backup,
  restore, crash-drill, and interrupted-workflow tests pass under the same
  syscall filter.
- **HTTP load gate in CI, and two scaling fixes it found (performance)**: the
  mixed HTTP load test now seeds 200 sites, runs 8 concurrent readers across
  the dashboard APIs while 20 real backup jobs execute, and fails on any error,
  unfinished job, or p95 latency over 500 ms; CI runs it on every push. It found
  that `/api/sites/overview` rescanned the whole sites directory once per site
  (quadratic in site count) and ran the database inventory helper (a root broker
  call in production) on every request, and that an expired service-status cache
  made every concurrent `/api/health` request spawn its own `systemctl`
  processes. Overview now resolves sites in one scan and uses a 5-second
  inventory cache that API database mutations invalidate; one request refreshes
  service status while others wait. Under the same load, max latency fell from
  884 ms to 131 ms and average from 65 ms to 11 ms
  (`docs/HTTP_LOAD_BASELINE_2026-10-02.md`).
- **Terminating a site without its Unix user no longer fails (reliability)**:
  `stepanel-sitectl delete` removed the site tree only when the site's system
  user existed, so terminating an imported recovery site, or retrying after a
  partial teardown, left root-owned state such as the PHP session directory that
  the unprivileged panel could not remove. The helper now removes the validated
  site tree regardless and deletes the user afterwards.
- **Production backups of sites with databases (critical fix)**: on a native
  production install the database dump step of every backup, database
  deletion safety backup, and staging clone ran `sudo stepanel-dbctl dump`,
  but production installs have no sudo policy, so these operations failed for
  any site with a managed database. The installation smokes did not catch it
  because they run a lab-only broker path. Production now asks the root
  broker to stream the dump into a file the panel created; the broker writes
  only into an empty, single-link, non-root-owned regular file under an
  approved staging root, so dumps of any size work and never pass through the
  JSON response. The lab-only shortcuts for database listing, dumps, and
  restores are removed, so the installation smokes now exercise the production
  database paths.
- **Removed dead placeholder code from the root broker**: about 900 lines of
  commented-out database, restore, and vhost "implementations" that only
  pretended to succeed (including a hard-coded placeholder password), and the
  three journal types only they used, are gone from `internal/rootbroker`.
  None of it was reachable, but it read like live privileged code. The
  deferred-work check now also rejects "in a real implementation" and "for now,
  just …" stub phrasing in production code.
- **Workflows proven to wait for locks held by another process**: new tests
  run the real restore job, site termination job, resource update, and account
  suspension handlers while a second lock owner on a separate database
  connection (as the worker is) holds the conflicting lock. Each workflow gives
  up without mutating anything when its deadline passes and succeeds once the
  lock is released.
- **Versioned backup encryption with key rotation (security)**: new
  encrypted backups use the `AES-256-GCM-HKDF-STREAM-v2` envelope. Each backup
  derives its own subkey with HKDF-SHA256 from the configured key and a random
  salt, chunk nonces can no longer repeat across backups, the header (key id,
  salt, chunk size) is authenticated, and a final-chunk marker makes truncation,
  reordering, and appended data detectable. The manifest records the key id
  (`encryption_key_id`), and retired keys listed in
  `STEPANEL_BACKUP_ENCRYPTION_PREVIOUS_KEYS` keep older backups restorable after
  rotation; the installer preserves that setting on upgrade. A restore whose key
  is missing names the key id it needs. Backups written in the v1 format are
  still read, and the archive header must match the scheme named in the signed
  manifest.
- **Long privileged operations are no longer killed after 30 seconds
  (reliability)**: the root broker gave every database dump and restore,
  repository clone, container build, and recursive site ownership change the
  30-second default deadline, and expiry SIGKILLs the helper. In production a
  database restore, backup dump, Git clone, build, or seal of a large site that
  took longer was killed mid-operation, leaving half-applied host state. Every
  broker action now has an explicit timeout class at least as long as the
  panel allows it (up to 120 minutes for size-proportional work), and a test
  fails if a new action is added without one.
- **One page on how StePanel requests root**: `docs/PRIVILEGE_MODEL.md`
  describes the transport, caller authentication, validation, resource locks,
  deadlines, audit, and recovery for every privileged request, and lists the
  generic helper actions still to be replaced by typed requests.
- **A failed restore-to-staging no longer leaves a half-provisioned site**:
  the staging site's account and PHP-FPM pool (created before publishing) and
  its directory are now torn down when the restore does not complete, so an
  orphan pool can no longer break PHP-FPM reloads. The handler also creates the
  import root if it is missing, like the other restore paths.
- **Disk exhaustion drills run in CI**: a backup, a restore, and durable job
  admission are run against a real full filesystem (an 8 MiB tmpfs), and must
  fail cleanly and recover once space is freed.
- **New tests for previously untested guarantees**: end-to-end restore-to-staging,
  and rejection of expired API tokens at the store and middleware.
- **Crash recovery proven at every boundary (Gate 5)**: new drills kill a
  real child process with SIGKILL at each of 19 instrumented points across
  backup, file restore, termination, and account suspension, then run the
  same startup recovery the panel runs (now `recoverUncleanShutdown`) or
  resume the durable job. 114 consecutive drills recovered to the same
  verified state.
- **Crash leftovers are cleaned up (reliability)**: the drills showed that a
  crash during a restore left its extracted copy under the import root and its
  copied tree in manager staging forever, potentially gigabytes each.
  Import-root scratch trees and manager staging are now removed once
  abandoned, at startup and every 15 minutes, and a test fails if a new
  scratch prefix is added without cleanup. Import-stage cleanup also no
  longer errors when the import root does not exist yet.
- **Startup no longer deletes a running worker's release checkout**: panel
  startup discarded every release staging tree, including one an external
  worker process was still building. Staging cleanup now removes only trees
  untouched for longer than the longest operation timeout.
- **Interrupted workflows are proven to end in a known good state (Gate 2)**:
  new acceptance tests drive the real backup, restore, release activation,
  resource update, and termination workflows, interrupt each at every
  instrumented boundary, run the conflicting workflow, and check exact site
  contents, staging, recovery journals, published backups, and route state.
  Termination interrupted at any of its nine steps resumes to completion, and
  a pending route never outlives the site's files.
- **Webhook deploys are audited before they change anything (SECURITY)**:
  signed Git webhooks are the one deploy path not routed through the
  authenticated middleware, which audits every other mutation before it runs.
  They now record `webhook.deploy.accepted` first and refuse with 503 if the
  audit cannot be written, and the completion event names the `webhook` actor.
- **Every mutating route is checked for an audit trail**: a test fails if a
  new mutating route is registered outside the fail-closed audit middleware
  without being a reviewed self-auditing route.
- **State errors are categorized where they happen**: durable job persistence
  failures (temporary when SQLite is only busy), quarantined recovery
  journals, and periodic cleanup failures now feed
  `stepanel_state_errors_total` by category.
- **No deferred-work markers in critical paths**: a test keeps TODO, FIXME,
  and HACK out of production Go code, the root helpers, and the installer.

### Documentation

- **Real screenshots replace the concept mockups**: the README and
  `docs/SCREENSHOTS.md` now show captures of the actual UI instead of
  illustrations that did not match it. `scripts/capture-screenshots.sh`
  regenerates them: it starts a disposable control plane, seeds synthetic
  sites, `.example` domains, backups, and a customer account through the API,
  and captures operator, site workspace, backup, activity, customer, mobile,
  and sign-in views with Playwright.
- **UI fixes found while capturing**: the customer "Your plan and access"
  panel rendered as a white card with unreadable text in the dark theme;
  site workspace action buttons such as "Create verified backup" were
  unstyled; and backup entries showed the full server path instead of the
  backup name.

- **Documentation audit against the code**: every current document was
  checked for references to files, tests, functions, commands, API routes,
  and settings that do not exist. `SECURITY_CLAIMS_VERIFICATION.md` was
  rebuilt (it cited 12 nonexistent tests and helpers, and declared every claim
  verified); `ADVERSARIAL_TESTING.md` now maps each area to its real tests and
  states what is not covered; `CODE_ORGANIZATION.md` was regenerated from the
  tree; the root broker guide lists only the operations the broker executes
  and drops invented benchmark figures; the upload-hardening and pip documents
  describe current behavior. Status documents (`CURRENT_STATUS`,
  `PRODUCTION_READINESS`, `FEATURES`, `ROADMAP`, architecture) reflect the
  current release state, and the README and installation guide say that guided
  setup ships after v0.7.0.
- **Reference documentation completed**: a command reference covering every
  `stepanel` subcommand, operator settings that were undocumented (TLS files,
  trusted proxies, build-runner policy, database tool URL, Linode token), and
  the lab-only settings.
- **Documentation references are checked in CI**: `scripts/check-docs-references.py`
  fails the build when a document names a file, test, function, command, route,
  or setting that does not exist, with an explicit allowlist for planned items.

### Setup

- **Guided installation**: `sudo ./install.sh --guided` asks seven questions,
  validates each answer immediately, generates all six keys, shows the host
  preflight, and installs only after confirmation. It confirms the
  authenticator app by asking for a current code (no lockout from a mistyped
  key) and proves the offsite backup location with a write, read-back, and
  delete test. Passwords are hashed before saving and no secret is printed.
- **Reusable settings files**: `stepanel setup` writes the answers to a
  root-only settings file, and `install.sh --config FILE` installs from it
  (with `--dry-run` to preview). Config files are parsed with the same strict
  rules as `/etc/ste-panel.env` and must not be readable by other users.
- **Safer first run**: the guide refuses to run where StePanel is already
  installed, because new keys would make existing encrypted backups
  unreadable. The old `stepanel setup` and `stepanel init` wizards, which
  printed secrets and wrote files the installer could not read, are replaced;
  `init` is now an alias for `setup`. The installer no longer prompts for a
  password when a password hash is supplied.

### Recovery

- **Restore rehearsals are recorded and summarized per site**: every
  rehearsal, passed or failed, is stored with its timing (migration 10), and
  `GET /api/sites/recovery/{site}` plus a Recovery status panel on the Backups
  tab report a confidence level (`verified`, `degraded`, `failing`,
  `unverified`, `no_backup`) with its reasons, the last backup and offsite
  copy, measured recovery time, recovery point, and whether the encryption key
  is proven. The status states what a rehearsal proves: the archive is
  verified, decrypted, and extracted; database import and application start
  are not yet rehearsed.
- **Automatic rehearsals follow scheduled backups**: a successful scheduled
  backup queues a rehearsal of that backup when the site has not been
  rehearsed within `STEPANEL_REHEARSAL_INTERVAL_HOURS` (default 24; `0`
  disables). Rehearsals refuse to start without the free-space reserve.

### Durable Jobs and Control Plane

- **Job cleanup no longer diverges from SQLite**: `Jobs.Cleanup` removed expired
  jobs from memory before deleting their rows, so a failed delete (busy
  database, I/O error, commit failure) left rows that no later cleanup knew
  about and that returned on restart. Rows are now deleted in one transaction
  first and the jobs are restored to memory if anything fails, so the next
  cleanup retries them. The file store had the same ordering bug and is fixed.
- **Faster Job Center and worker claims**: `Jobs.List` loads jobs in one query
  instead of one per job, and migration 9 indexes listing order and the
  per-owner claim check. With a 20,000-job history, listing drops from 15 ms to
  2.4 ms and a worker claim from about 72 ms to about 4 ms.
- **Stated control-plane capacity target**: at 500 sites, session validation
  p99 stays at or below 25 ms with 50 Job Center viewers and two workers
  (measured 6–8.6 ms, previously about 146 ms). See
  `docs/LOAD_BASELINE_2026-10-02.md`.

### Root Broker

- **Privileged operations run concurrently (SECURITY)**: the socket broker
  served one connection at a time, so a long Composer install or certificate
  issuance blocked every other privileged operation and made readiness report
  the broker unhealthy. Connections are now served concurrently with read and
  write deadlines, a request size cap, and a connection limit. A scheduler
  serializes work per site, database, account, or certificate domain, runs at
  most `-max-concurrent` operations (default 8), and lets health probes bypass
  the queue. A peer that connects and stalls is disconnected.
- **Host account changes are serialized**: `stepanel-sitectl` helper requests
  now share the broker's account mutation lock with typed site operations, so
  concurrent requests cannot race on `useradd`, `userdel`, or `usermod`.
- **One domain validator everywhere (SECURITY)**: the typed broker accepted any
  string containing a dot. HTTP handlers, the typed broker, and the helper
  schema now share `internal/domainname`: label-by-label validation and an
  explicit IDNA policy (ASCII only; internationalized names in punycode form).
  **Behavior change:** Unicode names, all-numeric top-level labels such as
  `1.2.3.4`, and reserved `ab--` labels are rejected.

### Outbound Requests

- **Task completion webhooks cannot reach internal addresses (SECURITY)**: a
  site user could point a webhook at loopback, private, or cloud metadata
  addresses, and curl sent it from the host network. Webhooks and archive
  imports now share `internal/safehttp`, which allows only public addresses,
  checks the address actually connected to (defeating DNS rebinding),
  revalidates redirects, and ignores environment proxies. Task units deliver
  webhooks with `stepanel task-webhook` instead of curl and deny the cloud
  metadata ranges. **Behavior change:** webhooks to private receivers are
  refused, and existing task units pick up the change when the task is saved
  again.

### Installer

- **Explicit consent before taking over a host**: the installer stopped and
  disabled any other web server without warning. It now prints a preflight of
  detected services, listeners, and content plus the proposed changes, and
  refuses to stop a web server that was already running unless
  `--take-over-host` is given. `--dry-run` validates and prints the plan without
  changing anything. A missing Caddy or OpenLiteSpeed repository is reported
  clearly instead of failing inside the package manager.

### Web Interface

- **One request layer for the browser**: every script now uses
  `StepanelAPI` (`get`, `post`, `put`, `patch`, `delete`, `request`) for CSRF,
  JSON, timeouts, cancellation, and error classification, and server errors
  show their request ID. `deploy.js`, `database.js`, and `htaccess.js` are no
  longer committed as minified one-liners. Frontend unit tests run in CI.

### Documentation

- **Accurate lifecycle authority gate**: Gate 1 now describes how site
  lifecycle actually works (lifecycle handlers orchestrate, `SiteManager` owns
  canonical paths, the root broker executes privileged steps), and a test fails
  if it routes an operation to an unimplemented `SiteManager` method.
- **Tenant isolation stated precisely**: `SECURITY.md` separates control-plane
  authorization and per-site process isolation (provided) from a
  hostile-workload boundary and certified multi-tenant isolation (not provided).

### Production Safety

- **Safety bypass flags can no longer persist silently (SECURITY)**: the
  installer refuses to run when quota/reconciliation/lab bypass variables are
  present in the environment or an existing `/etc/ste-panel.env` unless invoked
  with `--unsafe-lab`, which also records `STEPANEL_UNSAFE_LAB=1`. Production
  startup refuses any bypass without that marker; with it, startup logs a
  critical warning and records an audit event, and production readiness reports
  a critical failure. **Upgrade note:** hosts whose env file carries a bypass
  must remove it or rerun the installer with `--unsafe-lab`.

### Job Center

- **Active operation count is authoritative**: `/api/jobs` and the event-stream
  snapshot now always include every queued or running job in addition to the
  100 most recent, and the browser counts active work across all known jobs
  instead of only the eight rows on screen.
- **Snapshots replace stale jobs**: an event-stream snapshot now replaces the
  browser's job list, so jobs pruned on the server no longer linger.
- **Cancellation failures are shown**: both cancel buttons show the request in
  flight and report a failed cancellation next to the job instead of
  swallowing the error.
- **Drawer keyboard support**: opening Operations moves focus into the drawer;
  Escape or Close hides it and returns focus to the toggle.

### Root Broker

- **Helper RPC arguments are schema-validated (SECURITY)**: the compatibility
  `helper` request previously forwarded up to 32 caller-controlled arguments
  checked only for length and control characters. Each allow-listed action now
  declares its exact arity and a semantic type for every argument (site-bound
  paths, identifiers, ranges, private backends, pinned images); anything else is
  rejected before a root process starts. The unused `gitctl verify` action was
  removed from the allowlist.
- **Broker subprocess output is bounded while the child runs (SECURITY)**: the
  root broker used `CombinedOutput()`/`Output()` and checked sizes afterwards,
  so a helper writing gigabytes was fully buffered in the root process first.
  Every broker subprocess now runs through `helper.RunCapped`/`RunCappedSeparate`,
  which cap stdout and stderr as they arrive and kill the whole process group on
  overrun, timeout, or cancellation. A source-level test rejects new unbounded
  calls in the broker package.

### Environment Variables

- **Saving environment variables no longer erases secrets (SECURITY)**: `PUT
  /api/sites/environment/{site}` previously replaced the whole map, and the UI
  submitted redacted secrets as blank values, so editing any variable silently
  deleted every stored secret. `PUT` now merges per-variable `set`, `preserve`,
  and `delete` operations; a blank secret without an explicit operation is
  rejected. **API behavior change:** variables omitted from the body are now kept;
  send `{"operation": "delete"}` to remove one.
- **Encrypted environment state no longer leaks into runtime memory (SECURITY)**:
  after a database-bound persist, the encrypted image was unmarshalled back into
  the live store, so in-process reconciliation could apply ciphertext to sites and
  a later persist could encrypt it twice. Bound stores whose durable form differs
  from their runtime form now implement a codec, and the environment store alone
  decrypts its durable payload.
- **Environment values reach processes byte-for-byte**: values are now written
  to the systemd `EnvironmentFile=` double-quoted with `\`, `"`, `` ` ``, and `$`
  escaped, so spaces, quotes, backslashes, `#`, and `$` survive unchanged. A
  round-trip test runs values through a real systemd manager. Existing site
  environment files are rewritten (and services restarted once) on the next
  reconciliation.
- **Environment names match the host helper**: names starting with a digit are
  rejected by the API instead of becoming unappliable desired state.
- **Explicit secret flag in the UI**: new and existing variables have a Secret
  checkbox and a Remove action; secrecy is no longer inferred from the input type.

### Security Testing & Hardening

- **Comprehensive authorization test suite (SECURITY)**: Implemented 25+ adversarial tests
  validating critical authorization boundaries: API token scope enforcement, privilege
  escalation prevention, CSRF token protection, and cross-tenant access denial. Tests verify
  that limited-scope tokens (site:read, deploy:write, backup:create) cannot exceed
  permissions, non-admin users cannot claim admin status, and CSRF tokens are required
  for browser-based mutations but not API requests. All tests passing with clear failure
  modes for security violations.

- **Production readiness validation (OPERATIONAL)**: Added startup validation ensuring
  filesystem quotas are actually enforceable in production. Previously, UI advertised disk
  quotas (e.g., "10 GB limit") even if underlying filesystem didn't support quota
  enforcement, creating false security guarantees. Now: startup fails in production mode
  if `/var/www` (or configured STEPANEL_WEB_ROOT) doesn't have usrquota/grpquota support,
  with clear remediation message. Complements existing quota-enforcement mechanism.

- **Production readiness diagnostic endpoint (OPERATIONAL)**: Added `/api/admin/production-readiness`
  endpoint returning health status of 4 critical capabilities: filesystem quotas, encryption
  keys, offsite backups, and TLS configuration. Returns overall_status (healthy/degraded/critical)
  with per-check detail and remediation guidance. Enables operators to verify production
  prerequisites before deployment.

- **Node startup hardening documentation (OPERATIONAL)**: Documented recommended refactoring
  of Node app startup from shell-based (bash -lc 'nvm use VERSION; npm start') to resolved
  binary paths (absolute /opt/stepanel/.nvm/versions/node/vX.Y.Z/bin/npm). Reduces attack
  surface by eliminating shell execution at startup, improves auditability, and enables
  early validation that requested Node version is installed. Implementation targeted for
  helper layer (stepanel-appctl) in next phase.

- **Tier 1 code quality standards applied (CODE QUALITY)**: Audited codebase against CLAUDE.md
  Tier 1 standards and fixed violations. Removed hardcoded example domains (example.com,
  destination.example.com) from user-facing migration analysis output, replacing with actual
  requested hostnames. Verified filesystem operations, archive extraction, configuration
  management, error handling, and progress reporting all meet production standards. No critical
  violations found; codebase demonstrates strong security posture.

### Reliability and Compatibility

- **OCI image reference parsing**: Build-image validation now uses the
  `distribution/reference` parser, supports multi-level namespaces, requires
  a SHA-256 digest at deployment boundaries, and applies registry and optional
  namespace/repository allowlists separately. Image-size enforcement remains
  unimplemented and is not claimed as a release control.

### Security Fixes (CRITICAL - Archive Import Hardening)

- **SSRF protection with DNS resolution (CRITICAL)**: Fixed incomplete SSRF validation
  that only rejected literal private IPs in URLs but allowed hostnames resolving to
  private/reserved addresses. Now validates all DNS-resolved IPs via custom DialContext,
  preventing DNS rebinding attacks. Rejects private IP ranges (RFC 1918), loopback,
  link-local, IPv4-mapped IPv6, and validates addresses after HTTP redirects.
  Added comprehensive test coverage for all reserved ranges.

- **Safe atomic config file updates (CRITICAL)**: Fixed two issues in archive import
  configuration updates: (1) unconditional permission weakening (hardcoded 0644 replacing
  0600/0640 on sensitive files), and (2) brittle text replacement breaking on valid
  config formats. Now: preserves original file mode via atomic temp+rename, detects and
  skips function-call values (getenv, env), handles whitespace variation, validates paths.
  Prevents permission escalation and config corruption.

- **Config path traversal validation (CRITICAL)**: Added explicit path validation to
  config file selection, using same `EnsureInside()` pattern as archive extraction.
  Rejects absolute paths, `..` sequences, paths escaping site root. Validates path
  remains regular file (rejects symlinks/directories). Closes attack vector where
  malformed config path could target files outside site root.

- **Tar archive type validation (CRITICAL)**: Fixed overly broad tar extraction logic
  that treated unrecognized entry types as normal files. Now explicitly handles only
  tar.TypeReg, tar.TypeRegA, tar.TypeDir and rejects character devices, block devices,
  FIFOs, sockets, sparse extensions, and unknown types. Prevents extraction of malicious
  device nodes or pipe metadata.

- **Node/Composer command execution (CRITICAL)**: Fixed shell array expansion vulnerability
  in `deploy/integrations/stepanel-appctl` where Bash arrays wrapped in single quotes
  within `sh -c` prevented proper argument passing. npm/yarn/pnpm/composer commands
  received entire array as single quoted string, causing failures. Now uses
  `sh -c 'cd "$1" && shift && exec "$@"'` pattern to properly pass array elements.

### Security Fixes (HIGH - Archive Import)

- **File permission preservation on extraction (HIGH)**: Restored file executable bits
  during archive extraction (both tar and zip). Previously `os.Create()` applied umask,
  losing executable bits on scripts/binaries/CGI programs. Now restores archive metadata:
  tar header mode for .tar.gz, zip entry Mode for .zip. Masks to 0644|executable bits
  (rejects setuid/setgid/sticky). Prevents application breakage post-migration.

- **Filesystem mutation error checking (HIGH)**: Added error checking to all `os.MkdirAll()`
  calls in archive extraction paths. Previously ignored directory creation failures,
  leading to confusing secondary errors or partial extraction state. Now validates every
  mkdir operation fails early with clear error messages.

### Production Readiness Fixes

- **Archive import database workflow completed (P0 RELEASE BLOCKER)**: Archive import
  now succeeds end-to-end with file extraction, configuration updates, AND database
  dump identification. Previously, workflow would extract files then fail requiring
  manual database restore. Now: (1) Files extracted and site created, (2) SQL dump
  located and validated, (3) Operator receives clear restoration command with path.
  Phase 2 (v0.8.0) will add automated restoration when credentials provided. Database
  restoration no longer a blocker for release.

- **Archive inspection made asynchronous (PHASE 2)**: Moved archive inspection from
  synchronous HTTP request handling to durable job system. Inspection can now handle
  gigabyte-scale archives without blocking requests. POST `/api/admin/archive/inspect`
  returns immediately with job_id; GET `/api/admin/archive/inspect/status?job_id=...`
  polls results. Completes half-finished Phase 2 transition already in codebase.

- **Import result semantics (CORRECTNESS)**: Fixed major semantic issue where imports
  reported `success: true` even when critical database restoration failed. Now:
  `Success` flag reflects critical failures, status changes to `completed_with_errors`,
  NextSteps clearly indicate manual restoration required. Database restoration failures
  no longer hidden in Issues array.

- **Progress calculation fixed (UX)**: Replaced misleading modulo-based progress
  (20+files%40) that jumped backwards every 40 files with monotonic calculation
  (20+40*files/1000). Progress now advances linearly 20-60% during extraction without
  appearing to regress.

- **Production readiness documentation (DOCUMENTATION)**: Created authoritative
  `docs/PRODUCTION_READINESS.md` consolidating contradictory production status claims.
  Defines deployment classifications (Development, Beta, Single-Host Candidate, Multi-Tenant,
  GA), honest capability matrix by workflow (cpmove, WordPress, generic archive, git),
  prerequisites, blocking issues for multi-tenant, and GA roadmap. Fixes broken link in
  SECURITY.md.

- **Removed unused SkipAnalysis parameter (API CLEANUP)**: Removed dead code accepting
  `skip_analysis` parameter in archive import API. Parameter was never used (analysis
  always performed); created misleading API contract. Security improvement: validation
  skipping should never be normal feature.

### Performance Fixes (HIGH)

- **Backup listing verification DoS fix (HIGH)**: Fixed self-imposed denial-of-service
  where listing backups re-verified every archive with full SHA256 and file-by-file
  hashing. Implemented in-memory verification cache (5-minute TTL) to skip expensive
  archive traversal on listing operations. Restore operations still verify fresh
  (bypassing cache) to ensure safety before destructive operations. Reduces backup
  listing time from O(archive hash * count) to O(manifest reads) with LRU-ish cache
  eviction when exceeding 1000 entries. Phase 2 will add SQLite-backed persistent
  verification with background re-verification job.

### Security Fixes (Critical)

- **Python deployment privilege escalation (CRITICAL)**: Fixed root privilege
  escalation vulnerability in `deploy/integrations/stepanel-appctl` where
  root was executing `pip install` without version pinning or hash verification.
  Now: virtualenv ownership transferred to $site_user immediately after creation,
  pip install executed as $site_user with `runuser`, Gunicorn pinned to v23.0.0
  with PyPI hash verification. Matches Node deployment security model.

### Security Fixes (HIGH)

- **Build runner registry allowlist (HIGH)**: Restricts container images to
  allowlisted registries (STEPANEL_RUNNER_ALLOWED_REGISTRIES, default:
  docker.io, ghcr.io, quay.io). Combined with the pinned-digest requirement
  (image ref must be `name@sha256:...`), rootless Podman, `--cap-drop ALL`,
  `--security-opt no-new-privileges`, `--read-only`, CPU/memory/PID limits,
  bounded tmpfs, and network=none by default (STEPANEL_RUNNER_NETWORK_MODE),
  this bounds what a build sandbox can execute.

  **Corrections to prior CHANGELOG entry (published claim was stale):**
  - The old `STEPANEL_MAX_IMAGE_SIZE` claim was removed. The active setting
    is `STEPANEL_RUNNER_MAX_IMAGE_BYTES`; the privileged runner pulls the
    pinned digest, inspects the resulting image size, and rejects images
    above that byte limit before execution.
  - The env var for network default is `STEPANEL_RUNNER_NETWORK_MODE`
    ("none" / "egress"), not `STEPANEL_RUNNER_NETWORK_ENABLED` as the prior
    entry named.

- **Build runner image allowlist (HIGH, added Nov 2026)**: New optional
  STEPANEL_RUNNER_ALLOWED_IMAGES tightens the registry-wide allowlist to
  specific images or namespaces. When set, an image is accepted only if
  its `registry/namespace/repo` matches at least one entry — otherwise the
  build is refused with a clear error. Registry-wide allowlisting alone
  meant that allowing ghcr.io let a site select any public image under
  ghcr.io; this narrows the trust boundary to what an operator explicitly
  approved.
  - Patterns: exact `ghcr.io/anthropic/builder` OR trailing-wildcard
    `ghcr.io/anthropic/*` (any repo under a namespace).
  - Backward compatible: empty value keeps the previous behavior
    (registry allowlist alone).

- **Process group cleanup on timeout (HIGH)**: Fixed resource leak where timeouts/
  cancellations killed parent helper process but left child processes running
  indefinitely. Now all helpers configure process group (Setpgid=true) and timeout
  kills entire process group with SIGKILL (-PGID) instead of just parent PID.
  Prevents accumulation of orphaned pip/npm/podman/systemctl/git processes and
  concurrent mutations outside StePanel's lock lifecycle.

- **Git webhook secret isolation (HIGH)**: Fixed webhook authentication blast radius
  where single global `GitWebhookSecret` could deploy any repository to any site.
  Implemented per-site webhook configuration framework:
  - New `GitWebhookConfig` type stores per-site webhook secrets, allowed repos, allowed refs
  - Webhook URL changed from `/api/sites/git-webhook` to `/api/sites/git-webhook/:site`
  - Site name now explicit in path, enabling per-site secret lookup (Phase 2)
  - New helper functions for safe webhook path parsing and signature verification
  - Temporarily falls back to global secret with deprecation notice
  - Phase 2 will migrate to database-backed per-site configuration with validation
  - Reduces blast radius from all sites to single site on secret compromise

- **Git webhook site-binding (CRITICAL)**: Fixed follow-up to the per-site secret
  change above. The webhook path change gave every request a URL-derived site but the
  request context only carried a boolean "is-webhook" flag, so `gitDeploy` still read the
  deploy target from the JSON body's `"site"` field. A signature valid for site A therefore
  authorized a deploy on site B whenever the body claimed a different target — meaning
  the "reduces blast radius from all sites to single site" claim above only held when
  clients happened to spell the same site twice.
  - Context now carries the authenticated site name (`gitWebhookSiteKey`), not a bool.
  - `gitDeploy` uses that site as the sole trusted target for webhook requests.
  - A body `"site"` that disagrees with the URL/HMAC-authenticated site returns 403 and
    audits `webhook.site.mismatch`. An absent body site falls through to the authenticated
    one, so payload shapes that omit `"site"` continue to work.
  - Regression test `TestGitDeployWebhookRejectsCrossSiteBody` locks the fix in.

- **Git webhook ref-pattern matching (HIGH)**: Fixed broken glob matching in
  `AllowedRefs`. `matchGlobPattern` stripped `*` from the pattern and did a `strings.Contains`
  check, so `refs/heads/release/*` authorized any ref containing the substring
  `refs/heads/release/` — including unrelated branches like `refs/heads/other-release/main`.
  Patterns and refs were also lowercased, treating a case-sensitive namespace as
  case-insensitive.
  - New anchored matching uses `path.Match` semantics: `*` matches within a single path
    segment, `?` matches one non-`/` rune, character classes are supported.
  - New `**` suffix enables recursive matching (e.g. `refs/heads/release/**` matches
    `refs/heads/release` and all descendants).
  - Bare-branch shorthand preserved for backward compat: pattern `main` still
    authorizes `refs/heads/main`. Patterns beginning with `refs/` are strictly anchored.
  - Matching is case-sensitive, matching Git's own ref semantics.
  - Regression tests cover near-match refs, prefix collisions, recursive globs, and case.

- **Archive extraction hardening**: Comprehensive security validation for
  site import archives to prevent path traversal, symlink escapes, and zip bombs:
  - New `safeTarExtractPath()` prevents absolute paths, .. sequences, symlink traversal
  - Reject tar symlinks/hardlinks entirely during extraction
  - Archive bomb protection: 50GB decompressed size limit, 10GB per-file limit, 1M file limit
  - Use `io.LimitReader` for bounded extraction to prevent DoS
  - Both tar.gz and zip formats use identical validation

### Production Readiness Improvements

- **Archive Importer interface (Phase 1)**: New separate interface for importing
  websites from compressed archives (`.tar.gz` or `.zip`) stored in cloud storage
  or HTTP endpoints. Accepts archive URL and config file location as parameters.
  Analyzes archive structure, detects site type (WordPress, custom), and extracts
  database requirements, PHP version, and required extensions from config files.
  Admin-only API endpoints: `POST /api/admin/archive/inspect`,
  `POST /api/admin/archive/import`. Phase 1 provides inspection and analysis;
  Phase 2 (planned) will implement actual import with job tracking. See
  [`docs/ARCHIVE_IMPORTER.md`](docs/ARCHIVE_IMPORTER.md).

- **Security claims verification documentation**: Created
  [`docs/SECURITY_CLAIMS_VERIFICATION.md`](docs/SECURITY_CLAIMS_VERIFICATION.md)
  mapping all product claims ("safety-first", "tenant isolation", "durable jobs",
  etc.) to their code implementations, line numbers, and test coverage. Serves as
  audit checklist and grounds marketing claims in implementation.

- **Release artifact verification**: Added `scripts/verify-release-artifacts.sh`
  to validate packaging integrity in CI/CD pipeline, checking for:
  - Actual gzip compression (prevents mislabeled archives)
  - SHA256 checksum validity
  - SBOM presence and JSON validity
  - Required files in archives (LICENSE, README, SECURITY, binary)
  
  Integrated into `release.yml` GitHub Actions workflow to catch packaging
  defects before publication.

- **Helper package extraction (Phase 3)**: Migrated privileged helper command
  execution and file system safety utilities to `internal/helper/helpers.go`,
  including bounded command execution, safe path validation, symlink protection,
  and atomic writes. Maintains backward compatibility via root-level wrapper
  functions, enabling broader reuse of safety-critical utilities.

- **Package extraction (Phase 1-2)**: Migrated backup type definitions to
  `internal/backup/types.go` and database migration system to
  `internal/migration/schema.go` to improve code organization, testability, and
  reduce root package complexity (~27k LOC). Types are imported and aliased in
  root package for backward compatibility. See
  [`docs/archive/PACKAGE_EXTRACTION_PLAN.md`](docs/archive/PACKAGE_EXTRACTION_PLAN.md).

- **Expanded adversarial tenant isolation test coverage**: Added three new test
  scenarios (`TestCrossTenantBackupAccessDenied`, `TestPlanEnforcementIsolationPerTenant`,
  `TestTenantConcurrentAccessIsolation`) to verify multi-tenant authorization
  enforcement and catch race conditions in `SiteCapability` enforcement.

- **Customer-facing restore-to-staging documentation**: Created
  [`docs/CUSTOMER_WORKFLOWS.md`](docs/CUSTOMER_WORKFLOWS.md) with comprehensive
  guide for customers to safely test backup restoration before promoting to
  production, including step-by-step API examples, best practices checklist,
  and troubleshooting procedures.

- **Plan enforcement and account suspension workflows**: Added three new
  operator API endpoints for managing shared-hosting customer limits:
  - `GET /api/admin/plan-status` — Monitor customer usage vs. plan limits
  - `POST /api/admin/suspend` — Suspend account with audit logging (manual or automatic)
  - `POST /api/admin/unsuspend` — Lift account suspension
  
  Implements automatic suspension when customers exceed resource limits
  (sites, databases), automatic lifting when usage drops, temporary vs.
  permanent suspension modes, and full audit trail integration. Added
  [`docs/PLAN_ENFORCEMENT.md`](docs/PLAN_ENFORCEMENT.md) operator runbook with
  daily review procedures, customer upgrade workflows, and best practices.

- Tenant isolation at the data-access layer is now enforced by the type system:
  all site-scoped data-mutation functions (`CreateSiteBackup`,
  `backupRestoreFiles`, `pruneSiteBackups`, `cloneManagedDatabaseToStaging`,
  `ComposerStore.save/get`, and the site-lifecycle teardown chain) now accept a
  `SiteCapability` interface instead of a plain `site string`. This capability
  can only be obtained from `requireSiteAccess` (HTTP handlers) or
  `authorizeDurableSiteJob` (durable jobs), making it a compile-time error to
  call these functions without first verifying the caller's ownership. The
  HTTP-handler boundary and durable-job execution boundary both produce
  capabilities now; background reconciliation loops (`reconcileEnvironments`,
  `reconcilePHPProfiles`) operate on already-persisted desired state and remain
  on string parameters (documented as "excluded by design"). `handleSiteTermination`
  now performs the same durable-job ownership recheck as its sibling job handlers
  (`handleBackupJob`, `handleBackupRestoreJob`), closing a defense-in-depth gap.
- Added a standalone session-revocation action, independent of any other
  account mutation. Previously the only way to end a customer's session was
  as a side effect of changing something else (password, MFA, suspension,
  deletion). `POST /api/account/sessions/revoke` lets a customer log out
  every other session while keeping the one making the request active (for a
  suspected stolen cookie or a lingering session on another device);
  `POST /api/accounts/{username}/sessions/revoke` lets an administrator
  force-log-out a customer entirely, without suspending the account or
  resetting credentials. Added `Registry.RevokeUserExcept` in
  `internal/session` to back the self-service case.
- Every customer-facing, site-scoped API handler now records a
  `tenant.access_denied` audit event when `a.canAccessSite` refuses a
  request, instead of returning the 403/422 silently. A customer probing
  another tenant's site across any of the roughly 30 site-scoped endpoints
  previously left no trace. `handleCPMoveJob` and `handleWPressJob` also now
  recheck tenant ownership at execution time via `authorizeDurableSiteJob`,
  matching `handleBackupJob`/`handleBackupRestoreJob`; both are currently
  reachable only from administrator-only routes, so this is defense-in-depth.
- Added `tenancy.go`: `a.requireSiteAccess` replaces the raw `a.canAccessSite`
  check at all ~30 of those call sites with a single call that performs the
  check, the denial audit, and the HTTP error response together, returning an
  `AuthorizedSite` capability value. A redundant
  `!a.Auth.IsAdministrator(r) &&` guard in `siteManage` was simplified away as
  part of the migration (canAccessSite already returns true for an
  administrator unconditionally); `siteManage` previously had no test
  coverage at all, so `TestSiteManageDeniesCrossTenantRouteDeletion` was
  added alongside it.
- `runBoundedCommand` now enforces its `context.Context` argument itself
  (start, wait in a goroutine, kill on `ctx.Done()`) instead of relying
  entirely on the caller having built the command with
  `exec.CommandContext`. Every current caller already did, so nothing was
  actually hanging in production, but the function's own signature promised
  enforcement it did not keep.
- Pinned `toolchain go1.26.7` in `go.mod`, matching the exact version already
  pinned in the Dockerfile and `release.yml`.

## [0.7.0] - 2026-09-10

The stabilization release focuses on isolation, recovery, and operational
consistency. It is intended for controlled single-host operator deployments;
the shared-hosting beta remains explicitly limited and is not a complete
multi-tenant hosting platform.

### Post-checkpoint stabilization

- Added per-administrator rate limiting (5 actions/15 minutes) on the
  account-recovery endpoints (MFA reset, credential recovery, recovery-code
  regeneration), bounding the blast radius of a compromised admin session or
  a runaway automation script working through many customer accounts.
- Site termination now requires its offsite upload to succeed when
  `STEPANEL_REQUIRE_OFFSITE_BACKUP=1` is set, closing a gap where a
  locally-verified-only backup could satisfy the destructive-delete gate.
- Removed a redundant, never-invoked audit-failure tracker added alongside
  the security-headers/permission-handling fixes; `MustAudit` now fails
  closed (HTTP 503) on the account-recovery, API-token, and database-delete
  endpoints, and the remaining audit call sites log failures loudly via
  `ShouldAudit` instead of discarding them silently. The pre-existing
  sticky `audit_state` readiness check (`AuditPersistenceError`) already
  covered this at the process level and is unchanged.
- Added the canonical [`STATE.md`](docs/STATE.md) disaster-recovery inventory,
  including control-plane SQLite, TOTP replay, audit, key, journal, and
  provider state.
- Persisted accepted TOTP replay counters in the control-plane database so
  restart does not reopen the active replay window.
- Added validator fuzz targets and a coverage-floor gate; `make fuzz-smoke`
  provides a short local fuzzing pass.
- Added disposable-host `systemd-analyze security` checks for the panel and
  worker services and clarified OpenLiteSpeed's Tier 2 operator-integrated
  support boundary.
- Reconciled the operations and installation runbooks with the authoritative
  SQLite control-plane state model, and added a weekly/manual fuzzing workflow
  for privileged-input and archive validators.
- Added a release-acceptance destructive recovery lab covering process kills,
  reboots, storage exhaustion, database loss, lease recovery, offsite-transfer
  interruption, corrupted control-plane state, and blank-host restoration.
- Corrected shared-hosting operations guidance to describe existing
  fail-closed disk/inode quota enforcement accurately.
- Added a scheduled N-1 (`v0.6.0` to candidate) disposable-host upgrade smoke
  test with control-plane backup and restore verification.
- Published measurable controlled-single-host RPO/RTO objectives and linked
  them to backup-alerting and destructive recovery release evidence.
- Added a process-death site-transaction recovery test and extended N-1
  upgrade smoke coverage to prove installer rollback after a failed candidate.

### Production architecture checkpoint

- Durable SQLite control-plane state now covers accounts, site ownership,
  sessions, API tokens, jobs, leases, cancellation, retry/dead-letter state,
  resource profiles, routes, DNS claims, and lifecycle state.
- Workers can run independently under `stepanel-worker.service`; job status,
  listings, claims, cancellation, and recovery refresh from the authoritative
  database across process restarts.
- Customer and administrator API tokens have hashed, expiring, revocable
  credentials with explicit scopes. Customer data access is tenant-scoped at
  both HTTP and data-operation boundaries.
- Site lifecycle, route activation, staging, resource enforcement, and
  recovery readiness now use durable desired/applied state and fail closed when
  required host enforcement or control-plane integrity is unresolved.
- Customer domain activation requires a DNS TXT ownership claim and rechecks
  that claim for both production and staging routes.
- Recovery drills are wired into `make audit` and CI. The drill runner limits
  Go package parallelism for constrained hosts; the disposable-host, power-loss,
  and provider-failure drills remain release acceptance work.

### Changed

- Shortened the README landing page around the project identity, dashboard
  preview, architecture, quick workflows, production status, and deeper
  documentation links. Detailed configuration and migration procedures now
  live in their dedicated operator guides.

- Extracted bounded site filesystem accounting into the tested
  `internal/usage` package. Root HTTP code now supplies only authorization,
  path policy, and response handling.

- Extracted site-operation serialization into the tested
  `internal/operations` package and routed all site mutation callers through
  its explicit API, reducing root-package coupling without a disruptive
  application rewrite.

- Repositioned the project as a Caddy-first Linux hosting control plane and
  replaced interview-oriented engineering material with architecture decision
  records.
- Added a reproducible interrupted-restore case study and clarified that the
  next milestone is a stability/architecture release.
- Extracted bounded login throttling and peer-address policy into the tested
  `internal/auth` package, and added a first-class `make audit` target covering
  formatting, vet, tests, race tests, and release metadata.

- Reworked the live dashboard into the same dark, site-focused workspace
  shell used by the developer views: persistent desktop navigation, compact
  operator context, responsive mobile header, and consistent interactive
  cards without changing existing API routes or controls.
- Replaced the retired light dashboard illustration with a current dark
  workspace preview and regenerated the PNG used by product documentation.

### Fixed

- Corrected Apache and Caddy staging Basic Auth helper validation. Apache now
  initializes validated site data before deriving its auth-file path, accepts
  valid bcrypt hashes, and rolls staged auth state back with the vhost when
  validation or reload fails. OpenLiteSpeed staging requests now explicitly
  reject Basic Auth instead of silently creating an unprotected route.

- Made scheduled-task and worker desired-state writes transactional in memory:
  a failed durable state write restores the previous in-process value rather
  than reporting an unavailable operation while later requests observe a
  phantom change.

- Added a per-customer session generation to credential, MFA, and suspension
  changes. Password, TOTP, recovery-code, or lifecycle rotations invalidate
  existing customer sessions even if a separate session-registry persistence
  operation fails.
- Switched durable session-registry writes to the shared atomic state writer,
  including parent-directory synchronization after rename.
- Corrected malformed shell conditionals in the privileged Git and build-runner
  helpers and made the helper scripts pass `bash -n` and ShellCheck validation.

### Added

- Added retry-safe cPanel restore jobs with persisted `Idempotency-Key`
  correlation, so client retries return the original job instead of launching
  a second destructive restore. Resource and scheduled-task state now also
  roll back in memory when desired-state persistence fails before commit.

- Reconciled resource cgroup and PHP-FPM profiles during startup, closing the
  gap where a reboot could leave durable resource policy unapplied until a
  manual administrator action.

- Applied account-plan ceilings to direct administrator site-resource edits,
  preventing a site profile from bypassing the owning account's CPU, memory,
  process, or PHP-worker envelope.

- Extended per-site mutation serialization to environment, resource-profile,
  scheduled-task, and worker updates/reconciliation, preventing concurrent
  helper calls from racing over systemd, PHP, and site-level state.

- Extended the same site-operation boundary to PHP, Python, Node tooling,
  Composer, Node proxies, PHP routes, and SSH/SFTP access mutations and their
  reconciliation paths.

- Serialized Node application lifecycle, deploy-key, `.htaccess`, WordPress,
  and cPanel restore mutations with the corresponding site operation, closing
  remaining concurrent-host-mutation paths.

- Serialized managed database, Redis allocation, WordPress action, and
  malware-quarantine mutations with other operations for the same site.

- Fixed route-lock identity for `.htaccess` conversion so it coordinates with
  the actual PHP vhost filename used by route deployment.

- Prevented multiple customer accounts from claiming the same site and made
  account provisioning reject assignments without an existing site root.

- Made account-state loading fail closed when legacy or manually modified
  state assigns one site to multiple customer identities.

- Extended per-site mutation serialization to staging creation and local/off-site
  restore-to-staging, preventing concurrent requests from racing over site,
  environment, database, route, or release state.

- Added verified off-site backup restore-to-staging with fixed-object downloads,
  source-site authorization, optional selected-database import, and the same
  recovery-journaled cleanup guarantees as local staging restores.
- Added ownership-checked logical managed-database cloning to new staging sites,
  with explicit target credentials and cleanup if later staging steps fail.

- Staging creation now rolls back no-index markers, environment state, and
  newly activated routes when a later staging transaction step fails, avoiding
  orphaned policy and configuration state.

- Fixed disposable installation smoke coverage to exercise the production
  off-site-backup requirement with a local synthetic target instead of
  contradicting the installer contract.
- The installation smoke harness now installs `rclone` before readiness checks,
  covering the actual dependency required by mandatory off-site backups.

- Updated the standalone `/api/runner/build` path for the resource-limited
  runner contract and serialized it with other same-site builds, preventing
  runtime argument mismatches and artifact races.

- Serialized same-site Git and release-pipeline operations to protect the
  shared build-artifact path and release pointer, while retaining concurrency
  across different sites.

- Applied each site's CPU, memory, and PID resource profile to rootless Podman
  build containers, with conservative limits for legacy sites, and tightened
  runner path validation at the privileged boundary.

- Added `stepanel dr-check`, a secret-safe control-plane disaster-recovery
  inventory covering state files, keys, trust material, site data, external
  dependencies, and regeneration-only assets.

- Added nightly/manual disposable systemd installation smoke coverage across
  AlmaLinux 9, Rocky Linux 9, Ubuntu 24.04, and Debian 12, including real
  package installation, service restart, synthetic site creation, and
  selected-webserver validation.

- Release archives now include the production installer, privileged helpers,
  service definitions, and web assets so operators can install verified
  artifacts without building from source.

- Pinned the release GoReleaser action to an immutable commit and pinned the
  GoReleaser binary to exactly `v2.17.1` for reproducible artifact publishing.
- Declared `FEATURES.md` canonical for feature statuses and added explicit
  `main / unreleased` versus `v0.6.0` documentation version markers.

- Added a provider-neutral DNS capability contract and API while keeping the
  existing Linode mutation adapter explicit. Documented DNS desired-state and
  DNSSEC boundaries, and clarified that mail installation is optional,
  operator-managed infrastructure rather than core mailbox hosting.

- Added explicit webserver-specific WAF capability reporting. Apache reports
  ModSecurity/CRS availability, while Caddy and OpenLiteSpeed clearly report
  native WAF support as unavailable and require an external security layer.

- Automated Git rollback-release retention with count, age, and per-site byte
  limits; the current release and immediate rollback target are preserved,
  cleanup is deployment-serialized, and release storage metrics are exposed.
- The privileged Git helper now independently enforces the configured exact
  repository-host allowlist.

- Added crash-consistency metadata and optional external HMAC-SHA256 signatures
  for backup manifests, plus an authenticated `POST /api/backups/verify`
  operation that verifies backups without restoring them. Restore-to-staging
  now reports its consistency classification.

- Extended preview resource profiles with systemd CPU weight, memory high-water
  limit, and I/O weight controls, plus live status reporting. Updated the FPM
  Lens integration guidance to its current evidence-aware observe/review flow.
- Attached scheduled-task systemd services to their site resource slice so
  cron workloads receive the same cgroup boundary as managed applications.
- Added explicit task-unit ordering after the site slice and exposed scheduled
  tasks in the resource-enforcement contract.
- Added task-local `CPUQuota`, `MemoryMax`, and `TasksMax` ceilings, with
  configured site resource profiles propagated into each scheduled-task unit.
  Fixed worker creation to accept its documented retry argument and made
  environment updates restart all matching worker units reliably.
- Resource state now fails closed on invalid persisted profiles, and resource
  reconciliation no longer holds the store lock while querying systemd.
- Resource profiles can now apply opt-in per-site disk and inode ceilings via
  `setquota` when user quotas are enabled on the site filesystem; unsupported
  filesystems fail closed rather than reporting unenforced limits.
- Removing a filesystem quota profile now clears the previously applied Linux
  user quota through persisted pending state and startup/manual reconciliation.
- Built-in hosting plans now include explicit CPU, memory, process, and PHP
  worker ceilings. Account creation persists those profiles before host
  application, preserves existing site-specific profiles, and leaves failed
  applications pending reconciliation.
- SSH/SFTP access policy and public-key changes now apply through the reviewed
  root-controlled site helper, use root-owned authorized-key files, enforce
  explicit shell/SFTP modes, and reconcile failed host applications.
- Worker creation, update, and deletion now persist desired state before
  systemd mutation and retry pending changes during startup reconciliation.
- PHP-FPM runtime profiles now persist desired state before helper application,
  retain pending errors, and reconcile interrupted FPM changes at startup.
- Python application deployments now persist desired state before systemd
  application and retry pending services during startup reconciliation.
- Environment deletion now compensates a failed state write by restoring the
  previous host environment, preventing host/state divergence.
- Restore-to-staging can now provision a new managed destination database and
  import one selected verified backup dump, refusing reuse and cleaning up on
  failure.
- Plan-assigned sites now share an aggregate root-owned account cgroup slice in
  addition to their per-site slices, preventing site-count multiplication from
  bypassing the plan's application resource envelope.
- Added an administrator-only asynchronous files-only backup restore. It
  verifies the archive and optional signature before extraction, uses the site
  recovery journal for atomic replacement, preserves databases, and reports
  the backup consistency classification through the job result.
- Added an administrator-only database-only restore for existing managed
  databases. It verifies the selected dump, creates a pre-restore safety
  backup, imports through the database helper over stdin, and records that
  schema rollback remains manual.
- Added administrator-only off-site files restore through the configured
  rclone target. Retrieval is restricted to fixed backup objects for the
  requested site/backup ID before normal signature verification and recovery
  journaling.
- Added administrator-only off-site database restore using the same fixed
  object retrieval, pre-restore safety backup, ownership checks, and restricted
  database helper as local database restore.
- Backup listings now verify archive contents and configured manifest
  signatures before presenting a backup as restorable; unverifiable artifacts
  are omitted and logged for operator repair.

- Added AES-GCM encryption for customer TOTP secrets with dedicated
  `STEPANEL_ACCOUNT_KEY`, one-time MFA regeneration, and session revocation.

- Added one-time bcrypt-hashed customer MFA recovery codes with an
  administrator-only generation endpoint and session revocation.

- Added audited administrator customer-credential recovery with temporary
  password, regenerated MFA/recovery material, session revocation, and
  customer completion endpoints for password change and MFA enrollment.

- Clarified account deletion as customer-login removal rather than hosting
  termination, and made scheduled-task scripts root-owned under the control
  plane's task directory. Fixed validation to accept the standard Base64
  alphabet emitted by the API. Renamed the store operation to `RemoveLogin` to
  prevent callers from mistaking it for hosting teardown.

- Persisted scheduled-task intent before helper mutation, added pending-state
  startup/API reconciliation, and made task deletion resumable after crashes.

- Persisted desired environment values before host application and added
  startup reconciliation so interrupted environment updates can be reapplied.

- Updated operator/developer product previews and synchronized feature-status
  documentation, including resource posture, Security Center, deployment
  stages, restore-to-staging, staging protection, and release retention.
- Added a secret lifecycle and disaster-recovery runbook covering environment,
  audit, session, account, deploy-key, and off-site credentials.

- Read-only administrator Security Center endpoint aggregating existing posture
  checks, service state, disk/inode pressure, and backup schedule health.

- Optional Basic Auth protection for staging routes with bcrypt-only
  credential persistence and managed Caddy/Apache enforcement.

- Default `X-Robots-Tag: noindex, nofollow` protection for new staging routes,
  enforced by the managed Caddy and Apache vhost helpers.

- Configurable validated Git rollback-release retention, preserving the newest
  rollback target while safely pruning older StePanel-owned release trees.

- Verified files-only backup restore to a new recovery-journaled staging site;
  existing destinations and database restore remain deliberately refused.

- Live systemd cgroup CPU/memory/task counters in resource status plus a
  bounded, no-symlink site filesystem usage endpoint for quota planning.

- Observed systemd-slice resource status and an audited administrator
  reconciliation endpoint that re-applies pending or inactive desired profiles.

- Preview per-site resource profiles with persisted desired state, systemd
  CPU/memory/task enforcement for managed application and worker services, and
  validated PHP-FPM worker ceilings.

- Immediate customer panel-session revocation on suspension, actor-bound
  session records, and explicit terminology distinguishing customer login
  removal from future hosting-workload termination.

- Preview release-pipeline endpoint that connects constrained Git checkout,
  optional verified pre-activation backup, rootless artifact build, artifact
  validation, and atomic activation with preserved file rollback.

- Durable, site-scoped deployment records for sandboxed build completion and
  atomic Git activation, with commit/artifact/previous-release provenance and
  an authenticated deployment-history API.

- Per-site root-owned ED25519 Git deploy keys and restricted private SSH
  repository cloning. The panel returns only public keys and continues to
  reject passwords, tokens, arbitrary SSH users, and hosts outside the exact
  allowlist.
- Persisted per-site scheduled-task definitions backed by hardened systemd
  services/timers, site identity execution, timeouts, environment-file
  injection, audit events, and no writable host crontab.

- Rootless Podman build-runner boundary for validated site build definitions.
  Build commands execute in a capability-dropped, read-only container with only
  source and artifact mounts; the control plane retains release activation.
- Root-owned systemd environment-file rendering for encrypted per-site
  variables, with managed Node, Python, and worker service restarts after an
  audited environment update.
- Transactional staging site creation with validated destination routing,
  safe regular-file copying, isolated destination setup, recovery journals, and
  optional non-secret environment cloning. Database cloning remains fail-closed
  until the managed database helper supplies a transactional clone primitive.
- Per-site PHP-FPM runtime profiles with selected-version socket routing,
  validated memory/execution/upload/input settings, OPcache/display-error and
  error-reporting controls, atomic pool validation, and reload rollback.
- First-class Composer project inspection and dependency installation with
  site-identity execution, production/development and autoloader options, and
  persisted operation metadata.
- Node developer tooling API for bounded, audited package installation and
  production builds using npm, Yarn, or pnpm under the isolated site identity.
- Managed site worker lifecycle for Laravel, Horizon, Node, Celery, and RQ
  services with fixed commands, systemd restart policy, memory/task limits,
  audited definitions, and safe create/remove APIs.
- Bounded site log viewer API with allowlisted web/PHP/application/deployment,
  build, cron, and worker sources, filtering, download support, site access
  controls, and safe missing-log handling.
- Per-site SSH developer-access API with validated public-key fingerprints,
  SFTP/shell policy state, revocation, ownership checks, and audit events.
- Guarded WordPress developer operations through WP-CLI: status, core/plugin/
  theme updates, maintenance mode, and due cron execution, with site ownership
  checks, bounded execution, and audit events.
- Redis/Valkey site allocation API with logical database and namespace
  isolation, validated memory/eviction policy metadata, service detection, and
  audited lifecycle operations. Host-level ACL and cgroup enforcement remain
  privileged-helper work.
- Encrypted per-site environment storage with AES-GCM, masked secret reads,
  ownership checks, audited replacement/deletion, and the
  `/api/sites/environment/{site}` API. Configure `STEPANEL_ENVIRONMENT_KEY`.
- Signed, provider-neutral Git webhook deployments through
  `/api/sites/git-webhook`, using `X-StePanel-Signature` and the existing
  repository allowlist, release validation, atomic activation, and audit path.
- Shared-hosting account suspension, unsuspension, and customer-login removal
  endpoints; suspended customers cannot establish new sessions. Hosting-
  workload termination remains a separate planned workflow.
- Shared-hosting beta with administrator-provisioned customer accounts,
  independent bcrypt credentials and mandatory per-customer TOTP, persisted
  account state, `starter`/`professional`/`agency` assignment limits, and
  customer-scoped site, backup, and job access.
- Administrator-only account API and a documented shared-hosting operating
  contract that distinguishes enforced assigned-site limits from future
  resource quotas and customer lifecycle features.
- Customer workspace visual refresh with a role-aware welcome panel, plan and
  assigned-site summary, simplified navigation, and improved responsive cards.
- Professional workspace design layer with an accessible visual token system,
  sticky navigation, clearer task and form hierarchy, responsive small-screen
  layouts, and an in-product resource footer.

- Production-readiness doctor checks for mandatory launch MFA, enforced
  offsite-backup policy, transport-security boundary, and audit persistence.
- Production configuration now refuses startup unless administrator TOTP MFA
  and an enforced offsite-backup target are configured.
- Readiness now fails when a required offsite target or its `rclone` dependency
  is unavailable; backup inventory validates archive metadata and supports
  bounded, site-filtered responses.
- Caddy `.htaccess` migration through the dashboard, API, and
  `stepanel convert-htaccess` CLI. Common front-controller and redirect rules
  are translated, unsupported lines are reported, and partial application
  requires explicit operator acceptance.
- Root-owned Caddy PHP-site lifecycle with isolated PHP-FPM sockets, atomic
  configuration replacement, full-Caddyfile validation, automatic HTTPS, and
  rollback on failed reload.
- Site-centric developer workspace inventory at `/api/sites/overview`, grouping
  document roots, domains, Node applications, and managed database counts.
- Dashboard managed-sites cards backed by the same authenticated API.
- Verified, bounded, filterable audit-event history at `/api/audit/events`.
- Constrained HTTPS Git site releases with validated refs, shallow checkout,
  atomic activation, previous-release preservation, and audit records.
- In-dashboard site workspace details covering domains, applications, proxies,
  databases, and verified recent activity.
- A customer-first dashboard landing view with managed-site, connected-domain,
  and verified-backup counts; guided migration, domain, and deployment tasks;
  and per-site domain connection and verified-backup actions.
- Read-only per-database detail and atomic one-click Git rollback that preserves
  the replaced release for recovery.
- Release metadata validation covering the Go version, Helm chart, OpenAPI
  document, and changelog before CI and tagged-release publication.

### Changed

- The dashboard now opens as a hosting workspace rather than an infrastructure
  cockpit. Server service inventory remains available in the clearly labelled
  administrator operations section.
- Caddy is now the runtime and installer default; Apache and OpenLiteSpeed
  remain explicit options. Documentation and the dashboard now describe
  Caddy's automatic certificate lifecycle.
- Node application lifecycle changes are serialized, systemd unit replacement
  is atomic and rollback-aware, and rollback manifests are validated before
  they can drive the privileged helper.
- CI now syntax-checks and shell-lints every bundled integration helper,
  including the Caddy and OpenLiteSpeed paths.
- Reconciled installation, architecture, threat-model, production-readiness,
  Node, database, operations, demo, roadmap, API, and release documentation with the current
  site workspace and constrained Git deployment behavior.
- Updated the deterministic dashboard SVG/PNG preview to include managed sites
  and clearly identify it as a development preview after version 0.6.0.
- Privileged helper calls now use bounded contexts/output, and shutdown cancels
  backup scheduling and cleanup loops before waiting for jobs.
- Cloud inventory preserves partial results with warnings, bounds provider
  output, and removes panel-specific secrets from cloud CLI environments.
- Audit-event filtering verifies and selects recent matches in one pass; backup
  inventory skips isolated corrupt artifacts instead of hiding healthy backups.

### Fixed

- Dashboard Node deployment now sends the strict `node_version` application
  field and excludes proxy-only fields, allowing browser deployments to reach
  the app and proxy helpers successfully.
- Caddy and OpenLiteSpeed proxy deployments now accept the canonical
  `host:port` backend emitted by the API, validate private addresses and port
  ranges consistently, and attempt to reactivate the prior configuration when
  a reload fails. OpenLiteSpeed now renders the required `http://` scheme.
- Backend URLs with invalid ports are rejected, and accepted localhost/IP
  values are canonicalized before crossing the privileged-helper boundary.
- Audit, job, and session state files cannot be configured to the same path;
  database credential files are verified as readable regular files at startup.
- Failed backup-schedule writes now restore the prior in-memory state instead
  of exposing changes that were never durably committed.
- Node app deployment now commits a final running manifest before activation
  and restores the previous manifest when its systemd helper fails.
- Site detail responses now include routes, proxies, and applications, including
  resources owned by site names containing hyphens.
- Git releases now enforce an exact host allowlist, disable interactive Git
  credentials, reject symlink/device payloads and oversized trees, remove
  repository metadata before activation, and serialize release switching.
- Malformed recovery journals are quarantined while valid transactions
  continue recovering; site rollback is withheld when database cleanup is
  incomplete.
- cPanel and WordPress uploads are synced before durable restore jobs are
  accepted, reducing the risk of queued jobs referencing truncated staging
  files.

## [0.6.0] - 2026-09-04

### Added

- Native local MySQL, MariaDB, and PostgreSQL inventory with site ownership,
  allocated size, user, and encoding metadata.
- Least-privilege database/user provisioning, password rotation, and guarded
  deletion through the restricted root helper and authenticated dashboard.
- Mandatory checksummed logical safety dumps before managed database deletion.
- Read-only database health diagnostics, effective-setting inspection, and
  query-text-free session inventory with explicitly confirmed, audited session
  termination.
- Prometheus database pressure metrics and scheduled-backup RPO/failure metrics.
- PostgreSQL logical dumps for managed site backups.
- Per-schedule local retention, last-success, duration, error, and consecutive
  failure state.

### Changed

- Installer-managed remote database passwords are delivered as private systemd
  credentials instead of being retained in the daemon environment.
- PostgreSQL local installs now receive the same restricted lifecycle and
  backup helper boundary as MySQL and MariaDB.
- PITR, auto-tuning, replication orchestration, and automatic failover are
  explicitly reported as unavailable rather than represented by unsafe or
  misleading controls.

## [0.5.0] - 2026-09-04

### Added

- PostgreSQL installation support with optional default package installation on
  Debian/Ubuntu and validated PostgreSQL AppStream stream selection on
  RHEL-family systems.
- PHP PostgreSQL support through the distribution `php-pgsql` package.
- Optional phpMyAdmin installation for MySQL/MariaDB and phpPgAdmin installation
  for PostgreSQL through `STEPANEL_INSTALL_DB_ADMIN=1`.
- Authenticated `/api/database` status reporting and a dashboard database
  operations panel showing engine, version, host, service state, client, and
  the matching administration UI link.

### Changed

- Database service discovery and operator diagnostics now identify the selected
  PostgreSQL service instead of assuming MySQL/MariaDB.
- Database administration URLs can be customized with
  `STEPANEL_DB_ADMIN_URL`; Apache package integrations are linked automatically,
  while Caddy and OpenLiteSpeed require an explicitly reviewed PHP route.
- Synchronized release metadata to version `0.5.0` across Go, Helm, OpenAPI,
  and release documentation.

### Fixed

- Service inventory now reports only the configured web/database stack and
  detected optional services, eliminating false production alerts for engines
  that were intentionally not installed.
- Versioned PHP-FPM and PostgreSQL systemd units are detected without exposing
  raw system-bus errors as service states.
- PostgreSQL remote credential checks now use non-interactive `psql`; cPanel
  and WordPress MySQL restore controls are clearly disabled in PostgreSQL mode.
- Database-admin URLs reject scheme-relative, traversal, query, fragment, and
  malformed paths. Installer-managed Apache routes are IP-restricted to
  loopback by default through `STEPANEL_DB_ADMIN_ALLOW` and scoped to the panel
  virtual host rather than every hosted domain.
- The database operations card now collapses correctly on narrow screens, and
  admin-console readiness requires a valid Apache configuration target.

## [0.4.0] - 2026-09-04

### Added

- Linode snapshot listing and asynchronous deletion with strict snapshot ID
  validation and audit events.

- Asynchronous Linode load-balancer backend management with strict address,
  port, weight, and resource validation.

- Asynchronous Linode DNS record management with strict domain, record, target,
  and TTL validation.

- Asynchronous, allowlisted SSH actions for configured infrastructure servers,
  including service restarts and host reboots with strict host-key checking,
  bounded timeouts, persisted jobs, and audit events.

- Strict-host-key, read-only SSH server health inventory at `/api/ssh`, with
  bounded connectivity checks for configured infrastructure aliases.

- Cloud lifecycle actions now run as persisted asynchronous jobs with bounded
  provider timeouts, failure audits, and job-status polling.

- Authenticated cloud actions for configured Linode, AWS, and OpenStack
  providers, including start, stop, reboot, and snapshot operations with
  strict resource validation and audit events.

- Read-only cloud inventory integration for Linode, AWS, and OpenStack,
  covering servers, DNS, load balancers, and snapshots through standard
  provider credentials and CLIs.

- Durable scheduled site backups with interval validation, persisted next-run
  state, the `/api/backup-schedules` API, and execution through the existing
  audited backup job pipeline.

- OpenLiteSpeed can now be selected as the installed webserver with
  `STEPANEL_WEBSERVER=openlitespeed`; Caddy is also available with the same
  installer option, service inventory recognizes `lsws` and `caddy`, and Apache
  remains the default. OpenLiteSpeed and Caddy receive dedicated proxy helpers
  with backend validation, atomic updates, configuration checks, and rollback
  on failed reload/restart.
- Persistent, server-revocable administrator sessions, password-rotation
  invalidation, request correlation IDs, a recent-jobs API/dashboard, runtime
  capability reporting, and HTTP response-class metrics.
- Authenticated administration with bcrypt password hashes, signed sessions,
  login throttling, audit logging, and protected metrics.
- Asynchronous cpmove and WordPress restore workflows with archive inspection,
  capacity checks, progress reporting, retention controls, and database import.
- Managed Node.js applications with NVM version selection, hardened systemd
  units, rollback-aware deployment, and Apache reverse-proxy lifecycle controls.
- Optional Certbot, Fail2ban, ModSecurity/OWASP CRS, FPM Lens, ClamAV malware
  quarantine, Exim/Dovecot/SpamAssassin, and FTPS integrations.
- Service-state visibility, certificate management, observability assets,
  operations runbooks, an OpenAPI specification, and end-to-end lab tooling.
- Docker, Kubernetes, Helm, and Terraform deployment assets, including pinned
  images/providers, persistent storage, health probes, and ingress support.
- Debian/Ubuntu and RHEL-family Apache configurations plus audit-log rotation.
- Transactional PHP site vhosts that route domains to active per-site PHP-FPM
  sockets and reject conflicts with existing sites or Node proxies.
- Private site backup jobs with optional ownership-scoped database dumps,
  per-entry and whole-archive SHA-256 manifests, atomic publication, and a
  repeatable offline verification command.
- Separate dependency-free liveness and persistent-storage readiness endpoints,
  plus configurable upload, archive-entry, and global job concurrency limits.
- Optional TOTP administrator MFA with accepted-code replay protection, plus
  actor/target-aware, sequence-linked HMAC audit records and offline verification.

### Changed

- The dashboard now renders only live server, security, capability, and job
  data; simulated activity and inert controls were removed. Assets are embedded
  in the binary, external fonts were removed, and keyboard, reduced-motion,
  form-label, loading, and unavailable-feature states were improved.
- API handler errors use a consistent JSON envelope, production validates every
  managed path and privileged executable path, and unauthenticated readiness
  responses omit internal filesystem details.
- The installer now requires a panel FQDN and a 12-character administrator
  password, stores only its bcrypt hash, generates a session secret, validates
  options before host mutations, and writes configuration atomically. In-place
  upgrades preserve the existing root-owned runtime values, snapshot all
  StePanel-owned files, validate Apache and the candidate health endpoint, and
  restore the previous files and service state on core installation failure.
- The installer now generates and preserves a dedicated root-only audit HMAC
  key, refuses unsafe in-place key replacement, and deployment manifests accept
  the corresponding secret plus optional TOTP enrollment material.
- Local installations use a root-owned, operation-scoped database helper; the
  long-running control plane retains no local administrative credential.
- Newly installed mail and FTP daemons remain disabled until explicitly
  activated; FTPS activation requires readable certificate and key paths.
- Long-running restore jobs are allowed to finish during graceful shutdown,
  with matching systemd stop timeouts and per-target concurrency protection.
- Restore and certificate job records are persisted atomically; uncleanly
  interrupted work is reconciled into a visible failed state at startup.
- Site overwrites now use restart-safe transaction journals on the destination
  filesystem, retaining previous and partially restored document roots for the
  configured recovery window and rolling back uncommitted work at startup.
- Host restores now provision deterministic per-site Unix identities, private
  PHP-FPM pools, isolated Node service users, and explicit control-plane ACLs;
  site workloads are not members of Apache's shared filesystem group and PHP
  temporary files remain inside the site state directory.
- Local database restores now stream through a root-owned helper using an
  importer restricted to the one new schema. Root-only pending-operation
  records and site transaction journals enable startup cleanup after interrupted
  database provisioning or a later restore crash.
- Container and orchestration deployments now use numeric non-root identity
  `10001`, read-only root filesystems, dropped capabilities, bounded resources,
  persistent writable paths, and disabled service-account token mounting.
- The project now targets Go 1.26 and uses pinned CI and vulnerability-scanner
  versions.

### Fixed

- Added a single-instance process lock, bounded admission for expensive scan
  and inspection endpoints, stronger destination symlink checks, and HSTS in
  production mode.
- Startup now acquires a process lock to prevent multiple panel instances from
  concurrently mutating the same local job, recovery, and helper state.
- Expensive malware scans and cpmove inspections have bounded concurrent
  admission, and restore copies reject symlinked destination parents.
- Startup cleanup failures are now logged instead of silently discarded, and
  WordPress URL/cache/rewrite update failures abort the restore transaction.
- Production responses now include HSTS, while restore metadata and active
  plugin/theme configuration continue to be applied only after a malware scan.
- WordPress WPress restores now read validated `package.json` metadata, restore
  the archived active plugin/theme/stylesheet selections, and decode the
  archive's base64 `.htaccess` payload into the restored document root.
- Malformed WPress multipart requests no longer panic when upload metadata is
  missing, and cpmove inspection now enforces per-entry and total decompressed
  size limits to resist archive-bomb denial of service.
- Restore file copies now open source and destination files without following
  symlinks, close descriptors on failure, and write Node version metadata
  atomically.
- Job admission is synchronized with shutdown so persistent jobs cannot race
  `Wait`/process termination, while malware scans are bounded to one active
  filesystem scan.
- Privileged site, proxy, application, certificate, restore, backup, and
  malware operations now surface audit persistence failures instead of silently
  discarding the audit event.
- Restores now refuse pre-existing database or database-user names and remove
  newly created databases, users, and staged files when later steps fail.
- Failed SQL imports drop partially imported databases instead of leaving
  inconsistent state, and orphaned upload archives are included in retention.
- Concurrent cpmove and WordPress jobs can no longer restore into the same site.
- Backups share the per-site job lock with restores, preventing an internally
  initiated backup from racing a site replacement.
- Restore admission now checks free space on both staging and destination
  filesystems instead of silently continuing when a capacity check fails.
- Authenticated mutating requests now fail closed before handler execution when
  their audit preflight event cannot be durably persisted.
- Audit persistence failures remain visible in readiness until restart, while
  signed chain-state metadata detects unauthorized tail-pointer rewrites and
  safely anchors event sequences across log rotation.
- Apache proxy snippets are rendered with valid HTTP backend URLs, tested before
  reload, and rolled back when validation or reload fails.
- PHP site vhosts and Node proxies share an Apache configuration lock and reject
  duplicate managed or pre-existing `ServerName` assignments; certificate
  issuance participates in the same lock.
- Fresh RHEL-family installation now selects the correct Apache group and
  installs an appropriate virtual-host configuration.
- Partial mail-stack installations preserve existing daemons while keeping only
  newly installed companion services disabled by default.

### Security

- Apache proxy files and systemd units are root-owned and can only be changed
  through narrowly validated sudo helpers; the unprivileged daemon no longer
  controls Apache-included configuration directly.
- The control-plane process no longer retains global database credentials for
  local installations; destructive cleanup is limited to databases registered
  by the root-owned restore helper.
- systemd services now apply strict filesystem protection, namespace and kernel
  restrictions, empty capability sets where applicable, file-descriptor/task
  limits, private temporary directories, and restrictive umasks.
- Production responses include CSP, permissions, framing, MIME-sniffing,
  referrer, and cache-control headers; login request bodies are size-limited.
- Restore destinations, proxy backends, helper arguments, credentials, archive
  contents, and minimum free-space requirements receive stricter validation.
- Secure cookies remain mandatory in production, and installation guidance now
  requires TLS termination before first sign-in.

## [0.3.0] - 2026-09-04

### Added

- OpenLiteSpeed can be selected as the installed webserver with
  `STEPANEL_WEBSERVER=openlitespeed`; service inventory recognizes `lsws` and
  the installer preserves Apache as the default.

### Fixed

- App deployment now validates and normalizes domains at the API boundary,
  preventing malformed hostnames and audit-log injection through direct API use.
- Password-based session fingerprints remain stable across restarts without
  retaining the plaintext password in the in-memory authentication state.
- Session admission is bounded with expiry-based eviction, validation uses a
  read lock, HTTP metrics record implicit 200 responses correctly, and cPanel
  restore staging IDs are collision-resistant.

### Documentation

- Added the production-readiness wiki covering architecture, operations,
  security boundaries, backup/restore procedures, deployment, observability,
  and known limitations.

## [0.1.0] - 2026-08-22

The first documented foundation release: authenticated dashboard, LAMP installer, database engine/version selection, cpmove staging and restore, health endpoints, and deployment tooling.

### Known limitations

- Authentication, authorization, and TLS termination are not included yet.
- The installer expects a pre-built `stepanel` binary.
- Database restoration requires the local `mysql` client and root/socket access.
