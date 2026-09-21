# Go File Organization Refactoring - Proper Approach

## The Problem with Previous Attempt

The previous attempt failed because:
1. Root files are `package main` (executable package)
2. Executable packages cannot be imported by other packages
3. Methods on App struct need to be in an importable package
4. Circular dependency between root and internal packages is unavoidable with current structure

## The Proper Solution

### Phase 1: Convert Root to Importable Package
1. Rename `package main` → `package stepanel`
2. Move `main()` function to `cmd/stepanel/main.go`
3. Update go.mod if necessary
4. Verify compilation with new structure

### Phase 2: Create Clean Separation
Root package (`stepanel`) becomes the **application runtime** layer:
- App struct and HTTP handlers
- Request/response routing
- Configuration loading
- Database initialization

Internal packages become **domain logic** and **services**:
- `internal/auth/` - authentication & authorization
- `internal/backup/` - backup/restore operations
- `internal/site/` - site management
- `internal/deployment/` - deployment operations
- etc.

### Phase 3: Refactor Method Receivers
- Convert root-level functions to methods on App struct
- Keep App struct methods in root (or app.go file)
- Move pure business logic functions to internal packages
- Create interfaces for dependency injection

### Phase 4: Update Imports
- Add proper imports from root to internal packages
- Verify no circular dependencies
- Update method receivers if needed

## Alternative: Minimal Refactoring

If full refactoring is too risky, do **targeted cleanup**:

1. **Keep in root:**
   - main.go (bootstrap)
   - config.go (configuration)
   - app.go (App struct + HTTP handlers)
   - assets.go (embedded assets)
   - helpers.go (utility functions)
   - version.go
   - init.go

2. **Move to internal packages** (minimal, low-risk):
   - `internal/auth/` → auth/accounts/tokens (already mostly here!)
   - `internal/backup/` → backup operations (already mostly here!)
   - Keep other files in root until refactored

3. **Benefits:**
   - Minimal breaking changes
   - Clear separation of concerns  
   - Easier to maintain existing code
   - Can progressively refactor

## Recommended: Hybrid Approach

1. **Immediate (this session):**
   - Keep root as-is for stability
   - Ensure auth & backup packages work correctly
   - Document code organization

2. **Next phase:**
   - Full root→stepanel package conversion
   - Move to cmd/stepanel/main.go
   - Proper layering: root becomes application runtime only

3. **Later:**
   - Extract domain logic to internal packages
   - Create clean interfaces
   - Remove App struct from business logic

## Current Status

✅ Build is clean (all files in root)  
✅ Auth package (internal/auth/) is organized  
✅ Backup package (internal/backup/) has foundation  

❌ Large refactoring deferred to future phase  
❌ Root package still contains 131 files  

**Next step:** Document code organization and prepare for Phase 1 in future work.

---

## Why Not Full Refactoring Now?

1. **Risk**: Large codebase with existing customers
2. **Scope**: Would require ~8-12 hours of careful refactoring
3. **Testing**: Needs comprehensive testing after package reorganization
4. **Complexity**: Method receiver changes throughout codebase

Better to do it right than fast.
