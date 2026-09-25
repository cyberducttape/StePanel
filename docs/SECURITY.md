# StePanel Security

**Last Updated:** 2026-09-25
**Status:** Security reference; release approval is governed by [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md)

This document summarizes implemented security controls and known limitations. It is
not a production-release approval. See [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md)
for the sole current release gate and [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md)
for deployment status.

## Architecture

StePanel runs as a privileged control panel for WordPress sites. Security is enforced at multiple layers:

1. **Authentication**: API tokens, TOTP, per-site isolation
2. **Authorization**: Admin-only endpoints, site-scoped operations
3. **Isolation**: Process-tree cancellation, symlink rejection, filesystem bounds
4. **Hardening**: Container registry allowlist, webhook auth, no legacy protocols

## Completed Security Features

### Authentication & Tokens
- ✅ API token rate limiting (600 req/min per token, configurable)
- ✅ Legacy token deprecation tracking (30-day grace period) 
- ✅ TOTP enforcement for admin login
- ✅ Session management with secure cookies

### Container Security
- ✅ Registry allowlist (docker.io, ghcr.io, quay.io only)
- ✅ Digest-pinned image requirement (tag+sha256 validation)
- ✅ Path injection prevention in image references

### Archive Import
- ✅ SSRF protection (https only, no private IPs/localhost)
- ✅ Size-bounded downloads (prevents streaming attacks)
- ✅ Entry count limits (250k total, prevents directory bombs)
- ✅ Symlink rejection during extraction
- ✅ Path traversal validation (.. rejection, absolute path rejection)
- ✅ Decompression bomb protection (50GB limit)
- ✅ Context-aware cancellation (imports respect job cancellation)

### File Operations
- ✅ Symlink safety in backup/restore (O_NOFOLLOW)
- ✅ Process tree termination (SIGKILL process groups)
- ✅ Recursive directory traversal safety (filepath.WalkDir)
- ✅ Atomic file writes (temporary file + rename pattern)

### Webhooks
- ✅ Per-site webhook isolation
- ✅ Repository allowlist (webhook can only deploy from configured repos)
- ✅ Reference (branch/tag) filtering
- ✅ Audit logging for webhook failures

## Known Limitations

### Archive Import
- Database restoration is optional: automatic restoration requires managed-helper availability,
  valid archive credentials, and a successful transactional restore; otherwise the SQL dump
  remains an explicit operator follow-up
- Config file updates use atomic writes with proper parsing (preserves file permissions)
- Memory tracking for largest-files limited to 100 entries (to prevent memory bombs)
- No archive encryption support

### Process Management  
- Only Linux process-tree termination tested (Setpgid + SIGKILL)
- Windows process handling less robust

## Threat Model

### Threats We Defend Against

1. **Compromised API Token**
   - Mitigated: Rate limiting per token, token expiration, audit logging
   - Remaining risk: Attacker with valid token can perform all operations the token allows

2. **Malicious Archive Upload**
   - Mitigated: SSRF protection, size limits, entry limits, symlink rejection
   - Remaining risk: Decompression bomb still consumes 50GB RAM during inspection

3. **Privilege Escalation**
   - Mitigated: All operations scoped to authenticated user, no setuid helpers
   - Remaining risk: Process compromise affects all sites on that host

4. **Supply Chain (Container Images)**
   - Mitigated: Registry allowlist, digest pinning required
   - Remaining risk: Compromised upstream images (docker.io/library/...) affect all users

5. **Admin Session Compromise**
   - Mitigated: HTTPS, TOTP, session expiration
   - Risk: Attacker with admin session has unrestricted host access

### Threats Out of Scope

- Physical host compromise
- Network interception (assumes HTTPS)
- Brute-force credential attacks (rate limiting insufficient for offline testing)
- Compromised database layer

## Audit & Compliance

### Code Review
- Archive importer: Adversarial test suite with 75+ security tests
- Container validation: Path injection and registry tests
- HTTP handlers: Request forgery prevention (SSRF, path injection)

### Testing
- Path traversal validation in all extract paths
- Symlink rejection in all file operations  
- Rate limiter bounded-memory and behavior tests
- Process tree cancellation under timeout and explicit kill

## Recommendations for Operators

1. **Network Isolation**
   - Run StePanel on isolated network segment
   - Restrict outbound to only essential endpoints (package mirrors, container registry)
   - Block admin access from untrusted networks

2. **Access Control**
   - Enable TOTP for all admin accounts
   - Rotate API tokens regularly (30+ day lifecycle)
   - Audit token usage via /api/admin/tokens endpoint
   - Disable HTTP (HTTPS only)

3. **Monitoring**
   - Watch `/var/log/syslog` for StePanel audit entries
   - Alert on webhook authorization failures (potential compromised webhook)
   - Track decompressed archive sizes (>10GB may indicate attack)
   - Monitor process CPU/memory (decompression bomb detection)

4. **Incident Response**
   - Revoke all tokens immediately if admin account compromised
   - Investigate webhook failures for repository compromise
   - Review extracted archives for suspicious paths before restoration

## Version History

- **v0.7** (Historical): import workflow functional for controlled single-host operation
- **v0.6**: Container registry allowlist, digest pinning
- **v0.5**: Archive import beta (no DB restoration yet)
- **v0.4**: Legacy token deprecation, TOTP enforcement

## See Also

- [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) — Feature completeness matrix
- [ARCHITECTURE.md](./ARCHITECTURE.md) — System design and components
- [ROADMAP.md](./ROADMAP.md) — Planned features and timeline

---

**Important**: This document is machine-generated from implementation code. If implementation contradicts this document, implementation is correct and this doc needs updating. Open an issue if you find discrepancies.
