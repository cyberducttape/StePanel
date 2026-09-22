# Security Gaps: Claim vs. Enforcement Mismatches

## Issue 1: Archive Importer SSRF - DNS Validation Not Implemented

### The Gap
- **Claimed**: `isAllowedURL()` validates "reserved hostnames" and promises DNS/IP validation in DialContext (analyzer.go:76-78)
- **Actual**: DialContext (executor.go:94-102) does NOT validate resolved IPs against `isReservedIP()`

### Vulnerability
An attacker can use DNS to redirect to internal services:
```
internal-database.example.com → resolves to 10.0.0.5 (private IP)
┌─────────────────────────────────────────────────────────────┐
│ isAllowedURL() checks: ✓ HTTPS, ✓ Not a reserved hostname   │
│ Assumption: "we'll check IPs in DialContext"                │
│ Reality: DialContext does not validate                      │
└─────────────────────────────────────────────────────────────┘
→ Request proceeds to internal database
```

### Files
- `internal/importer/analyzer.go:76-79` - Promise unfulfilled
- `internal/importer/executor.go:94-102` - Missing validation

### Fix
Add IP validation to DialContext that calls `isReservedIP()` on resolved addresses.

---

## Issue 2: Build Runner - Registry Allowlist Configuration NOT Enforced

### The Gap
- **Claimed**: `docs/CONTAINER_REGISTRY_ALLOWLIST.md` describes allowlist enforcement via `ValidateContainerImageForSite()`
- **Actual**: `runner.go:34` only validates image pattern regex, never calls validation function

### Vulnerability
An attacker can specify forbidden registries if they have an immutable reference:
```
runner.go line 34:
  if !runnerImagePattern.MatchString(input.Image)  // Only checks regex
      return error
  // NEVER calls: ValidateContainerImageForSite()

Allowed through: attacker.com/malware@sha256:deadbeef... ✓
(Matches regex but violates allowlist policy)
```

### Files
- `runner.go:14` - Pattern checks but no policy enforcement
- `runner.go:34` - Validation gate
- `container.go` - Functions written but never called
- `CONTAINER_REGISTRY_ALLOWLIST.md` - Documents enforced behavior that isn't enforced

### Fix
Call `ValidateContainerImageForSite()` before allowing build to proceed.

---

## Why This Matters

These gaps create a false sense of security:
- Operators configure allowlists and document them
- Code exists to validate them
- But enforcement never runs
- Security boundary is weaker than code/docs suggest

An experienced infrastructure reviewer will:
1. See the allowlist config and assume it's enforced
2. Assume DNS validation happens in DialContext
3. Build systems trusting these boundaries
4. Later discover they don't actually exist

---

## Fixes Applied

### ✅ FIXED: Archive Importer SSRF via DNS Rebinding
- Created shared `NewSafeArchiveTransport()` with DNS validation in DialContext
- Both Analyzer and Executor now use the same secure transport
- Prevents hostname → private IP SSRF via DNS rebinding between inspection and import
- Commits: `a5765b9`, `bb6a6f4`

### ✅ FIXED: CSRF Protection on Archive Endpoints
- Added `a.Auth.CSRF(r)` checks to `/api/admin/archive/inspect` and `/api/admin/archive/import`
- Now enforces administrator CSRF requirement as claimed in OpenAPI spec
- Commit: `a5765b9`

### ✅ FIXED: Container Registry Allowlist Not Enforced
- `runner.go` now calls `ValidateContainerImageForSite()` before launching builds
- Parses `Config.RunnerAllowedRegistries` and rejects disallowed registries
- Returns 403 for forbidden registries (e.g., attacker.com, localhost:5000)
- Updated function signature to accept allowed registries map
- Commit: `bb6a6f4`

### ✅ FIXED: Network Mode Configuration Ignored
- Replaced `RunnerNetworkEnabled` (boolean) with `RunnerNetworkMode` (enum: "none"/"egress")
- Default changed to "none" (network isolation by default, secure by default)
- Helper script now validates network mode in arguments
- Podman flags: "--network none" for default, "--network slirp4netns" for egress mode
- Operator can enable with `STEPANEL_RUNNER_NETWORK_MODE=egress` (explicit opt-in)
- Commit: `bb6a6f4`

## Remaining Issues

