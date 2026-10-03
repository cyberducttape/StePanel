# Security Claims Verification

This document maps StePanel's security claims to the code that implements them
and the tests that prove them. Every function and test named here exists on
`main`; a claim without a dedicated test says so instead of pointing at one.

Status vocabulary: **Verified**: implemented and covered by named tests.
**Partial**: implemented, but the claim is narrower than it sounds or test
coverage is incomplete. Read the boundary note before repeating the claim.

Last verified: 2026-10-02 against `main`.

## Backups

### Backups are verified before they are trusted (Verified)
- **Implementation:** `backups.go`: `CreateSiteBackup()` publishes a manifest
  with per-entry and archive SHA-256 values; `VerifyBackupArchive()` and
  `VerifySiteBackupStrict()` re-check them before restore, rehearsal, and
  offsite restore.
- **Tests:** `backups_test.go`: `TestCreateSiteBackupPublishesVerifiedManifest`,
  `TestStrictBackupVerificationBypassesListingCache`.

### Backup manifests can be signed (Verified)
- **Configuration:** `STEPANEL_BACKUP_SIGNING_KEY`.
- **Implementation:** `backups.go`: `writeBackupManifest()` (HMAC signature),
  `verifyBackupManifestSignature()`.
- **Tests:** `backups_test.go`: `TestSignedBackupManifestRequiresValidExternalKey`,
  `TestBackupManifestSignatureDoesNotFollowSymlink`.

### Backup archives are encrypted (Verified)
- **Configuration:** `STEPANEL_BACKUP_ENCRYPTION_KEY`.
- **Implementation:** `backup_encryption.go`: versioned envelope
  (`AES-256-GCM-HKDF-STREAM-v2`). Each backup derives its own subkey with
  HKDF-SHA256 from the configured key and a random 32-byte salt; chunk nonces
  are the chunk index plus a final-chunk flag, unique under that subkey; the
  header (format, key id, salt, chunk size) is authenticated as associated
  data. Truncation, reordering, appended data, and header edits are rejected.
  The manifest records the key id, never the key, and retired keys in
  `STEPANEL_BACKUP_ENCRYPTION_PREVIOUS_KEYS` still decrypt older backups.
- **Tests:** `backup_encryption_test.go`: `TestBackupEncryptionRejectsTampering`,
  `TestBackupEncryptionKeyRotation`, `TestBackupEncryptionReadsVersionOneArchives`;
  `backups_test.go`: `TestCreateSiteBackupEncryptsArchivePayload`.
- **Boundary:** backups written before this release use the v1 format
  (random base nonce, no final-chunk marker), which is still read but cannot
  detect truncation at a chunk boundary; the archive SHA-256 in the signed
  manifest still detects it when a signing key is configured. A key
  compromise exposes every backup encrypted under that key; rotation protects
  only backups made afterwards.

### Backups are proven restorable, not only written (Partial)
- **Implementation:** `backup_restore.go`: `handleBackupRehearsalJob()` verifies,
  decrypts, and extracts a backup into a disposable directory and checks every
  database dump; `internal/recovery` records each result, and
  `GET /api/sites/recovery/{site}` reports per-site recovery confidence.
- **Tests:** `recovery_status_test.go`: `TestPassingRehearsalIsRecordedAndVerifiesRecovery`,
  `TestFailingRehearsalIsRecordedAndReported`,
  `TestFailedRehearsalOverridesEarlierPass`; `internal/recovery`:
  `TestAssessRules`.
- **Boundary:** a rehearsal proves the archive level only. It does not import
  databases or start the application; the status says so explicitly.

### Restore-to-staging leaves production untouched (Verified)
- **Implementation:** `backup_restore.go`: `backupRestoreToStaging()` restores
  into manager-owned staging and activates it on a separate, non-indexed
  staging site with its own route; a failed restore tears the staging site's
  identity and directory back down.
