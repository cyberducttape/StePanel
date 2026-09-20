# v0.7.0 Production Blocker Resolutions

Six critical issues must be resolved before v0.7.0 can be recommended for production multi-tenant deployments.

## Blocker 1: Root-Network Pip Install ⚠️ CRITICAL

**Issue**: Python package installations run as root with unrestricted network access. Compromised PyPI mirror or MITM attack could install malicious packages on the host.

**Status**: REQUIRES FIX

**Solution**:
1. Run pip install as the site user (unprivileged), not root
2. Use `--no-index --find-links=/var/cache/stepanel/wheels/` to block network access
3. Pre-cache wheels in read-only directory during system setup
4. Implement wheel signature verification
5. Sandbox pip install in network-isolated container or chroot

**Implementation Priority**: HIGHEST
**Risk**: Supply chain attack on hosting platform

**Recommended Approach**:
- Create wheel cache directory: `/var/cache/stepanel/wheels/`
- Pre-download common packages at install time
- Pin specific wheel versions (not flexible versioning)
- Run pip as site user with `--user --no-index`
- Document fallback: operators can preload wheels for custom packages

---

## Blocker 2: Webhook Authorization Redesign ⚠️ CRITICAL

**Issue**: Current webhook auth doesn't bind webhooks to specific repositories/branches. A webhook secret leak exposes all sites' deployments.

**Status**: REQUIRES REDESIGN

**Current Model**:
```
GitHub webhook → shared secret → deploys any branch on any site
```

**Required Model**:
```
GitHub webhook + site + repository + branch/ref → unique authorization token
```

**Changes Needed**:
1. Add `site_id` to webhook token generation
2. Add `repository_url` to webhook token (bind to specific GitHub repo)
3. Add `ref_pattern` (main, develop, refs/tags/v*, etc.)
4. Verify webhook signature includes these bindings
5. Reject webhooks that don't match all three dimensions
6. Log/audit webhook rejections for debugging

**Implementation**:
- Update webhook token format to include site/repo/ref
- Verify HMAC over full context, not just payload
- Database table: `webhook_authorizations(site, repo_url, ref_pattern, secret_hash, created_at, expires_at)`
- Reject any webhook without exact site + repo + ref match

**Example**: Webhook for `mysite` + `github.com/user/repo` + `refs/heads/main` only deploys those specific changes to that site

---

## Blocker 3: Fix Process-Tree Cancellation ⚠️ CRITICAL

**Issue**: Current implementation has partial process-tree cleanup. Long-running child processes (npm, pip, cargo builds) can outlive their parent timeout.

**Status**: PARTIALLY IMPLEMENTED (needs verification)

**Review Required**:
```go
internal/helper/helpers.go:117-123 (configureProcessGroup)
internal/helper/helpers.go:100-102 (Kill -PGID)
```

Verify:
1. ✅ `Setpgid=true` creates process group? Check Unix documentation
2. ✅ `unix.Kill(-cmd.Process.Pid, unix.SIGKILL)` kills entire group? 
3. ⚠️ What about children spawned by npm/pip (install workers, compilers)?
4. ⚠️ Are there race conditions between detection and kill?

**Known Issues**:
- npm/pip spawn worker processes that may ignore parent death
- Systemd might be managing the processes, not raw process groups
- Container/cgroup isolation may prevent cross-process kills

**Testing Required**:
```bash
# Long-running npm build, kill should terminate all children
go test -v ./internal/helper/... -run TestProcessGroupKill
```

---

## Blocker 4: Constrain Container Build Image/Egress Model ⚠️ CRITICAL

**Issue**: Container builds can pull arbitrary images and have unrestricted egress. No verification of image signatures or registry allowed-list.

**Status**: REQUIRES CONSTRAINTS

**Required Changes**:
1. Registry allowlist (only docker.io, ghcr.io, quay.io)
2. Image signature verification (cosign, sigstore)
3. Rate-limit image pulls per-site
4. Block egress to non-registry networks
5. Image size enforcement (prevent DoS via huge layers)
6. Implement re-export detection (block pull-from-untrusted-registry-and-push-to-attacker)

**Implementation**:
```go
// In containerd/runner helper:
if !isAllowedRegistry(imageRef) {
    return err "registry not allowed"
}

// Verify signature before pull
if !verifyImageSignature(imageRef, trustedPublicKey) {
    return err "image signature verification failed"
}

// Enforce size limits
if imageSizeBytes > maxImageSizeBytes {
    return err "image exceeds maximum size"
}

// Block unexpected registries
if !isAllowedEgress(network.Addr()) {
    return err "egress not allowed"
}
```

**Registry List**:
- `docker.io` - Docker Hub
- `ghcr.io` - GitHub Container Registry
- `quay.io` - Quay.io (Red Hat)

