# StePanel Code Quality Standards & Architecture Guidelines

This document establishes the code quality expectations and architectural patterns for StePanel development.

**Last Updated:** 2026-09-25  
**Scope:** All new code and refactored subsystems must follow Tier 1 standards

## Tier 1 Code Quality Standards

StePanel's mature subsystems (durable jobs, audit logging, backups, account isolation) demonstrate the quality bar for all new code. All code must meet these standards:

### 1. Filesystem Operations

**MUST:**
- ✅ Check and handle all error returns from `os.Create`, `os.Mkdir`, `os.MkdirAll`
- ✅ Use atomic writes: temp file + rename, not direct overwrites
- ✅ Preserve file permissions from source (don't reset to 0644)
- ✅ Validate all paths stay within intended root directory
- ✅ Reject absolute paths, `..` sequences, symlink traversal

**MUST NOT:**
- ❌ Ignore `os.MkdirAll()` errors with bare `_ =`
- ❌ Unconditionally reset file mode (e.g., `0644` over `0600`)
- ❌ Trust user-supplied paths without validation
- ❌ Follow symlinks to targets outside root

**Example:**
```go
// ✅ Good
info, err := os.Stat(configFile)
if err != nil {
    return fmt.Errorf("cannot stat config: %w", err)
}
if !info.Mode().IsRegular() {
    return fmt.Errorf("not a regular file")
}
// Preserve original permissions
if err := os.Chmod(tempFile, info.Mode()); err != nil {
    return fmt.Errorf("cannot restore permissions: %w", err)
}

// ❌ Bad
os.MkdirAll(path, 0755)  // Error ignored!
os.WriteFile(file, data, 0644)  // Unconditional mode reset
```

### 2. Archive Extraction

**MUST:**
- ✅ Validate all entry types (reject unknown, devices, FIFOs, sockets)
- ✅ Preserve file executable bits from archive metadata
- ✅ Enforce size limits per file and total
- ✅ Reject symlinks, hardlinks, sparse files
- ✅ Check for directory bombs (file/directory count limits)

**MUST NOT:**
- ❌ Treat unrecognized tar types as normal files
- ❌ Reset extracted file permissions to defaults
- ❌ Trust archive structure without validation

**Example:**
```go
// ✅ Good: explicit type handling
switch header.Typeflag {
case tar.TypeReg, tar.TypeRegA:
    // Extract regular file
    archiveMode := os.FileMode(header.Mode)
    safeMode := 0644 | (archiveMode & 0111)  // Preserve executable bits
    os.Chmod(targetPath, safeMode)
case tar.TypeDir:
    // Create directory
    os.MkdirAll(targetPath, os.FileMode(header.Mode&0755))
default:
    return fmt.Errorf("unsupported tar type %v for %s", header.Typeflag, header.Name)
}

// ❌ Bad: implicit type handling
if header.Typeflag == tar.TypeDir {
    os.MkdirAll(targetPath, 0755)
} else {
    os.Create(targetPath)  // Could extract device node!
}
```

### 3. Configuration Management

**MUST:**
- ✅ Validate paths before use (no traversal, not absolute)
- ✅ Use proper parsing for config formats (not text replacement)
- ✅ Detect function-call values (getenv, env) and skip replacement
- ✅ Handle whitespace variation in config syntax
- ✅ Validate config file is regular file (not symlink/directory)

**MUST NOT:**
- ❌ Use brittle string replacement on config files
- ❌ Trust user-supplied config paths without validation
- ❌ Overwrite values that come from environment/function calls
- ❌ Follow symlinks in config file paths

**Example:**
```go
// ✅ Good: safe path validation
if filepath.IsAbs(configPath) {
    return fmt.Errorf("config path cannot be absolute")
}
if strings.Contains(configPath, "..") {
    return fmt.Errorf("config path contains traversal")
}
if err := h.EnsureInside(webRoot, resolvedPath); err != nil {
    return fmt.Errorf("config escapes site root: %w", err)
}
info, err := os.Stat(configFile)
if err != nil || !info.Mode().IsRegular() {
    return fmt.Errorf("invalid config path")
}

// ❌ Bad: unsafe text replacement
content = strings.Replace(content, 
    "define('DB_NAME', 'old')",
    "define('DB_NAME', 'new')", 1)
// What if value is getenv('DB_NAME')? Silent failure.
```

### 4. Error Handling

**MUST:**
- ✅ Check error returns from all system calls
- ✅ Wrap errors with context (`fmt.Errorf(...%w)`)
- ✅ Return errors early, don't continue on failure
- ✅ Fail fast on critical operations (don't mask with default values)

**MUST NOT:**
- ❌ Ignore errors with `_ = operation()`
- ❌ Continue after errors in critical paths
- ❌ Silently default to fallback values when operations fail

**Example:**
```go
// ✅ Good
if err := os.MkdirAll(dir, 0755); err != nil {
    return fmt.Errorf("failed to create directory: %w", err)
}

// ❌ Bad
_ = os.MkdirAll(dir, 0755)  // Silently fails, continues
```

### 5. Progress Reporting

**MUST:**
- ✅ Ensure progress values are monotonically increasing
- ✅ Use percentage calculations, not modulo
- ✅ Cap at maximum when total unknown (don't fake precision)
- ✅ Report actual progress, not fake precision

**MUST NOT:**
- ❌ Use modulo operations that cause backwards jumps
- ❌ Report fake percentages without knowing denominator
- ❌ Allow progress to regress as operation continues

**Example:**
```go
// ✅ Good: monotonic progress
fileProgress := job.FilesExtracted
if fileProgress > 1000 {
    fileProgress = 1000
}
job.Progress = 20 + int(40*fileProgress/1000)  // Monotonic 20-60%

// ❌ Bad: backwards jumps
job.Progress = 20 + int(job.FilesExtracted%40)  // Jumps 20→59→20→59...
```

### 6. User-Facing Output

**MUST:**
- ✅ Use generic, product-appropriate messaging
- ✅ Reference configured hostnames, not examples
- ✅ Provide actionable next steps

**MUST NOT:**
- ❌ Hardcode example domains (panel.example.com)
- ❌ Show placeholder URLs to users
- ❌ Reference unfinished/future features as current

**Example:**
```go
// ✅ Good
nextSteps := []string{
    "View the imported site in the StePanel site overview at /sites",
    "Verify site configuration and SSL certificates",
}

// ❌ Bad
nextSteps := []string{
    "Verify site loads at https://panel.example.com/site/" + siteName,
}
```

## Domain Boundary Interfaces

Extract new domains with clear interface contracts. This enables testing, mocking, and failure injection.

## Architectural Rule: Site Lifecycle Authority

**Canonical sites** (permanent workspaces at `{webRoot}/sites/{siteName}/*`) may ONLY be created, activated, or deleted by `internal/sites.Manager`. No exceptions.

**Staging areas** (temporary workspaces granted by SiteManager) may be mutated by domain components, but:
- Must be obtained via `SiteManager.GrantStaging()`
- Must be activated/discarded only via `SiteManager.ActivateStaged()` or `SiteManager.DiscardStaging()`
- Must never persist paths or assume existence across operation boundaries

This rule prevents inconsistent lifecycle setup (missing PHP-FPM, recovery journals, account ownership, resource envelopes, audit trails) across different site creation paths.

### Required Interfaces for Privileged Operations

```go
// Site lifecycle operations
type SiteManager interface {
    Create(ctx context.Context, req *CreateSiteRequest) error
    Delete(ctx context.Context, name string) error
    UpdateResources(ctx context.Context, name string, limits *ResourceLimits) error
}

// Database operations
type DatabaseManager interface {
    CreateDatabase(ctx context.Context, site string, spec *DatabaseSpec) error
    RestoreDatabase(ctx context.Context, site string, dump io.Reader) error
    ValidateConnectivity(ctx context.Context, site string) error
}

// Service management
type ServiceManager interface {
    RestartPHP(ctx context.Context, site string) error
    RestartWebserver(ctx context.Context, site string) error
    StopAllServices(ctx context.Context, site string) error
}

// Archive handling
type ArchiveFetcher interface {
    Fetch(ctx context.Context, url string, maxSize int64) (io.ReadCloser, error)
    Validate(ctx context.Context, archive io.Reader, format string) (*ArchiveInfo, error)
}
```

### Benefits

- **Testability**: Mock interfaces in unit tests
- **Failure injection**: Test error paths (network failures, disk full, etc.)
- **Dependency clarity**: Package dependencies are explicit
- **Refactoring safety**: Change implementations without affecting callers

## Code Review Checklist

Before merge, verify:

### Security
- [ ] All filesystem operations have error checking
- [ ] All user-supplied paths validated (no traversal, not absolute)
- [ ] Archive entries validated by type
- [ ] File permissions preserved from source
- [ ] No hardcoded secrets, example domains, or placeholder URLs

### Reliability
- [ ] All error returns checked
- [ ] Progress calculations monotonic (never regress)
- [ ] No ignored errors with bare `_`
- [ ] Atomic writes used for configuration
- [ ] All size/count limits enforced

### Architecture
- [ ] Uses domain interfaces where applicable
- [ ] Clear separation of concerns
- [ ] No circular dependencies between packages
- [ ] Related functionality grouped in same package

### Testing
- [ ] Happy path tested
- [ ] Error conditions tested
- [ ] Edge cases covered (empty, oversized, malformed input)
- [ ] Destructive operations have failure injection tests

## Package Organization

See [ARCHITECTURE_ROADMAP.md](docs/ARCHITECTURE_ROADMAP.md) for planned internal package organization.

New code should be placed in appropriate `internal/` package based on domain, not in root package.

## Package Refactoring Strategy

### Current State

The root package contains ~150 Go files (~2.5MB), which creates maintenance and review challenges:
- Hard to locate authorization checks (scattered across files)
- Hard to understand privilege boundaries
- Hard to trace which code mutations canonical site state
- Slows down security and architecture reviews

The mature subsystems (sites, jobs, auth, backups) prove the value of clear boundaries.

### Refactoring Goal

Organize root package into domain-specific packages under `internal/`:

```
internal/
  accounts/      ← Customer identity and site ownership
  security/      ← Authentication, CSRF, scopes, capabilities
  http/          ← HTTP handlers organized by domain
  sites/         ← Site lifecycle (already exists - keep expanding)
  deployment/    ← Git deploy, release pipeline, artifacts
  backup/        ← Backup, restore, archive operations
  database/      ← Database provisioning, restoration
  jobs/          ← Durable job system
  ...
```

### Strategy: Incremental Extraction (Not a Big Rewrite)

Instead of rewriting large sections:

1. **Extract domains as they're touched**: When modifying feature X, extract its domain package
2. **Keep HTTP handlers unified initially**: Extract auth/accounts/sites, but leave HTTP wiring in root temporarily
3. **Validate with tests**: Before committing a large extraction, verify no functionality breaks
4. **Use bridge functions**: Have root provide dependency injection until domains are fully separated

### Why This Works

- **No disruptive rewrite**: Working code stays working
- **Incremental risk**: Test each extraction independently
- **Clear ownership**: As domains extract, their boundaries become self-evident
- **Measurable progress**: Each extraction reduces root package size

### Not Recommended

❌ **Single big refactor** of all 150 files at once — too risky, too expensive
❌ **Forced artificial splits** that don't match actual domain boundaries
❌ **Leaving root code in internal/** — that defeats the purpose

### Example: Accounts Domain Extraction

```go
// Before: HTTP handler + account logic in root/accounts.go
func (a *App) accounts(w http.ResponseWriter, r *http.Request) {
    account, err := a.Accounts.Update(...)  // domain logic mixed with HTTP
}

// After: Domain logic in internal/accounts/, HTTP handler in root/accounts.go
func (a *App) accounts(w http.ResponseWriter, r *http.Request) {
    account, err := a.accountsService.Update(...)  // delegate to domain
}

// internal/accounts/service.go
type Service struct { store *Store }
func (s *Service) Update(...) { ... }
```

The extraction happens **one domain at a time**, with clear interfaces between root and internal packages.

## Standards Enforcement

- **Pre-commit hook** (planned): Syntax check, format check
- **CI gates**: Code review checklist automated where possible
- **Merge requirements**: Code review approval required
- **Rollout**: Apply Tier 1 standards to all new code; refactor Tier 2 subsystems incrementally

## Related Documents

- [ARCHITECTURE_ROADMAP.md](docs/ARCHITECTURE_ROADMAP.md) — Planned package organization
- [PRODUCTION_READINESS.md](docs/PRODUCTION_READINESS.md) — Release criteria
- [SECURITY.md](docs/SECURITY.md) — Security boundaries and threat model
- [CONTRIBUTING.md](CONTRIBUTING.md) — Contribution workflow

---

**Questions?** Open an issue or discuss in PR reviews.

**Non-compliance?** Not a blocker for review, but will be tracked for refactoring Phase 2+.