- **Tests:** `restore_to_staging_test.go`:
  `TestRestoreToStagingPublishesIsolatedCopy`,
  `TestRestoreToStagingRouteFailureLeavesNoHalfSite`;
  `backup_restore_validation_test.go`:
  `TestRestoreDatabaseIntoStagingContextHonorsCancellation`.

### Termination keeps a recovery point (Verified)
- **Implementation:** `site_lifecycle.go`: `handleSiteTermination()` takes a
  verified backup first; `ensureTerminationOffsiteBackup()` blocks termination
  when `STEPANEL_REQUIRE_OFFSITE_BACKUP=1` and the offsite upload fails.
- **Tests:** `site_lifecycle_test.go`:
  `TestEnsureTerminationOffsiteBackupBlocksOnUploadFailure`,
  `TestSiteTerminationFailureInjectionStopsBeforeDestructiveWork`.

## Tenant boundaries

### Customers cannot operate other customers' sites (Verified)
- **Implementation:** `tenancy.go`: `requireSiteAccess()` returns a
  `SiteCapability` used by site-scoped handlers; denials record a
  `tenant.access_denied` audit event.
- **Tests:** `tenant_isolation_test.go`: `TestTenantIsolationMatrix` (18
  cross-tenant attempts covering site overview, recovery status, deployments,
  Git deploy, environment, SSH access, deploy keys, backups, databases, domain
  claims, workers, tasks, logs, disk usage, resource profiles, Composer, PHP
  runtime, and Redis) and `TestCrossTenantDenialIsAudited`.
