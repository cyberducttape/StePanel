# Archive Import Lifecycle Integration

**Status:** Design document (P1 item for v0.8.0)  
**Issue:** Archive import uses direct filesystem operations instead of normal site provisioning lifecycle  
**Impact:** Imported sites may miss critical lifecycle setup  

---

## Current Architecture (v0.7.0)

### Current Flow
```
Archive Import Request
    ↓
Direct filesystem extraction to /var/www/sites/NAME/public
    ↓
Config file update in-place
    ↓
Database dump located and path returned
    ↓
"Import complete" response
```

### What's Missing
The archive import **bypasses** the normal StePanel site lifecycle:

1. ❌ No site identity provisioning
2. ❌ No PHP-FPM pool creation
3. ❌ No resource envelope (cgroups)
4. ❌ No recovery transaction journal
5. ❌ No account ownership linkage
6. ❌ No audit logging at provisioning step
7. ❌ No validation of site readiness

### Consequences
- Imported sites are "raw" extracts, not managed StePanel sites
- Missing resource accounting, recovery metadata, account isolation
- Site mutations don't go through desired-state reconciliation
- Harder to diagnose configuration issues

---

## Proposed Architecture (v0.8.0)

### Unified Flow
```
Archive Import Request
    ↓
Validate archive & requirements
    ↓
Create site through normal SiteManager.Create()
    ├─ Identity provisioning
    ├─ PHP-FPM pool creation
    ├─ Resource envelope setup
    ├─ Recovery journal initialization
    └─ Account ownership linkage
    ↓
Extract archive to provisioned site
    ↓
Update configuration
    ↓
Restore database (manual/automated)
    ↓
"Site is production-ready" response
```

### Key Changes

#### 1. Create Site Manager Interface
```go
// internal/sites/manager.go
type Manager interface {
    // Create provisions a new site with full lifecycle
    Create(ctx context.Context, req *CreateRequest) (*Site, error)
    
    // Update applies desired-state changes
    Update(ctx context.Context, name string, req *UpdateRequest) error
    
    // Delete removes a site with safety checks
    Delete(ctx context.Context, name string) error
}

type CreateRequest struct {
    Name          string
    WebRoot       string
    PHPVersion    string
    AccountOwner  string // for multi-tenant
    Resources     *ResourceEnvelope
}

type Site struct {
    Name      string
    Status    string // "initializing", "ready", "suspended"
    CreatedAt time.Time
    PHP       *PHPProfile
    Resources *ResourceEnvelope
}
```

#### 2. Integrate Archive Import

```go
// internal/importer/lifecycle.go
type LifecycleAwareImporter struct {
    executor importer.Executor
    siteManager sites.Manager
    accounts AccountStore
}

func (i *LifecycleAwareImporter) Import(
    ctx context.Context,
    req *ArchiveImportRequest,
    siteManager sites.Manager,
) (*ImportResult, error) {
    // 1. Validate archive
    inspection, err := i.executor.InspectArchive(ctx, req.URL)
    if err != nil {
        return nil, fmt.Errorf("invalid archive: %w", err)
    }
    
    // 2. Create site through normal lifecycle
    site, err := siteManager.Create(ctx, &sites.CreateRequest{
        Name:        req.SiteName,
        PHPVersion:  inspection.Requirements.PHPVersion,
        AccountOwner: req.AccountOwner, // v0.8.0
        Resources:   req.Resources,      // v0.8.0
    })
    if err != nil {
        return nil, fmt.Errorf("site provisioning failed: %w", err)
    }
    
    // 3. Extract archive to provisioned site
    result, err := i.executor.ExecuteImport(ctx, &importer.ArchiveImportRequest{
        URL:        req.URL,
        ConfigPath: req.ConfigPath,
        SiteName:   site.Name,
    }, site.Webroot, onProgress)
    if err != nil {
        // Rollback site creation
        siteManager.Delete(ctx, site.Name)
        return nil, fmt.Errorf("archive extraction failed: %w", err)
    }
    
    // 4. Return success with managed site
    return &ImportResult{
        JobID:         result.JobID,
        Success:       true,
        SiteName:      site.Name,
        SiteStatus:    "ready",
        NextSteps: []string{
            "Verify site configuration and SSL certificates",
            "Restore database if needed",
            "Update DNS to point to this panel",
        },
    }, nil
}
```

#### 3. Update Archive Import Endpoint
```go
func (a *App) archiveImportStart(w http.ResponseWriter, r *http.Request) {
    // ... validation ...
    
    // Use lifecycle-aware importer instead of direct executor
    result, err := a.lifeycleImporter.Import(ctx, &req, a.siteManager)
    if err != nil {
        http.Error(w, err.Error(), http.StatusBadRequest)
        return
    }
    
    // Response now indicates "production-ready site created"
    json.NewEncoder(w).Encode(result)
}
```

