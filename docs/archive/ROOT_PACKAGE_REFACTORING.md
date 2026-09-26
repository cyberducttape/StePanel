# Root Package Refactoring Strategy

**Status:** Planning (P2 item for v0.8.0)  
**Current State:** 133 files, ~30,000 lines in root package  
**Goal:** Extract to domain-driven `internal/*` packages  
**Priority:** P2 (Quality/Maintainability)  

---

## Current Problem

### Scale Issues
- **133 Go files** in root package
- **~30,000 lines** of code
- **11 files over 600 lines** each
- **Largest files:**
  - jobs.go: 1,687 lines (job/worker management)
  - main.go: 1,410 lines (routing & startup)
  - accounts.go: 1,319 lines (tenant management)
  - git_deploy.go: 970 lines (deployment)
  - backups.go: 837 lines (backup operations)
  - wpress.go: 794 lines (WordPress-specific)
  - cloud.go: 763 lines (cloud provider integration)
  - auth.go: 758 lines (authentication/authorization)

### Consequences
1. **Hard to understand** - Monolithic package mixes HTTP handlers with business logic
2. **Hard to test** - Everything depends on everything else
3. **Hard to audit** - Security boundaries unclear
4. **Hard to maintain** - Changes ripple across many files
5. **Hard to reuse** - Domain logic locked in package main

### Code Quality Impact
- Mixed concerns (HTTP + domain + platform)
- HTTP handlers directly mutate state
- Implicit dependencies everywhere
- No clear API contracts between subsystems

---

## Target Architecture

### Domain-Driven Packages

```
cmd/stepanel/
  main.go (minimal entry point)

internal/app/
  app.go (HTTP routing)
  routes.go (route registration)
  
internal/auth/
  manager.go (authentication logic)
  policy.go (authorization policy)
  sessions.go (session management)

internal/accounts/
  manager.go (account provisioning)
  isolation.go (account isolation)
  store.go (account state)

internal/sites/
  manager.go (site lifecycle)
  provisioner.go (PHP-FPM, cgroups)
  recovery.go (recovery journals)
  store.go (site state)

internal/backups/
  manager.go (backup operations)
  verifier.go (backup verification)
  scheduler.go (backup scheduling)

internal/databases/
  manager.go (database provisioning)
  restorer.go (database restoration)
  diagnostics.go (database health)

internal/deployment/
  git.go (Git deployments)
  builds.go (Container builds)
  releases.go (Release management)

internal/importer/
  [already extracted] (archive/cpmove)

internal/platform/
  helpers.go (privileged helper execution)
  syscall.go (OS-level operations)

internal/jobs/
  [already extracted] (async job queue)

... and more domain packages
```

### Key Principles

1. **Interfaces at boundaries** - Each domain exports only interfaces
2. **No HTTP handlers in domains** - Handlers live in `internal/app/`
3. **Explicit dependencies** - Package A imports packages it needs
4. **Testable in isolation** - Each domain can be tested without HTTP
5. **Clear contracts** - Each interface documents its responsibility

---

## Refactoring Strategy

### Phase 1: Extract Non-HTTP Domains (v0.8.0, Est. 40-60h)

Extract largest, most independent domains first:

1. **internal/platform** (6-8h) - Privileged helper calls
   - Move: Helper execution, syscall wrappers, isolation
   - From: main.go, cloud.go, sites.go, databases.go
   - Impact: Foundation for other extractions

2. **internal/accounts** (8-12h) - Account provisioning
   - Move: Account creation, suspension, MFA, scoped access
   - From: accounts.go, account_management.go, auth.go (auth policy)
   - Impact: Enables account isolation testing

3. **internal/databases** (8-12h) - Database operations
   - Move: Provisioning, restoration, diagnostics
   - From: database_operations.go, backup_restore.go
   - Impact: Enables database-only testing

4. **internal/deployment** (6-10h) - Deployments
   - Move: Git deploy, container builds, releases
   - From: git_deploy.go, runner.go, apps.go
   - Impact: Enables deployment testing

5. **internal/backups** (6-8h) - Backup operations
   - Move: Backup scheduling, verification, restoration
   - From: backups.go, backup_restore.go, backup_schedule.go
   - Impact: Enables backup testing

### Phase 2: HTTP/API Layer (v0.8.0, Est. 20-30h)

Extract HTTP handlers into routing layer:

1. **internal/app/routes.go** - Endpoint registration
   - Consolidate all mux.Handle() calls
   - Organize by domain
   - Clear route -> handler mapping

2. **internal/app/handlers.go** - Request/response handling
   - Move: HTTP middleware, request parsing, response formatting
   - Keep: Domain-specific logic delegated to services

### Phase 3: Remaining Domains (v0.8.0+, Est. 30-50h)

1. **internal/migration** - cPanel/WordPress migrations
2. **internal/resources** - Resource management (cgroups, quotas)
3. **internal/recovery** - Recovery journal handling
4. **internal/integration** - Cloud provider integrations
5. **internal/observability** - Metrics, logging, audit

### Phase 4: Root Package Cleanup (v0.8.1, Est. 10-20h)

- Rename `main()` entry point to `cmd/stepanel/main.go`
- Move remaining utilities to `internal/`
- Final test suite integration
- Documentation update

---

## Implementation Guidelines

### Extracting a Domain: Step-by-Step

**Example: Extract `internal/sites/manager.go`**