---

## Blocker 5: Stop Full Archive Verification During Backup Listing ⚠️ MEDIUM

**Issue**: Every backup list operation re-hashes entire backup archives. This is O(archive_size) per request and can exhaust disk I/O.

**Status**: FIXABLE WITH METADATA INDEX

**Current Flow**:
```
GET /api/backups → listBackupsPage()
  → filepath.Walk(backupRoot)
    → for each backup: VerifySiteBackup()
      → hash entire archive (20GB takes 2+ minutes)
```

**Required Solution**: Use SQLite verification cache

**Implementation**:
1. Move verification to background job (not API request)
2. Cache verification results in SQLite with TTL (5 minutes)
3. Serve list from cache during TTL
4. Return `verified_at` from cache, not real-time
5. Trigger async re-verification if cache expires

**Example**:
```go
// In backup_index.go (already written)
cache := idx.GetVerificationCache(site, backupName)
if cache != nil && cache.IsValid() {
    return cache.CheckSum // O(1)
}

// Trigger async verification (returns immediately)
jobs.Queue(VerifyBackupJob{site, backupName})
```

**Performance**: O(1) list operations, background verification

---

## Blocker 6: Migrate or Expire Unrestricted Legacy API Tokens ⚠️ CRITICAL

**Issue**: Unscoped legacy tokens grant full API access. If leaked, attacker can delete sites, restore arbitrary backups, create databases.

**Status**: PARTIALLY IMPLEMENTED (detection only)

**Current Status**:
- ✅ Detected in UI: "You have legacy unscoped tokens"
- ✅ Audit logged: "token.legacy_unscoped_access"
- ❌ NOT ENFORCED: tokens still work with full access

**Required Solution**: Enforce expiration or mandatory migration

**Option A: Expiration (Recommended)**
```
Today: All legacy tokens detected, flagged in audit
Day 1-30: Work period, legacy tokens still functional
Day 31: Automatic revocation, audit logged
Day 32: New API calls with legacy tokens → 401 Unauthorized
```

**Option B: Mandatory Migration**
- Legacy token → generate scoped token automatically
- Migrate scopes to minimal required (if possible)
- Log migration in audit
- Disable legacy token

**Implementation (Option A)**:
```go
type apiTokenInfo struct {
    // ... existing fields
    LegacyUnscoped bool
    DeprecationDate *time.Time  // When legacy status detected
    ExpiresAt      *time.Time   // Automatic expiration
}

// On token use
if token.IsLegacyUnscoped() && time.Now().After(token.ExpiresAt) {
    return 401 "Legacy unscoped tokens expired. Create new scoped tokens."
}
```

**Timeline**:
- Day 0: Audit finds all legacy tokens
- Day 1-30: Operators migrate to scoped tokens
- Day 31: Automatic expiration
- Day 32+: Enforce 401 on legacy token use

---

## Secondary Issues (Address after blockers)

### Startup Reconciliation
- ✅ ADDRESSED in previous session
- Timeline tracking with phases
- Status visible from /readyz

### Helper Timeout Semantics  
- ✅ ADDRESSED in previous session
- Operation-specific timeouts
- Differentiated HTTP timeouts

---

## Code Structural Recommendation (LOW priority for v0.7.0)

Continue extracting from `package main` into `internal/` packages:

**Current**: 28,000 lines in root-level files
**Target**: <5,000 lines in root package (bootstrap only)

**Priority Order**:
1. `internal/backup` - Extract 2300 lines of backup code
2. `internal/site` - Extract site lifecycle
3. `internal/deployment` - Extract git/app deployment
4. `internal/database` - Extract database operations
5. `internal/httpapi` - Extract HTTP handlers
6. `cmd/stepanel` - Move main() and bootstrap

This doesn't block v0.7.0 but improves maintainability for future versions.

---

## Sign-Off Checklist for v0.7.0 Production

- [ ] Blocker 1: Pip install runs unprivileged, no network access
- [ ] Blocker 2: Webhook authorization bound to site/repo/ref
- [ ] Blocker 3: Process tree cancellation verified working
- [ ] Blocker 4: Container registry/image constraints enforced
- [ ] Blocker 5: Archive verification cached (not during listing)
- [ ] Blocker 6: Legacy tokens expire or migrated
- [ ] Secondary: Startup reconciliation with phases
- [ ] Secondary: Helper timeout semantics documented
- [ ] Testing: Recovery drills pass
- [ ] Testing: E2E smoke test passes
- [ ] Documentation: All changes documented
- [ ] Audit: All changes audit-logged

**Status**: 2/8 ✅ (issues 3, 5 partially done; others require work)
