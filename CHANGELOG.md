# Changelog

All notable changes to StePanel are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

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
  [`docs/PACKAGE_EXTRACTION_PLAN.md`](docs/PACKAGE_EXTRACTION_PLAN.md).

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