1. **Define interface** (what the HTTP layer needs):
```go
// internal/sites/manager.go
type Manager interface {
    Create(ctx context.Context, req *CreateRequest) (*Site, error)
    Delete(ctx context.Context, name string) error
    Update(ctx context.Context, name string, req *UpdateRequest) error
    GetStatus(ctx context.Context, name string) (*SiteStatus, error)
}

type CreateRequest struct {
    Name       string
    PHPVersion string
    Owner      string
}
```

2. **Implement the manager** (move all site-related logic):
```go
type defaultManager struct {
    platform  platform.Helper  // Injected dependency
    store     *siteStore       // Internal state
    resources *resourceManager // Domain service
}

func (m *defaultManager) Create(ctx context.Context, req *CreateRequest) (*Site, error) {
    // Move CreateSite logic from main.go here
    // Use only injected dependencies and internal state
}
```

3. **Update HTTP handlers** (keep only HTTP concerns):
```go
// In internal/app/handlers.go
func (a *App) createSite(w http.ResponseWriter, r *http.Request) {
    // Parse request
    var req sites.CreateRequest
    json.NewDecoder(r.Body).Decode(&req)
    
    // Delegate to domain service
    site, err := a.siteManager.Create(r.Context(), &req)
    if err != nil {
        http.Error(w, err.Error(), 400)
        return
    }
    
    // Format response
    json.NewEncoder(w).Encode(site)
}
```

4. **Update main.go** (inject dependencies):
```go
// In main.go startup
siteManager := sites.NewManager(
    platformHelper,
    accountStore,
    resourceManager,
)

app.siteManager = siteManager
```

5. **Add tests** (test domain in isolation):
```go
// In internal/sites/manager_test.go
func TestCreateSite_InvalidName(t *testing.T) {
    m := &mockManager{} // Test without HTTP
    _, err := m.Create(ctx, &CreateRequest{Name: ""})
    if err == nil {
        t.Error("expected error for empty site name")
    }
}
```

### Testing Strategy

Each extracted domain gets:
- **Unit tests** - Domain logic in isolation
- **Integration tests** - With mocked platform helpers
- **End-to-end tests** - Via HTTP handlers

### Breaking Changes Minimization

- Keep HTTP API identical (same routes, responses)
- All changes internal to root package
- No version bump needed (v0.7.x compatibility)

---

## Success Metrics

- [ ] Root package reduced from 133 to <30 files
- [ ] All files <500 lines except main.go
- [ ] Each domain exports clear interface
- [ ] No circular dependencies
- [ ] 100% test coverage for extracted domains
- [ ] HTTP handlers <200 lines each
- [ ] Main.go focused on setup/teardown
- [ ] Documentation updated with package layout

---

## Timeline Estimate

| Phase | Domains | Effort | Timeline |
|-------|---------|--------|----------|
| 1 | 5 core | 40-60h | v0.8.0 (2-3 weeks) |
| 2 | HTTP layer | 20-30h | v0.8.0 |
| 3 | Remaining | 30-50h | v0.8.0+ |
| 4 | Cleanup | 10-20h | v0.8.1 |
| **Total** | **10+** | **100-160h** | **2-3 months** |

---

## Phased Rollout

### MVP (v0.8.0): Platform + Accounts + Databases
- Extract the 3 most impactful domains (40-50h)
- Reduces coupling, improves testability
- Clear path for remaining extractions
- Still shipping features

### Next (v0.8.1): Deployments + Backups
- Extract 2 more domains (20-30h)
- Further improve testability
- Make foundation for v0.9 multi-tenant work

### Long-term (v0.9+): Complete refactoring
- Extract remaining domains
- Full domain-driven architecture
- Ready for scaling and feature expansion

---

## Related Decisions

- **ADR-0005**: [Incremental Go Package Boundaries](./adr/0005-incremental-go-package-boundaries.md)
- **ARCHITECTURE_ROADMAP.md**: Long-term package organization plan
- **CLAUDE.md**: Code organization requirements for new code

---

## Benefits

### Immediate (v0.8.0)
- ✅ Easier to understand (smaller files)
- ✅ Easier to test (isolated domains)
- ✅ Clearer boundaries (explicit interfaces)
- ✅ Faster reviews (smaller changesets)

### Long-term (v0.9.0+)
- ✅ Multi-tenant support (account isolation)
- ✅ Distributed deployment (domain services)
- ✅ Better error handling (clear contracts)
- ✅ Reusable components (internal packages)
- ✅ Community contributions (clear extension points)

---

## Risk Mitigation

### Potential Issues
1. **Circular dependencies** - Mitigated by interface-based design
2. **Test complexity** - Mitigated by incremental extraction
3. **Compilation time** - No impact (still single main.go)
4. **Runtime performance** - No impact (function calls unchanged)

### Safeguards
- Keep same HTTP API (no version bump)
- Comprehensive integration tests during extraction
- Feature flag any internal API changes
- Revert strategy: Keep old code until new is tested

---

## Next Steps

1. **Immediate (this session):**
   - ✅ Create this refactoring strategy
   - ✅ Identify extraction targets
   - 📋 Prioritize phases

2. **v0.8.0 planning:**
   - Extract Phase 1 (5 core domains, 40-60h)
   - Add integration tests
   - Update documentation

3. **Implementation:**
   - Start with `internal/platform` (foundation)
   - Then `internal/accounts` and `internal/databases`
   - Add tests for each domain
   - Iterate to other domains

---

## See Also

- [ARCHITECTURE.md](./ARCHITECTURE.md) - Current system design
- [ARCHITECTURE_ROADMAP.md](./ARCHITECTURE_ROADMAP.md) - Planned package structure
- [adr/0005](./adr/0005-incremental-go-package-boundaries.md) - Architecture decision record

