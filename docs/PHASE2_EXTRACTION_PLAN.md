# Phase 2 Extraction Plan (v0.8.0)

**Status:** Planning  
**Scope:** Extract 4 independent domains from root package into internal packages  
**Estimated commits:** 40-60 across all domains  
**Estimated review time:** 1-2 weeks (due to scope)

## Extraction Order (Dependency Analysis)

### Domain 1: `internal/audit/` (LOWEST DEPENDENCIES)

**Current location:** Scattered in main.go, auth.go

**Functions to extract:**
- `Audit(path, event, actor, detail string) error`
- `AuditAs(path, actor, event, subject, detail string) error`
- `ShouldAudit(path, actor, event, subject, detail string) error`
- `VerifyAuditLog(path string) error`
- `auditEvents(w http.ResponseWriter, r *http.Request)` (HTTP handler)
- `securityAudit(w http.ResponseWriter, r *http.Request)` (HTTP handler)

**Dependencies:**
- Config.AuditLog (path to audit log file)
- Auth system (for current user)
- No other subsystem depends on audit internals

**New interfaces:**
```go
type AuditLogger interface {
    LogEvent(ctx context.Context, actor, event, subject, detail string) error
    VerifyLog(path string) error
    GetEvents(filter *EventFilter) ([]AuditEvent, error)
}
```

**Files to create:**
- `internal/audit/logger.go` — core logging logic
- `internal/audit/verify.go` — tamper detection
- `internal/audit/api.go` — HTTP handlers
- `internal/audit/types.go` — AuditEvent type

**Breaking changes:** None (refactored behind interface)

---

### Domain 2: `internal/jobqueue/` (LOW DEPENDENCIES)

**Current location:** jobs.go (~1,687 lines)

**Functions to extract:**
- Job struct and lifecycle
- Worker system
- Job persistence (SQLite)
- Durable job handling
- Job queue management

**Dependencies:**
- Database connection
- Config (job state file path)
- Internal job handlers (in main.go)

**New interfaces:**
```go
type JobExecutor interface {
    Execute(ctx context.Context, job *Job) error
}

type JobQueue interface {
    Enqueue(kind, actor, subject string, payload []byte, priority int) (*Job, error)
    Get(id string) (*Job, bool)
    Dequeue(types []string) (*Job, error)
    MarkComplete(id string, output []byte) error
}
```

**Files to create:**
- `internal/jobqueue/types.go` — Job, JobState types
- `internal/jobqueue/queue.go` — Enqueue, Get, Dequeue
- `internal/jobqueue/worker.go` — Worker loop, recovery
- `internal/jobqueue/persistence.go` — SQLite backend
- `internal/jobqueue/handlers.go` — Job-type dispatch

**Breaking changes:** None (interface wraps existing code)

---

### Domain 3: `internal/backup/` (MEDIUM DEPENDENCIES)

**Current location:** backups.go (~836 lines), backup-related code in helpers.go

**Functions to extract:**
- Backup creation
- Backup storage (S3, B2, local)
- Backup verification
- Offsite backup publishing
- Backup export API

**Dependencies:**
- jobqueue (backup jobs dispatched through job system)
- Sites system (per-site backups)
- Database (backup metadata)

**New interfaces:**
```go
type BackupCreator interface {
    Create(ctx context.Context, site string) (*Backup, error)
}

type BackupStorage interface {
    Store(ctx context.Context, path string, data io.Reader) error
    Retrieve(ctx context.Context, path string) (io.ReadCloser, error)
    Verify(ctx context.Context, path string) (bool, error)
}

type BackupRepository interface {
    PublishSignature(ctx context.Context, backup *Backup) error
    GetBackups(site string) ([]Backup, error)
}
```

**Files to create:**
- `internal/backup/types.go` — Backup type
- `internal/backup/creator.go` — backup creation logic
- `internal/backup/storage/local.go` — local storage
- `internal/backup/storage/s3.go` — S3 backend
- `internal/backup/storage/b2.go` — B2 backend
- `internal/backup/verify.go` — verification logic
- `internal/backup/export.go` — export API handlers
- `internal/backup/repository.go` — backup tracking

**Breaking changes:** Moderate (backup job handlers need refactoring)

---

### Domain 4: `internal/identity/` (HIGH DEPENDENCIES)

