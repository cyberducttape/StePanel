# Root Helper Architecture: Boring, Safe, Tested

## Current State
The root helper (`stepanel-helper` helper binaries) currently:
- Executes bash/shell commands with root privilege
- Receives complex directives from the web API
- Handles network package installation (pip, npm, etc.)
- Manages archive extraction
- Orchestrates systemd services

**Problem**: Complexity creates surface area for privilege escalation.

## Target Architecture

```
┌────────────────────────────┐
│ Unprivileged StePanel API  │
│                            │
│ • Auth / tenants / jobs    │
│ • Desired state / SQLite   │
│ • Request validation       │
│ • Rate limiting            │
└─────────────┬──────────────┘
              │
       Typed request only
       No shell escaping needed
              │
┌─────────────▼──────────────┐
│ stepanel-helper            │
│ Small Go binary, root-owned│
│                            │
│ • Tiny command vocabulary  │
│ • Independent validation   │
│ • Fail-safe defaults       │
│ • No network access        │
│ • No external execution    │
└─────────────┬──────────────┘
              │
       systemd / DB / FPM / Filesystem
```

## Principles

### 1. Extreme Separation of Concerns
The helper should **only know how to**:
- Start/stop/restart systemd services
- Create/delete OS users
- Change file permissions/ownership
- Mount filesystems
- Execute pre-validated scripts (from allowlist only)

The helper should **never**:
- Execute arbitrary commands from input
- Download packages from the network
- Run package managers (pip, npm, cargo, etc.)
- Parse complex configuration formats
- Make decisions based on user input

### 2. Typed Interface, Not Shell Syntax
Current: `stepanel-appctl python-apply <site> <version> <root> <entrypoint> <port> <workers>`
- Positional arguments subject to escaping bugs
- Handler must parse and validate
- Easy to get wrong

Target: Structured requests via protocol
```go
type PythonApplyRequest struct {
    Site        string `json:"site"`        // Validated: alphanumeric
    Version     string `json:"version"`     // Validated: regex
    Root        string `json:"root"`        // Validated: ensureInside()
    Entrypoint  string `json:"entrypoint"` // Validated: regex
    Port        int    `json:"port"`        // Validated: range
    Workers     int    `json:"workers"`     // Validated: range
}
```

No shell escaping needed. Validation happens before calling helper.

### 3. Independent Validation
Helper validates every input **again**, never trusting the API:

```go
// Helper receives request
req := &PythonApplyRequest{}
// ... decode from stdin/socket

// Helper validates independently
if !isValidSiteUser(req.Site) {
    log.Fatal("invalid site")
}
if !isValidPythonVersion(req.Version) {
    log.Fatal("invalid version")
}
if err := ensureInside(dataRoot, req.Root); err != nil {
    log.Fatal("path escape attempt: ", err)
}
// ... run operation only if all checks pass
```

### 4. Fail-Safe Defaults
- Operations are opt-in, not opt-out
- Missing validation is failure, not success
- No recovery from unknown states
- Restart required for recovery

### 5. No Network Access
- Helper process runs in network namespace with no egress
- Package installation happens in build phase, not runtime
- Pre-cached wheels/dependencies only
- Prevents supply-chain attacks

### 6. Allowlist-Only External Execution
The only way the helper runs external code:
- From an immutable allowlist in the binary
- Only for essential system operations
- Each operation is audited
- No stdin/argv passed to external commands

## Implementation Roadmap

### Phase 1: Go Helper Foundation
- [ ] Create `cmd/stepanel-helper/` with Go binary
- [ ] Implement typed request protocol (protocol buffers or JSON via stdin)
- [ ] Migrate one operation: systemd service lifecycle
- [ ] Add comprehensive input validation

### Phase 2: Core Operations
- [ ] User creation/deletion
- [ ] File permission/ownership changes
- [ ] Filesystem operations (mount, umount, cleanup)
- [ ] Service orchestration

