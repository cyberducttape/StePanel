# Package Extraction Plan

**Status:** Planning  
**Priority:** Medium (maintainability, not blocking features)  
**Estimated Effort:** 3–4 hours for full execution  
**Risk Level:** Moderate (many call sites, circular dependencies with App type)

## Motivation

The root package contains ~27k LOC across 126 files. Three subsystems are candidates for extraction into `internal/*` to improve:
- Cognitive load and testability
- Clear separation of concerns
- Reduced import cycles
- Easier onboarding for new contributors

## Phase 1: internal/backup (~2,100 LOC)

### Files to Move
- `backups.go` — HTTP handlers for backup listing/creation
- `backup_restore.go` — Restore orchestration and durable job handling
- `backup_restore_validation.go` — Archive validation logic
- `backup_retention.go` — Pruning logic
- `backup_schedule.go` — Scheduled backup registry and reconciliation
- All `*_test.go` versions of the above

### Types to Extract (public API)
```go
type BackupResult struct
type BackupRestoreResult struct  
type DatabaseSafetyBackup struct
type RestoreToStagingRequest struct
type BackupManifest struct
type durableBackupRequest struct
type durableBackupRestoreRequest struct
```

### Challenges & Mitigations

**Challenge 1: Tight coupling to App**
- `backups.go` HTTP handlers need `a.Auth`, `a.Accounts`, `a.Config`, `a.Jobs`, `a.Schedules`
- **Mitigation:** Export methods from `App` that take or return backup types:
  ```go
  func (a *App) BackupCreate(ctx context.Context, site SiteCapability, includeDatabases bool) (BackupResult, error)
  func (a *App) BackupRestore(req durableBackupRestoreRequest) (Job, error)
  ```
  Handlers remain in `backups.go` (or move to internal/backup and call these methods).

**Challenge 2: Circular dependencies**
- `internal/backup` needs to call `CreateSiteBackup()` which is also in backup code
- **Mitigation:** Keep implementation in `internal/backup`, export from there, import back into root handlers

**Challenge 3: Job integration**
- Backup jobs (`handleBackupJob`, `handleBackupRestoreJob`) are registered in `main.go`
- **Mitigation:** Export job handlers from `internal/backup`:
  ```go
  func (a *App) HandleBackupJob(ctx context.Context, item Job) ([]byte, error)
  func (a *App) HandleBackupRestoreJob(ctx context.Context, item Job) ([]byte, error)
  ```

### Implementation Strategy

1. **Create `internal/backup/types.go`**: Move all `type` definitions
2. **Create `internal/backup/create.go`**: Move `CreateSiteBackup`, `CreateSiteBackupSync`, helpers
3. **Create `internal/backup/restore.go`**: Move restore logic and validators
4. **Create `internal/backup/schedule.go`**: Move `BackupSchedules` type and methods
5. **Create `internal/backup/retention.go`**: Move `pruneSiteBackups` (1 function, easy)
6. **Create `internal/backup/jobs.go`**: Durable job handlers
7. **Update root `backups.go`**: Import from `internal/backup`, call extracted methods
8. **Update root `main.go`**: Job registration now calls `internal/backup` handlers
9. **Update tests**: Move `*_test.go` files, update imports

### Testing Strategy
- Run `go test ./...` after each file move
- Keep test files co-located with code during extraction
- No behavior changes, only refactoring

### Rollback Plan
- Each move is a Git commit; revert individual commits if issues arise
- Full rollback: `git reset --hard` before extraction started

---

## Phase 2: internal/migration (~200 LOC)

### Files to Extract
- `controlplane.go` — Control-plane DB schema and migrations (only the migration-related parts)
- Create `internal/migration/schema.go` for type definitions and version tracking

### Types to Extract
```go
type ControlPlaneMigration struct
type SchemaVersion struct
```

### Implementation Strategy
1. Create `internal/migration/schema.go` with migration types
2. Extract `migrations` map and version tracking from `controlplane.go`
3. Export `ApplyMigrations(db *sql.DB) error` from `internal/migration`
4. Keep durable-job and recovery-journal logic in root (different concerns)

### Low Risk
- Only ~200 LOC
- Minimal dependencies
- No HTTP handlers or Job integration

---

## Phase 3: internal/auth (Future)

### Scope
- Move `Auth` type and methods currently scattered across root
- Consider moving CSRF, session handling, account authorization

### Status
- Already has partial extraction (`internal/auth` exists with `auth.go`)
- This phase would deepen the extraction

---

## What NOT to Extract

**Keep in root:**
- HTTP routing and handler registration (`main.go`, routes setup)
- Config loading and validation
- Job queue orchestration (`Jobs` type in `jobs.go`)
- Site transaction and recovery-journal logic (specific to main.go's orchestration)
- Helper invocation and privilege separation (tightly coupled to Linux/service model)

---

## Execution Checklist

### Pre-extraction
- [ ] Full test suite passes
- [ ] All code compiles with `go build ./...`
- [ ] Git branch is clean (`git status`)

### During extraction (per phase)
- [ ] Create new package directory
- [ ] Move files incrementally (one file at a time if possible)
- [ ] Update imports in moved files
- [ ] Create wrapper functions/methods in root if needed
- [ ] Run tests after each move: `go test ./...`
- [ ] Commit each file move: `git commit -m "Move X to internal/Y"`

### Post-extraction
- [ ] Full test suite passes
- [ ] All code compiles with `-v` flag
- [ ] Check for any `// TODO` comments left behind
- [ ] Update CLAUDE.md or architecture docs
- [ ] Commit summary: `git commit --amend` or new summary commit

---

## Success Criteria

✓ Code compiles and all tests pass  
✓ No circular imports between packages  
✓ Public API is minimal and well-defined  
✓ Internal implementation details are hidden  
✓ Call sites in root are cleaner (fewer long function lists)  
✓ Future contributors can understand backup/migration logic in isolation  

---

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|-----------|
| Missed circular dependency | Medium | High | Run `go mod graph` after each move |
| Import path mistakes | Medium | Medium | Test compile after each file move |
| Lost test coverage | Low | High | Keep `*_test.go` files with code |
| Behavior regression | Low | Critical | Full test suite pass before/after |

---

## Future Opportunities

Once Phase 1–2 are complete:
- Extract `internal/sites` (site lifecycle, operations)
- Extract `internal/database` (managed database operations)
- Extract `internal/helper` (privilege-separated helper invocation)
- Consolidate `internal/auth` with session and account logic

Target state: Root package ~5k LOC, focused on HTTP routing, config, and orchestration.
