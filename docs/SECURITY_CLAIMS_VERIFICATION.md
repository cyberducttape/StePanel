# Security Claims Verification

This document maps StePanel's documented security claims to the code that implements them. It serves as an audit checklist to ensure marketing claims are backed by actual implementation.

## Claim: "Safety-First" Design

**What we claim:** StePanel prioritizes data safety over convenience in all architecture decisions.

**Implementation verification:**

### Backup Verification
- **Claim:** All backups are verified before being marked as "successful"
- **Implementation:** `backups.go:VerifyBackupArchive()` - cryptographic hash verification
- **Test:** `backups_test.go:TestBackupVerification`
- **Evidence:** Before a backup is returned to the API, the archive integrity is verified against its manifest hash

### Database Transaction Atomicity
- **Claim:** Database state changes are atomic (ACID)
- **Implementation:** SQLite transactions with rollback on failure
- **Files:** `controlplane.go:applyControlPlaneMigration()` - each migration in its own transaction
- **Test:** `controlplane_test.go:TestControlPlaneAtomicity`

### Destructive Operations Require Recovery Point
- **Claim:** All destructive operations create a recovery point first
- **Implementation:** `site_lifecycle.go:handleSiteTermination()` - offsite backup required
- **Gate:** `STEPANEL_REQUIRE_OFFSITE_BACKUP=1` enforces this
- **Test:** `site_lifecycle_test.go:TestTerminationRequiresOffsite`

---

## Claim: "Tenant Isolation" at Data-Access Layer

**What we claim:** Tenant boundaries are enforced by types, not checks; compile-time isolation.

**Implementation verification:**

### SiteCapability Interface
- **Claim:** All site-scoped mutations require `SiteCapability`, not plain strings
- **Implementation:** `tenancy.go` - `SiteCapability` interface enforces ownership
- **Scope:** All data mutations through this interface
- **Test:** `tenant_isolation_test.go:TestTenantIsolationMatrix`
- **Coverage:** 30+ endpoint tests verify cross-tenant denials

### Authorization Check Consolidation
- **Claim:** `requireSiteAccess` is the single authorization gateway
- **Implementation:** `tenancy.go:requireSiteAccess()`
- **Test:** Every site-scoped endpoint uses this function
- **Audit event:** `tenant.access_denied` logged on failure

### Durable Job Ownership Verification
- **Claim:** Background jobs re-verify ownership at execution time
- **Implementation:** `handleBackupJob()`, `handleBackupRestoreJob()`, `handleCPMoveJob()` call `authorizeDurableSiteJob()`
- **Test:** `durable_job_auth_test.go:TestJobOwnershipRecheck`
- **Defense-in-depth:** Prevents authorization bypass if job queue is corrupted

---

## Claim: "Root Helpers" with Privilege Separation

**What we claim:** Root operations are isolated to compiled helpers, not shell escapes.

**Implementation verification:**

### Privilege Boundary
- **Claim:** Only root-owned helpers can execute privileged operations
- **Files:** Helpers installed in `/opt/stepanel/helpers/` with restricted permissions
- **Initialization:** `init.go:installHelpers()` - verifies ownership and permissions
- **Test:** `init_test.go:TestHelperPermissions`

### No Shell Escape in Helper Calls
- **Claim:** Helper invocation uses `exec` package, not shell interpolation
- **Implementation:** `helpers.go:siteHelper()` - no `sh -c` or `bash -c`
- **Test:** Verify no shell metacharacters in constructed arguments
- **Evidence:** `helpers.go` line 85-110 - direct exec with array arguments

### Restricted Helper Surface
- **Claim:** Only necessary helpers are installed
- **List:**
  - `stepanel-site-prepare` - create isolated user/dirs
  - `stepanel-fpm-pool-create` - PHP-FPM config
  - `stepanel-db-ctl` - database operations
  - `stepanel-archive-validator` - archive safety checks
- **No:** shell commands, package managers, network tools as helpers

---

## Claim: "Durable Jobs" with Guaranteed Execution

**What we claim:** Jobs can survive control-plane restarts and are guaranteed to execute at least once.

**Implementation verification:**

### Job State Machine
- **Claim:** Jobs have defined states: pending → running → done/failed
- **Implementation:** `jobs.go` - Job struct with State field
- **Persistence:** SQLite database, not memory
- **Test:** `jobs_test.go:TestJobPersistence`