### Phase 3: Migrate from Shell
- [ ] Python/Node/PHP setup (from pre-cached dependencies only)
- [ ] Database operations (via `mysql`/`psql` only, no inline SQL)
- [ ] Archive handling (same validation as today, just better)
- [ ] Git deployment (same security, better structure)

### Phase 4: Testing & Hardening
- [ ] Adversarial test suite (hostile inputs, symlink attacks, path traversal)
- [ ] Network isolation verification
- [ ] Privilege escalation testing
- [ ] Fuzzing against request protocol

## Example: Current vs Target

### Current (Bash)
```bash
#!/bin/bash
# stepanel-appctl
site=$1
version=$2
root=$3
entrypoint=$4
port=$5
workers=$6

# Vulnerable to:
# - IFS exploits
# - Globbing
# - Variable expansion
# - Quote escaping
# - Path traversal (if root check fails)

cd "$root" 2>/dev/null || exit 1
python-deploy.sh "$site" "$version" "$entrypoint" "$port" "$workers"
```

### Target (Go)
```go
package main

import (
    "encoding/json"
    "log"
    "os"
)

type AppApplyRequest struct {
    Site       string `json:"site"`
    Version    string `json:"version"`
    Root       string `json:"root"`
    Entrypoint string `json:"entrypoint"`
    Port       int    `json:"port"`
    Workers    int    `json:"workers"`
}

func main() {
    var req AppApplyRequest
    if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
        log.Fatal("invalid request: ", err)
    }

    // Independent validation
    if err := validateRequest(&req); err != nil {
        log.Fatal("validation failed: ", err)
    }

    // Operation with no shell, no escaping needed
    if err := applyPythonApp(req); err != nil {
        log.Fatal("apply failed: ", err)
    }
}

func validateRequest(req *AppApplyRequest) error {
    if !isValidSite(req.Site) {
        return fmt.Errorf("invalid site: %q", req.Site)
    }
    if !isValidVersion(req.Version) {
        return fmt.Errorf("invalid version: %q", req.Version)
    }
    if err := ensureInside(baseDir, req.Root); err != nil {
        return fmt.Errorf("path escape: %w", err)
    }
    // ... more validation
    return nil
}

func applyPythonApp(req AppApplyRequest) error {
    // Actual operation: no shell, no escaping
    // Fail if any step errors
    return nil
}
```

## Benefits

**Security**:
- No shell escaping attacks
- No arbitrary command execution
- Clear input/output boundaries
- Typed validation everywhere
- Easier to audit

**Maintainability**:
- Type safety (compiler catches errors)
- Testable in isolation
- Clear error messages
- Logging/debugging straightforward

**Operations**:
- Boring, predictable behavior
- Crash reporting is clear
- Recovery is deterministic
- No mysterious shell behavior

## Testing Strategy

### Unit Tests
- Input validation (each field, each constraint)
- Path safety (symlinks, traversal, escapes)
- Permission checks
- Error handling

### Integration Tests
- Real systemd operations (in test container)
- File system operations (real mounts, permissions)
- Archive extraction (with adversarial payloads)
- User/group management

### Adversarial Tests
- Path traversal: `../../../etc/passwd`
- Symlink attacks: `root -> /etc`
- Null bytes in filenames
- Huge requests (DoS)
- Malformed JSON
- Valid JSON, invalid semantics
- Race conditions (concurrent requests)

### Fuzzing
- Protocol buffer fuzzing
- JSON fuzzing
- Path component fuzzing

## Timeline

**v0.7.0**: Foundation (this architecture documented)
**v0.8.0**: Phase 1 + 2 (Go helper with core operations)
**v0.9.0**: Phase 3 (All operations migrated)
**v1.0.0**: Phase 4 (Comprehensive testing, ready for production multi-tenant)

This is **not** a small refactor. It's the right architectural foundation for a production hosting platform, and it should be done carefully with extensive testing.

But once done: the boring Go binary holding the loaded gun is exactly what production needs.