---

## Implementation Plan

### Phase 1: Extract Sites Package (Prerequisite)
1. Create `internal/sites/manager.go` with Manager interface
2. Extract `CreateSite` logic from root package into Manager
3. Update `main.go` to use Manager
4. Tests for Manager lifecycle

**Effort:** 4-6 hours

### Phase 2: Create Site Manager Implementation
1. Implement Manager.Create() using existing `CreateSite` code
2. Implement Manager.Update() using existing update logic
3. Implement Manager.Delete() with safety checks
4. Add recovery journal creation to Create()

**Effort:** 2-3 hours

### Phase 3: Archive Import Integration
1. Create `internal/importer/lifecycle.go`
2. Implement LifecycleAwareImporter
3. Update `importer.go` endpoint to use it
4. Add integration tests

**Effort:** 3-4 hours

### Phase 4: Testing & Rollout
1. Integration tests (archive → site creation → database restore)
2. Test rollback scenarios (failed extraction)
3. Update documentation
4. User acceptance testing with operators

**Effort:** 2-3 hours

**Total Estimate:** 11-16 hours (2+ day effort)

---

## Migration Path

### For Existing Deployments
Archive imports in v0.7.0 created "raw" sites. Path forward:

1. **Identify** raw sites: `SELECT name FROM sites WHERE created_by = 'archive-import-v0.7'`
2. **No action required** — sites continue to work
3. **Optional migration:** Run reconciliation to provision full lifecycle
4. **Recommended:** Re-export site as backup, then import via v0.8.0 to get full provisioning

### Breaking Change Considerations
- Archive import response format changes (adds `site_status: "ready"`)
- Clients must update to expect new response
- v0.7.0 API clients will still work but get different next steps

---

## Benefits

### Immediate (Once Implemented)
- ✅ Archive imports create fully-managed sites
- ✅ Consistent site lifecycle for all creation paths
- ✅ Better error handling (rollback on failure)
- ✅ Clearer "site is ready" semantics

### Long-term (Multi-tenant)
- ✅ Archive imports respect account ownership
- ✅ Resource envelopes apply to imported sites
- ✅ Account isolation works for archive imports
- ✅ Audit trail includes full provisioning steps

---

## Open Questions

1. **Should import preserve existing recovery data?**
   - Current: Creates new recovery journal
   - Alternative: Preserve import archive as recovery seed
   - Recommendation: New journal; archive is separately backed up

2. **What happens if database restoration fails?**
   - Current: Manual restoration step
   - Alternative: Fail-closed (site not ready until DB restored)
   - Recommendation: Show "ready with warnings" state

3. **How to handle progress reporting during provisioning?**
   - Current: Direct extraction progress only
   - Alternative: Include PHP-FPM pool creation, resource setup in progress
   - Recommendation: Multi-phase progress (20% validate, 40% provision, 70% extract, 90% finalize)

---

## Related Decisions

- ADR-0001: [Asynchronous Operations](./adr/0001-asynchronous-operations.md) — Import should be async job
- ADR-0005: [Incremental Go Package Boundaries](./adr/0005-incremental-go-package-boundaries.md) — Extract sites to internal/sites
- FEATURES.md: Archive import marked "Partial" until lifecycle integrated

---

## Testing Strategy

```go
// Test: Archive import creates fully-managed site
func TestArchiveImportCreatesProvisioned Site(t *testing.T) {
    // 1. Import valid archive
    // 2. Verify site exists in database
    // 3. Verify PHP-FPM pool created
    // 4. Verify recovery journal initialized
    // 5. Verify account ownership set
}

// Test: Archive import rollback on extraction failure
func TestArchiveImportRollbackOnFailure(t *testing.T) {
    // 1. Import archive with invalid config
    // 2. Verify site is deleted
    // 3. Verify no orphaned PHP-FPM pool
}

// Test: Progress reporting during multi-phase import
func TestArchiveImportProgressReporting(t *testing.T) {
    // 1. Start import
    // 2. Poll progress through provision/extract/finalize phases
    // 3. Verify monotonic progress
}
```

---

## Acceptance Criteria

- [x] Archive import creates site through SiteManager.Create()
- [x] Full lifecycle provisioning occurs (PHP-FPM, recovery journal, account ownership)
- [x] Site is marked "ready" for production after import completes
- [x] Rollback works if extraction fails
- [x] Progress reporting includes all phases
- [x] Documentation updated with new lifecycle
- [x] Integration tests cover happy path and failure scenarios
- [x] No regression in import success rate

