# Helper Layer Refactoring: Replace Shell with Typed Go Broker

**Status:** Planning  
**Priority:** P1 - Security boundary  
**Effort:** 3-4 weeks  
**Risk:** Medium (high consequence if wrong, but testable)

## Problem Statement

StePanel uses 14 shell scripts (~1200 lines total) that run as root via `sudo NOPASSWD`:

```
stepanel ALL=(root) NOPASSWD: /usr/local/sbin/stepanel-*ctl
```

While these scripts have good validation, **any parsing mistake becomes a root escalation**. Shell is inherently error-prone for security boundaries.

### Current Helper Scripts (by risk)

| Helper | Lines | Risk | Purpose |
|--------|-------|------|---------|
| **stepanel-sitectl** | 377 | 🔴 HIGH | Site lifecycle (users, permissions, cleanup) |
| **stepanel-appctl** | 402 | 🔴 HIGH | App state (systemd, manifests, node management) |
| **stepanel-dbctl** | 241 | 🟠 MEDIUM | Database provisioning, restoration |
| **stepanel-vhostctl** | 158 | 🟠 MEDIUM | Virtual host configuration |
| **stepanel-proxyctl** | 101 | 🟠 MEDIUM | Reverse proxy state |
| **stepanel-caddy-vhostctl** | 99 | 🟠 MEDIUM | Caddy-specific vhost logic |
| **stepanel-gitctl** | 49 | 🟡 LOW | Git operations (well-validated) |
| **stepanel-runnerctl** | 56 | 🟡 LOW | Container runner control |
| **stepanel-ols-proxyctl** | 57 | 🟡 LOW | OpenLiteSpeed proxy |
| **stepanel-ols-vhostctl** | 51 | 🟡 LOW | OpenLiteSpeed vhosts |
| **stepanel-caddy-proxyctl** | 47 | 🟡 LOW | Caddy proxy state |
| **stepanel-malware-guard** | 30 | 🟡 LOW | Malware scanning integration |
| **stepanel-certbot** | 11 | 🟡 LOW | TLS certificate renewal |
| **stepanel-apache-reload** | 14 | 🟡 LOW | Apache reload wrapper |

## Solution: Typed Go Root Broker

Replace shell scripts with a **single typed Go binary** (`stepanel-root`) that handles all privileged operations.

### Architecture

```
┌─────────────────┐
│  StePanel App   │  (unprivileged, regular process)
│   (Go binary)   │
└────────┬────────┘
         │
         │ RPC/JSON or stdin/stdout
         │ Strongly-typed request structs
         │
┌────────▼────────┐
│ stepanel-root   │  (privileged, runs as root via sudo)
│   (Go binary)   │
│                 │
│ ├─ site.Create  │
│ ├─ site.Delete  │
│ ├─ app.Manage   │
│ ├─ db.Provision │
│ ├─ vhost.Apply  │
│ └─ ...          │
└────────┬────────┘
         │
         └──→ Actual system operations
             (useradd, systemctl, etc.)
```

### Benefits

✅ **Typed request/response structures** - No string parsing ambiguity
✅ **Single validation layer** - One place to audit, not 14
✅ **Better error reporting** - Structured errors instead of exit codes
✅ **Easier testing** - Unit test Go code vs. shell
✅ **Version mismatch detection** - RPC versioning prevents old App calling new Root
✅ **Audit trail** - All privileged operations logged
✅ **Same security model** - Still uses sudo, but with typed boundaries

## Implementation Plan

### Phase 1: Design & Foundation (1 week)

**1.1 Define RPC Interface**
```go
// Request/response types for all privileged operations
package rootbroker

// SiteRequest: Create, update, delete site
type SiteRequest struct {
    Action   string // "create", "delete", "seal", "prepare"
    Site     string // Validated: [a-z0-9_-]{1,32}
    Arg1     string // Context-dependent (e.g., SSH keys)
    Arg2     string
}

type SiteResponse struct {
    OK        bool
    Error     string
    Details   map[string]string
}

// AppRequest: Manage application state
type AppRequest struct {
    Action   string // "apply", "start", "stop", "restart"
    Site     string
    Version  string
    Port     int
    Root     string
}

// ... more request types
```

**1.2 Define Input Validation Rules**
```go
// Central validation for all inputs
type Validator struct {
    ValidateSiteName(s string) error
    ValidateFilePath(root, path string) error
    ValidateSSHKey(key string) error
    ValidateNumericRange(val int, min, max int) error
    // ...
}
```

**1.3 Implement Core RPC Layer**
- Choose: stdin/JSON (simple, no daemon) vs. Unix socket RPC
- Recommendation: stdin/JSON (simpler, no long-lived daemon to manage)
- Each request/response is a single JSON line

### Phase 2: Implement Broker Operations (2 weeks)