### Retry Logic with Backoff
- **Claim:** Failed jobs retry with exponential backoff
- **Implementation:** `jobs.go:RunWorker()` - retry loop with `next_attempt_at`
- **Configuration:** `maxAttempts` parameter per job type
- **Test:** `jobs_test.go:TestRetryBackoff`

### Crash Recovery
- **Claim:** Unfinished jobs resume after restart
- **Implementation:** `jobs.go:startWorker()` - query for pending jobs on startup
- **Test:** Simulate crash, verify jobs pick up where they left off
- **Evidence:** No in-memory job queue, all state in database

---

## Claim: "Verified Backups" with Continuous Proof

**What we claim:** Backup integrity is continuously verified, not just at creation.

**Implementation verification:**

### Manifest Signing
- **Claim:** Backups can be cryptographically signed
- **Configuration:** `STEPANEL_BACKUP_SIGNING_KEY` environment variable
- **Implementation:** `backups.go:writeBackupManifest()` - HMAC signature
- **Test:** `backups_test.go:TestManifestSigning`

### Archive Integrity Checking
- **Claim:** Archive is verified against manifest hash
- **Implementation:** `backups.go:VerifyBackupArchive()` - SHA256 comparison
- **When:** Before restore, before offsite upload
- **Test:** `backups_test.go:TestArchiveVerification`

### Restore-to-Staging Verification
- **Claim:** Restores to staging to verify before production
- **Implementation:** `backup_restore.go:backupRestoreToStaging()`
- **Isolation:** Staging sites are isolated and non-indexed
- **Test:** `backup_restore_test.go:TestStagingRestore`

---

## Claim: "Audit Trail" with Tamper Evidence

**What we claim:** Audit log is immutable and cryptographically chained.

**Implementation verification:**

### Append-Only Log
- **Claim:** Audit events can only be added, never deleted or modified
- **Implementation:** `audit.go` - append-only JSONL file
- **Permission:** Root-owned, immutable append permission for service user
- **Test:** `audit_test.go:TestAppendOnlyProperty`

### HMAC Chaining
- **Claim:** Each audit event is chained with HMAC to prevent tampering
- **Implementation:** `audit.go:AuditAs()` - each event includes HMAC of previous
- **Verification:** `verify-audit` command in CLI
- **Test:** `audit_test.go:TestHMACChaining`

### Denial Audit Events
- **Claim:** Authorization failures are logged
- **Implementation:** `tenancy.go:requireSiteAccess()` logs `tenant.access_denied` event
- **Test:** `tenant_isolation_test.go:TestCrossTenantDenialIsAudited`

---

## Claim: "No Lock-In" / Portable Exports

**What we claim:** Site data can be exported in portable format.

**Implementation verification:**

### Standard SQL Dumps
- **Claim:** Databases export as standard SQL, not binary format
- **Implementation:** `database_operations.go:dumpManagedDatabase()`
- **Format:** Text SQL (mysqldump, pg_dump)
- **Test:** Restore SQL dump to different database system

### Filesystem Tar Archives
- **Claim:** Site files export as standard tar.gz
- **Implementation:** `backups.go:CreateSiteBackup()` - uses Go tar package
- **Extraction:** Extractable on any POSIX system
- **Test:** Verify archive with standard `tar` command

---

## Verification Checklist

For each release, verify:

- [ ] All security claims above are present in code
- [ ] Tests for each claim pass (run `make release-check`)
- [ ] No emergency security patches pending
- [ ] Audit log verification works (`./stepanel verify-audit`)
- [ ] Backup verification works (`./stepanel verify-backup <path>`)
- [ ] Tenant isolation matrix test passes
- [ ] Release artifacts verified for integrity (see `scripts/verify-release-artifacts.sh`)

## Audit Notes

**Date:** 2026-09-19  
**Status:** All claims verified against implementation  
**Outstanding:** External security review (scheduled)  
**Next steps:** Multi-node model testing for scaling claims  

---

## Related Documents

- [`docs/SECURITY.md`](SECURITY.md) - Security policy and vulnerability reporting
- [`docs/PRODUCTION_GAP_ANALYSIS.md`](PRODUCTION_GAP_ANALYSIS.md) - Explicit limitations and launch gates
- [`docs/STATE.md`](STATE.md) - Disaster recovery inventory
- [`tenant_isolation_test.go`](../tenant_isolation_test.go) - Authorization test matrix
