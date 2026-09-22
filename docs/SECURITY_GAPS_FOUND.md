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

### ⚠️ NOT YET FIXED: MaxImageSize Not Enforced
- Config accepts `STEPANEL_MAX_IMAGE_SIZE`
- `MaxImageSize` field exists but is never used
- Requires fetching manifest/size before launching container
- Needs separate work to inspect image size
- **Status**: Tracked but deferred to separate PR

### 🏗️ ARCHITECTURAL: Archive Import Bypasses Site Lifecycle
- **Problem**: Archive import uses direct filesystem operations, doesn't provision through stepanel-sitectl
- **Impact**: "Import complete" ≠ "StePanel-managed site created"
- **Current flow**: Archive extraction → direct filesystem writes to WEBROOT/sites/*/public
- **Expected flow**: Archive extraction → typed SiteManager operation → identity provisioning → PHP-FPM pool → resource envelope → routes → database → recovery transaction → account ownership → desired state reconciliation
- **Risk**: Subtle bugs where imported sites miss critical lifecycle setup (resource accounting, recovery metadata, account isolation, etc.)
- **Recommendation**: Integrate archive import into normal site creation lifecycle instead of direct filesystem manipulation. Consider renaming to "Archive Extraction Assistant" if keeping current design.
- **Scope**: Architectural refactoring for v0.8+

## Testing Recommendations

Add integration tests for:
1. DNS rebinding scenario (hostname resolves to private IP)
2. CSRF-missing requests to archive endpoints (expect 403)
3. Forbidden registry in build request (expect 403)
4. Network mode "none" vs "egress" (verify podman --network flag)
5. MaxImageSize enforcement (when implemented)
