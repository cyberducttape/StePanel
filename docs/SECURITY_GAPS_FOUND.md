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

## Recommended Fixes (Priority Order)

### P0: Archive Importer SSRF
1. Add IP validation to DialContext (**prevents SSRF bypass via DNS**)
2. Verify isReservedIP() is called on all resolved addresses
3. Test with hostname → private IP scenarios

### P1: Build Runner Allowlist
1. Call `ValidateContainerImageForSite(site, image)` in `runner.go:runnerBuild()`
2. Return error if validation fails
3. Remove or update documentation if allowlist becomes truly optional
4. Test with forbidden registries (attacker.com, localhost:5000, etc.)

### P2: Code/Documentation Alignment
1. Audit all "planned" features that have tests/code but aren't wired
2. Update docs to clearly mark enforcement status
3. Add integration tests that verify claimed protections actually work