- **Boundary:** this is control-plane authorization. It is not a
  hostile-workload boundary: sites share one kernel and network (see
  [`SECURITY.md`](../SECURITY.md#what-tenant-isolation-does-and-does-not-mean)).
  Handlers added later are covered only if they are added to the matrix.

### Background jobs re-check ownership when they run (Verified)
- **Implementation:** `jobs.go`: `authorizeDurableSiteJob()`, called by backup,
  restore, rehearsal, cpmove, and termination job handlers.
- **Test:** `durable_job_auth_test.go`:
  `TestAuthorizeDurableSiteJobRechecksSuspensionAndOwnership`.

## Privileged execution

### Privileged work crosses a typed, validated boundary (Partial)
- **Implementation:** the panel runs unprivileged; production installs send
  privileged requests to the root broker (`cmd/stepanel-root`,
  `internal/rootbroker`) over a peer-authorized Unix socket. The broker
  validates every request (`validator.go`) and runs root-owned helper scripts
  in `deploy/integrations/stepanel-*` with argument vectors, never a shell
  command string. Generic helper requests are checked against a per-action
  schema (`helper_schema.go`).
- **Tests:** `internal/rootbroker`: `TestValidateHelperRequestAllowlist`,
  `TestHelperSchemaRejectsOutOfContractArguments`,
  `TestHelperSchemaAcceptsWellFormedRequests`.
- **Boundary:** the helpers are Bash scripts, not compiled code, and some
  operations still use the schema-validated generic helper request rather than
  a dedicated typed request. In Go code, the only `sh -c` runs a fixed,
  input-free inventory command on remote hosts over SSH (`ssh_inventory.go`);
  where helper scripts use `sh -c` (`stepanel-appctl` running Node and
  Composer tools as the site user), values are passed as positional
  arguments, never interpolated into the command string.

## Durable jobs

### Jobs survive restarts (Verified)
- **Implementation:** `jobs.go`: SQLite-backed job rows; `ClaimNext()` leases
  work and `RunWorkerPool()` processes it; jobs interrupted by a crash are
  reconciled when the store is opened.
- **Tests:** `jobs_test.go`: `TestDurableJobsPersistAndReconcileRunningWork`,
  `TestOpenJobsReconcilesInterruptedWork`,
  `TestDurableClaimTransitionRestoresMemoryOnPersistenceFailure`.
- **Boundary:** delivery is at-least-once for interrupted jobs, so job
  handlers must be idempotent; restore-class jobs are failed rather than
  re-run after an unclean shutdown.

### Failed jobs retry and then dead-letter (Verified)
- **Implementation:** `jobs.go`: per-job `maxAttempts` with `next_attempt_at`
  backoff, then a dead-letter state surfaced by operational health.
- **Tests:** `jobs_test.go`: `TestDurableQueueClaimRetryAndDeadLetter`;
  `health_test.go`: `TestOperationalHealthReportsDurableDeadLetters`.

### Job cleanup never diverges from the database (Verified)
- **Implementation:** `jobs.go`: `Cleanup()` deletes durable rows first and
  restores memory if the delete fails.
- **Tests:** `jobs_cleanup_test.go` (BEGIN, DELETE, COMMIT, `SQLITE_BUSY`, and
  file-store failures).

### Schema migrations are transactional (Verified)
- **Implementation:** `controlplane.go`: `runControlPlaneMigrations()` applies
  each migration in its own transaction and refuses newer schema versions.
- **Tests:** `controlplane_test.go`: `TestControlPlaneMigrationsRejectNewerSchemaVersion`,
  `TestControlPlaneMigrationsSerializeConcurrentOpen`.

## Audit trail

### Audit events are chained and tamper-evident (Verified)
- **Implementation:** `internal/audit/logger.go`: HMAC-chained JSONL events;
  `stepanel verify-audit <log>` verifies a chain offline.
- **Tests:** `audit_test.go`: `TestAuditChainRecordsActorAndVerifies`,
  `TestVerifyAuditLogRejectsTampering`, `TestAuditChainContinuesAcrossRotation`;
  `internal/audit`: `TestLoggerRejectsTamperedLogAndMissingIdentity`.
- **Boundary:** tamper evidence, not immutability: a root user can still
  delete the file, which the chain detects but cannot prevent. Remote audit
  anchoring is planned.

## Outbound requests

### Server-originated requests reach public addresses only (Verified)
- **Implementation:** `internal/safehttp`: the address check runs in the
  dialer on the connected IP (defeating DNS rebinding), redirects are
  revalidated, and environment proxies are ignored. Used by archive imports
  and task completion webhooks.
- **Tests:** `internal/safehttp`: `TestClientRefusesLoopbackAtConnectTime`,
  `TestClientRevalidatesRedirects`; `task_webhook_test.go`:
  `TestTaskWebhookEnforcesPolicyAtConnectTime`.
- **Boundary:** tenant code itself (tasks, PHP, workers) can still reach
  loopback and private networks; task units only deny cloud metadata ranges.

## Portability

### Site data exports in standard formats (Partial)
- **Implementation:** `backups.go`: `CreateSiteBackup()` writes a tar archive;
  `dumpManagedDatabaseContext()` produces text SQL with the engine's own dump
  tool.
- **Tests:** `backups_test.go`: `TestCreateSiteBackupIncludesManagedDatabaseDump`.
- **Boundary:** encrypted archives need StePanel (or the documented format and
  key) to decrypt before standard `tar` can read them.

## Release checklist

For each release, confirm:

- [ ] Every function and test named above still exists (the documentation
      audit script checks names automatically).
- [ ] `go test ./...` passes, including the tests named above.
- [ ] `stepanel verify-audit` and `stepanel verify-backup` succeed on the
      release candidate.
- [ ] Release artifacts verify with `scripts/verify-release-artifacts.sh`.

An external security review has not yet been performed.

## Related Documents

- [`SECURITY.md`](../SECURITY.md): security policy and what tenant isolation covers
- [`docs/SECURITY.md`](SECURITY.md): security features
- [`docs/STATE.md`](STATE.md): disaster recovery inventory
- [`tenant_isolation_test.go`](../tenant_isolation_test.go): authorization test matrix