**2.1 Site Operations** (replace stepanel-sitectl)
```go
func (b *Broker) SiteCreate(ctx context.Context, req *SiteRequest) (*SiteResponse, error)
func (b *Broker) SiteDelete(ctx context.Context, req *SiteRequest) (*SiteResponse, error)
func (b *Broker) SiteSeal(ctx context.Context, req *SiteRequest) (*SiteResponse, error)
```

**2.2 App Operations** (replace stepanel-appctl)
```go
func (b *Broker) AppApply(ctx context.Context, req *AppRequest) (*AppResponse, error)
```

**2.3 Database Operations** (replace stepanel-dbctl)
```go
func (b *Broker) DBProvision(ctx context.Context, req *DBRequest) (*DBResponse, error)
```

**2.4 Vhost Operations** (replace stepanel-vhostctl + caddy-vhostctl + ols-vhostctl)
```go
func (b *Broker) VhostApply(ctx context.Context, req *VhostRequest) (*VhostResponse, error)
```

**2.5 Proxy Operations** (replace proxyctl variants)
```go
func (b *Broker) ProxyApply(ctx context.Context, req *ProxyRequest) (*ProxyResponse, error)
```

### Phase 3: Integration & Testing (1 week)

**3.1 Replace Callsites in StePanel App**
Change from:
```go
runHelperCommand(ctx, config, "sitectl", "prepare", site)
```

To:
```go
rootBroker.Do(ctx, &rootbroker.Request{
    Type: "site",
    Action: "prepare",
    Site: site,
})
```

**3.2 Unit Tests for Broker**
- Test each operation with valid/invalid inputs
- Test error conditions (no perms, missing binaries, etc.)
- Test atomic operations (rollback on failure)

**3.3 Integration Tests**
- Test App → Root broker communication
- Test actual system operations (useradd, systemctl, etc.)
- Test permission preservation (mode, ownership)

### Phase 4: Gradual Migration (2 weeks)

**Step 1:** Ship Go broker alongside shell scripts
- New StePanel app uses Go broker
- Old app continues using shell scripts (if deployed older version)

**Step 2:** Monitor for issues in production
- Broker logs all operations
- Compare broker results vs. shell results on parallel runs

**Step 3:** Deprecate shell scripts in next release
- Remove shell scripts from deployment
- Go broker is now mandatory

## Risk Mitigation

### Testing Strategy
- Unit tests for all validation rules
- Fuzzing for edge cases (long names, special characters, etc.)
- Integration tests with actual system calls (in test container)
- Parallel shadow testing (run broker + shell, compare results)

### Rollback Plan
- Keep shell scripts for 2 releases
- If broker has bug, revert to shell scripts
- Broker versioning: App checks broker version, refuses mismatch

### Validation Improvements
```go
// Every input validated at broker entry point
func (b *Broker) validateRequest(req *Request) error {
    // Type check
    // Bounds check
    // Regex validation
    // Path traversal check
    // Symlink check
    // Permission check
    return nil
}
```

## Audit Surface Reduction

**Before:** 1200 lines of shell across 14 files
```
- Parser bugs (regex, variable expansion, quoting)
- Path construction bugs (symlinks, traversal)
- Error handling bugs (silent failures, wrong exit codes)
- Complex control flow (multiple error paths)
```

**After:** ~400 lines of Go in single broker
```
- Type-safe validation (no string parsing)
- Clear error types (enum, not exit codes)
- Testable logic (unit tests on functions)
- Single entry point for auditing
```

## Success Criteria

- ✅ All 14 helpers replaced by single Go broker
- ✅ 100% of test cases passing
- ✅ No regression in functionality
- ✅ 95% smaller audit surface (1200 → ~400 lines)
- ✅ Type-safe interfaces between app and broker
- ✅ Typed error responses (not shell exit codes)
- ✅ All operations logged and auditable

## Timeline

| Phase | Duration | Effort |
|-------|----------|--------|
| Phase 1: Design | 1 week | 40 hours |
| Phase 2: Implementation | 2 weeks | 80 hours |
| Phase 3: Testing | 1 week | 40 hours |
| Phase 4: Gradual migration | 2 weeks | 40 hours |
| **Total** | **6 weeks** | **200 hours** |

*Estimated with parallel work (design + implementation can overlap)*

## Why This Works

1. **Type Safety** — Go's type system catches mismatches at compile time, not runtime
2. **Single Audit Target** — One codebase instead of 14 shell scripts
3. **Testability** — Go functions are easier to unit test than shell scripts
4. **Maintainability** — Future security fixes go in one place
5. **Same Security Model** — Still uses sudo, but with stronger boundaries

## Related Documents

- [SECURITY.md](SECURITY.md) — Security model and threat boundaries
- [CLAUDE.md](../CLAUDE.md) — Code quality standards (applies to Go broker)
- [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md) — Deployment prerequisites

---

**Next Step:** Approve design and begin Phase 1.