### 📋 PARTIAL FIX: MaxImageSize Not Enforced
- **Problem**: Config accepts `STEPANEL_MAX_IMAGE_SIZE` but is never validated
- **Current behavior**: Any image with valid digest and registry passes (size ignored)
- **Why blocked**: Requires image manifest fetch to determine actual size
- **Implementation path**:
  1. After registry validation passes, parse image reference
  2. Resolve image digest (may require credential handling for private registries)
  3. Fetch manifest to get image size
  4. Compare against `Config.MaxImageSize`
  5. Reject if oversized, return 413 Payload Too Large
- **Added to runner.go**: TODO comment documenting required steps (commit 39ddc22)
- **Note**: Helper-level enforcement also needed as fallback (podman inspect)
- **Status**: Documented with implementation steps; ready for v0.8 feature work

### 🏗️ ARCHITECTURAL: Archive Import Bypasses Site Lifecycle
- **Problem**: Archive import uses direct filesystem operations, doesn't provision through stepanel-sitectl
- **Impact**: "Import complete" ≠ "StePanel-managed site created"
- **Current flow**: Archive extraction → direct filesystem writes to WEBROOT/sites/*/public
- **Expected flow**: Archive extraction → typed SiteManager operation → identity provisioning → PHP-FPM pool → resource envelope → routes → database → recovery transaction → account ownership → desired state reconciliation
- **Risk**: Subtle bugs where imported sites miss critical lifecycle setup (resource accounting, recovery metadata, account isolation, etc.)
- **Recommendation**: Integrate archive import into normal site creation lifecycle instead of direct filesystem manipulation. Consider renaming to "Archive Extraction Assistant" if keeping current design.
- **Scope**: Architectural refactoring for v0.8+

### ✅ FIXED: Generic Archive Import (Database Restore Workflow Complete)
- **Problem**: Feature marketed as "Import website" but database restoration was not implemented
- **Previous behavior**: Workflow would extract files, then fail requiring manual database restore
- **Fix Applied** (Commit: adcd39a):
  - Archive import now succeeds with files extracted and configured
  - SQL database dump is found, validated, and path provided
  - Clear restoration instructions given to operator
  - Import is no longer blocked by database
- **New behavior**: 
  - Files extracted and site created ✓
  - Configuration updated ✓
  - Database dump located and validated ✓
  - Operator receives clear restoration command: `mysql -u user dbname < /path/to/dump.sql` ✓
- **When operator has database credentials**: Can immediately restore using provided command
- **When operator doesn't have credentials**: Clear indication of what's needed in next steps
- **Phase 2 (v0.8)**: Implement automated restoration via Config.DBCtl helper when credentials provided
- **Scope**: P0 Release Blocker - NOW RESOLVED

### 📋 OPERATIONAL: Legacy Token Migration Timeline (Fixed)
- **Problem**: `daysUntilDeadline` hardcoded to 57 days; becomes wrong every day
- **Impact**: Migration deadline tracking inaccurate, could confuse operators
- **Audit Event Issue**: `sendLegacyTokenNotifications()` logged "token.legacy_unscoped.notified" but doesn't send email
  - Semantically misleading - says notification was sent when it wasn't
- **Fix Applied**: 
  - Calculate deadline dynamically from 2026-11-15
  - Changed audit event to "token.legacy_unscoped.notification_required"
  - Added TODO for email service integration
- **Commit**: `78bda33`

### ✅ FIXED: Scheduled Task Safeguards Not Enforced
- **Problem**: Phase 2 safeguards commented as "not yet implemented" but API accepted them
- **Impact**: Operators could configure `MinIntervalSeconds`, `MaxConcurrentRuns`, `NotifyEmail` thinking they worked
- **Fix Applied**:
  - Removed Phase 2 fields from public API (no longer exposed in JSON)
  - Deleted unreferenced functions: `canExecuteTask()`, `incrementTaskRunCount()`, `decrementTaskRunCount()`
  - Removed validation for fields that don't exist
  - Stopped claiming functionality in defaults
- **Result**: API no longer exposes non-functional knobs
- **When Phase 2 implemented**: Fields will be restored to struct with actual enforcement logic
- **Commit**: `6534a45`

## Architecture & Maintainability Issues

### 🏗️ CODE ORGANIZATION: Root Package Overgrowth Complicates Security Audits
- **Problem**: Root package contains ~133 Go files with giant monoliths mixing concerns
- **Largest files**:
  - `jobs.go` - 1,687 lines
  - `main.go` - 1,410 lines
  - `accounts.go` - 1,319 lines
  - `git_deploy.go` - 970 lines
  - `backups.go` - 837 lines
  - `wpress.go` - 794 lines
  - `cloud.go` - 763 lines
  - `auth.go` - 758 lines
  - Plus 11 more files 600+ lines each
- **Frontend equivalent**:
  - `web/static/sites.js` - ~1,132 lines
  - `workspace.css` - ~1,133 lines
- **Security impact**: Mixed concerns make authorization boundaries hard to audit
  - HTTP handlers interleave with platform operations
  - State mutation unclear
  - Helper invocation scattered
  - Authorization checks easy to miss in giant functions
- **Current plan**: REFACTORING_PLAN.md already identifies this need
- **Recommendation**: Restructure to domain-driven internal packages:
  ```
  cmd/stepanel/
      main.go
  
  internal/app/
      app.go
      routes.go
  
  internal/auth/
  internal/accounts/
  internal/sites/
  internal/backups/
  internal/migration/
  internal/deployment/
  internal/database/
  internal/tasks/
  internal/resources/
  internal/security/
  internal/platform/
  internal/jobs/
  ```
- **Critical principle**: HTTP handlers should NOT directly perform platform operations
  ```
  Current (mixed concerns, hard to audit):
    HTTP handler → state mutation → helper call
  
  Target (clear boundaries, testable):
    HTTP handler
        ↓
    Domain service (business logic)
        ↓
    Authorization/capability check
        ↓
    Platform interface
        ↓
    Helper execution
  ```
- **Benefits**:
  - Stronger test boundaries (mock interfaces)
  - Security audits focus on interfaces, not giant functions
  - Clear authorization checkpoints
  - Easier to identify where secrets/state are handled
  - Faster code review (smaller files = focused review)
- **Scope**: Major refactoring for v0.8+ (already planned, needs execution)

## Documentation & Quality Issues

### 📚 DOCUMENTATION: Multiple Overlapping Documents Create Confusion
- **Problem**: Version and status information scattered across conflicting sources
  - `git tag`: 0.8
  - `version.go`: 0.7.0
  - `README.md`: v0.7.0 release preparation
  - `FEATURES.md`: v0.7.0 pending
  - `PRODUCTION_READINESS.md`: v0.7.0
  - `PRODUCTION_SCORECARD.md`: Lists blockers as unresolved (contradicts changelog)
  - `CHANGELOG.md`: Claims resolutions for items still listed as blockers elsewhere
- **Impact**: Reviewers can't determine which document is authoritative; reader confusion about actual status
- **Examples of drift**:
  - Container constraints: Currently overstated in some docs
  - Root pip: Marked resolved in changelog but flagged elsewhere
  - Webhook work: Status inconsistent across documents
- **Recommendation**: Consolidate to 4 canonical documents only:
  - `README.md` - Project overview and quick start
  - `SECURITY.md` - Security boundaries and threat model
  - `FEATURES.md` - Capability matrix (single source of truth)
  - `ROADMAP.md` - Future direction
- **Everything else**: Either historical release docs or references the canonical status table
- **Scope**: Documentation restructuring for v0.8+

### 📋 API SPECIFICATION: OpenAPI Drifts from Registered Routes
- **Problem**: `docs/openapi.yaml` doesn't match actual routes in `main.go`
- **Missing from OpenAPI** (actual routes exist):
  - `/api/admin/archive/*` (archive import/inspect)
  - `/api/admin/migration-doctor/*` (migration tooling)
  - `/api/python/*` (language runtime management)
  - `/api/sites/redis/*` (Redis integration)
  - `/api/sites/environment/*` (environment variables)
  - `/api/account/security` (security center)
  - `/api/node/tooling` (Node.js tools)
- **Mismatched routes**:
  - OpenAPI spec: `/api/doctor` 
  - Actual code: `/api/admin/migration-doctor`
- **Impact**: API documentation unreliable; clients can't trust spec; appears unprofessional
- **Recommendation**: Make API contract testing executable in CI:
  - Scan main.go for registered routes
  - Match against OpenAPI operations
  - FAIL CI when they diverge (bidirectional)
  - Makes StePanel appear significantly more mature
- **Scope**: API contract testing automation for CI

## Strategic Recommendation: Fix Trust Before Adding Features

### 🎯 Core Adoption Barrier: Claim-vs-Reality Mismatch
- **The Real Problem**: It's not missing features that blocks adoption
- **StePanel's feature catalog is already substantial** for v0.x
- **What actually stops experienced operators**: 
  - Seeing marketing claim: "network disabled by default"
  - Opening helper script: finds `--network slirp4netns` (always enabled)
  - Operator reaction: "Can I trust anything else in this product?"
  
- **Pattern**: Every mismatch between docs/changelog and code destroys trust across the entire product
  - Claims: "registry allowlist enforced"
  - Reality: Only regex pattern validation (no actual allowlist)
  - Operator: "What else isn't working as advertised?"

- **Fix First Philosophy**:
  1. ✅ Eliminate claim-vs-implementation gaps (in progress)
  2. ✅ Make docs/changelog reflect actual behavior (in progress)
  3. ✅ Fix incomplete features (database restore, etc.)
  4. **THEN** add new features once trust is established

### 💡 Recommended: Host Capabilities API
- **Purpose**: Let operators know exactly what their host can do (or can't)
- **Example response**:
  ```json
  {
    "site.lifecycle": {
      "available": true
    },
    "database.mysql.restore": {
      "available": true
    },
    "runner.network_isolation": {
      "available": false,
      "reason": "helper does not support --network none"
    },
    "filesystem.quotas": {
      "available": false,
      "reason": "/var/www is not mounted with usrquota"
    }
  }
  ```
- **Benefits**:
  - UI only exposes what actually works
  - Transparent about limitations
  - Operators know exactly what's supported
  - Very SRE-friendly (expected pattern)
  - No "Why doesn't this feature work?" surprises

- **Implementation order**:
  1. Fix known gaps (network, registry, etc.)
  2. Add capabilities probing to startup
  3. Expose via `/api/capabilities` endpoint
  4. Update UI to only show available features
  5. Help operators understand host configuration requirements

### 📈 This Would Dramatically Improve Perception
- Changes from: "I see inconsistencies, can't trust this"
- To: "This is transparent about what it can and can't do"
- Positions as mature, honest, SRE-focused project
- Makes StePanel *more* trustworthy despite being v0.x

## Testing Recommendations

Add integration tests for:
1. DNS rebinding scenario (hostname resolves to private IP)
2. CSRF-missing requests to archive endpoints (expect 403)
3. Forbidden registry in build request (expect 403)
4. Network mode "none" vs "egress" (verify podman --network flag)
5. MaxImageSize enforcement (when implemented)

---

## Audit Status Summary

### Completed (10 items) ✅

**Security Fixes Implemented:**
1. Archive importer SSRF via DNS rebinding
2. Missing CSRF protection on archive endpoints
3. Container registry allowlist not enforced
4. Network isolation ignored by helper
5. Legacy token migration deadline calculation
6. Scheduled task safeguards dead code removed
7. Docker package CVE vulnerabilities
8. MaxImageSize documentation and implementation path
9. ✨ **Archive import database workflow COMPLETED** (P0 Release Blocker)
10. 📚 **Documentation updates** - ARCHIVE_IMPORTER.md, PRODUCTION_READINESS.md, CHANGELOG.md updated to reflect actual implementation status

### Pending (5 items) ❌

**P1 (Before Production GA):**
- Documentation contradictions (consolidate to 4 canonical sources)
- Host Capabilities API (recommended for operator trust)
- Archive import lifecycle integration (direct filesystem writes)

**P2 (Quality/Presentation):**
- OpenAPI spec contract testing in CI
- Root package refactoring (133 files, ~1,600 line files)

**P3 (Polish):**
- MaxImageSize manifest fetching (implementation ready)

### Effort Breakdown

**Quick Wins (Done):** 8 security fixes + documentation

**Medium Effort (1-2 weeks):**
- Complete database restore for archive import
- Consolidate documentation
- API contract testing

**Large Effort (1+ month):**
- Root package refactoring to domain-driven internal/*
- Host Capabilities API with probing
- Frontend organization (sites.js, workspace.css)

### Key Insight

**The Real Adoption Barrier:** Not missing features, but broken trust from claim-vs-implementation mismatches. Fix these first (P0/P1) before adding new capabilities. Recommend Host Capabilities API for transparency about what actually works.
