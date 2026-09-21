# StePanel Architecture & Code Organization Roadmap

## Design Philosophy

StePanel is built around a **privilege boundary**, not a monolith:

```
Browser (untrusted context)
    ↓ HTTPS
Unprivileged Go control plane (validation + orchestration)
    ↓ Unix socket (validated args only)
Privileged helper (root-owned platform operations)
    ↓
Filesystem, processes, databases
```

This architecture is the project's greatest strength and must be preserved during reorganization.

## Current Code Organization Issues

### main.go (1,300+ lines) — NEEDS REFACTORING
All HTTP routes, handlers, initialization, and business logic orchestration in one file.

**Solution**: Split into internal packages:
```
internal/httpapi/       # HTTP handler registration, middleware
internal/auth/          # Authentication, tokens, sessions, TOTP
internal/sites/         # Site lifecycle (create, delete, config)
internal/backups/       # Backup scheduling, retention, restore
internal/migrations/    # Archive import, restore orchestration
internal/deploy/        # Git deployment, webhook auth
internal/database/      # Database operations, migrations
internal/security/      # Audit logging, permission checks
internal/platform/      # Privileged helper invocation, process trees
internal/jobs/          # Job queue, durable operations
internal/importer/      # (Already split) Archive analysis + extraction
```

### sites.js (1,100 lines) — NEEDS REFACTORING
Single monolithic frontend controller handles all UI state, API calls, DOM, and dialogs.

**Solution**: Split into modules:
```
assets/js/
  api.js              # API client, request/response handling
  dom.js              # DOM utilities, selectors, attributes
  dialogs.js          # Modal/dialog framework, confirmation flow
  state.js            # Client-side state management
  sites/
    overview.js       # Site listing, search, bulk operations
    runtime.js        # Live status, uptime, resource usage
    deployments.js    # Git deployment history, triggers
    databases.js      # Database browser, query interface
    backups.js        # Backup calendar, restore, retention
    logs.js           # Log viewer, search, filtering
    security.js       # Tokens, audit log, access controls
```

### Testing Coverage — CRITICAL ISSUE
**Current**: Global threshold 60%  
**Problem**: Single aggregate percentage is useless for security software

**Solution**: Per-package thresholds, especially:
- `internal/auth/` — **90% branch coverage minimum**
- `internal/security/` — **90% branch coverage minimum**  
- `internal/platform/` — **90% branch coverage minimum**
- `internal/importer/` — **90% branch coverage minimum** (currently 0%)
- `internal/sites/` — **85% branch coverage minimum**

Security-critical paths (tenant isolation, authorization, path validation, archive extraction) must have high coverage.

## Security Practices to Preserve

These are the project's identity and must become more visible:

1. **Process Isolation** — Process groups + SIGKILL for cleanup
2. **File Safety** — O_NOFOLLOW, symlink rejection
3. **Input Validation** — Strict patterns (regex + length)
4. **Audit Trail** — Every privileged operation logged
5. **Rate Limiting** — Per-token, per-user limits
6. **Typed Confirmations** — Destructive ops require explicit typed confirmation
7. **Recovery Journals** — Restore records pre-mutation state
8. **Bounded Resources** — Capped memory, file count, subprocess output
9. **Context Cancellation** — Long ops respect job cancellation
10. **Privileged Helper Isolation** — Narrow socket interface

## Refactoring Roadmap

### Phase 1: Code Organization (v0.8)
- Split main.go into internal packages
- Split sites.js into modules  
- Each package exports `RegisterRoutes()`, owns its tests
- Verify no circular dependencies

### Phase 2: Testing (v0.8)
- Add per-package coverage thresholds (90% for security)
- Write tests for internal/importer/ (currently 0%)
- Add integration tests for privilege boundaries
- Add adversarial tests for input validation

### Phase 3: Documentation (v0.9)
- Package README.md files (responsibility, contracts)
- Request/response type Godoc comments
- Privilege boundary examples
- Frontend module documentation

### Phase 4: Cleanup (v1.0)
- Remove experimental/deprecated code
- Finalize public API surface
- Archive old documentation

## Why This Matters

StePanel's strength is **humility about scope**: solve WordPress operations, respect privilege boundaries.

But as codebase grows, unmaintainability erodes that humility:
- 1,300-line main.go signals: "ball of mud"
- 1,100-line sites.js signals: "frontend pasta"
- No tests for importer signals: "we don't actually care about this"

A properly organized codebase signals: "this is a control plane I can understand, audit, and extend."

The architecture and security practices are there. Organization needs to catch up.