**Current location:** auth.go (~736 lines), accounts.go (~1,319 lines)

**Functions to extract:**
- Authentication (TOTP, sessions, tokens)
- Account management (creation, quotas)
- Token rate limiting
- Legacy token deprecation

**Dependencies:**
- Database (user/account data)
- jobqueue (provisioning jobs)
- audit (log auth events)
- All HTTP handlers (every endpoint checks auth)

**New interfaces:**
```go
type Authenticator interface {
    ValidateToken(token string) (*Account, error)
    ValidateTOTP(account *Account, code string) error
    CreateSession(account *Account) (string, error)
}

type AccountManager interface {
    CreateAccount(ctx context.Context, req *CreateAccountRequest) (*Account, error)
    UpdateQuotas(ctx context.Context, id string, quotas *ResourceQuotas) error
    DeleteAccount(ctx context.Context, id string) error
}
```

**Files to create:**
- `internal/identity/account.go` — Account type
- `internal/identity/auth.go` — authentication logic
- `internal/identity/token.go` — token management
- `internal/identity/quota.go` — resource quotas
- `internal/identity/totp.go` — TOTP validation

**Breaking changes:** High (every endpoint imports auth)

---

## Extraction Strategy (Per Domain)

For each domain (in order: audit → jobqueue → backup → identity):

1. **Create the package** (`internal/{domain}/`)
2. **Define interfaces** (from CLAUDE.md patterns)
3. **Extract types** (move structs, enums)
4. **Extract functions** (move logic, update imports)
5. **Update root package** (keep only routing, delegate to domain)
6. **Update tests** (rewrite with interfaces)
7. **Verify** (no breaking changes to callers)
8. **Commit** (one logical commit per file extracted)

---

## Dependency Constraints

**Must extract in order:**
1. ✅ `internal/audit/` — no internal dependencies
2. ✅ `internal/jobqueue/` — depends only on config
3. ⚠️ `internal/backup/` — depends on jobqueue, sites
4. ⚠️ `internal/identity/` — depends on everything

**Never extract:**
- ❌ Extract identity before audit/jobqueue (would break imports)
- ❌ Extract backup before jobqueue (backup jobs need queue)
- ❌ Move types without updating callers (breaks compilation)

---

## Impact per Domain

### audit/
- **Files changed:** main.go, auth.go (imports)
- **Import statements:** ~50 (audit calls scattered everywhere)
- **Risk:** Low (audit is fire-and-forget, non-blocking)

### jobqueue/
- **Files changed:** main.go, all job handlers
- **Import statements:** ~30
- **Risk:** Low (interfaces abstract the implementation)

### backup/
- **Files changed:** main.go, storage backends, API handlers
- **Import statements:** ~40
- **Risk:** Medium (backup jobs are critical, must not break)

### identity/
- **Files changed:** Nearly every file with HTTP handlers
- **Import statements:** ~150+ (every handler checks auth)
- **Risk:** High (affects authentication, requires careful testing)

---

## Rollback Plan

If extraction breaks something:

1. **Revert commits** (rollback is fast, extraction creates new branches)
2. **Identify breakage** (what changed between before/after)
3. **Fix in situ** (keep domain logic in root, extract later)
4. **Document blocker** (ARCHITECTURE_ROADMAP.md notes constraint)

---

## Next Steps

**Manual verification required:**
- [ ] Review this plan for correctness
- [ ] Confirm extraction order is acceptable
- [ ] Identify any additional dependencies I missed

**Proceed with Phase 2?**
- [ ] Extract audit/ only (low-risk proof-of-concept)
- [ ] Extract audit/ + jobqueue/ (foundation layers)
- [ ] Extract all 4 domains (full Phase 2)
- [ ] Skip Phase 2 for now (stabilize v0.7.0 first)

**Time estimate:**
- audit/ alone: 3-4 commits, 1-2 hours
- audit/ + jobqueue/: 8-10 commits, 4-6 hours
- All 4 domains: 40-60 commits, 2-3 days of careful work

---

## Related Documents

- [CLAUDE.md](../CLAUDE.md) — Code quality standards for refactoring
- [ARCHITECTURE_ROADMAP.md](ARCHITECTURE_ROADMAP.md) — Full planned structure
- [PRODUCTION_READINESS.md](PRODUCTION_READINESS.md) — Release schedule
