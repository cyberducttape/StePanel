# Helper Operation Timeout Classes (HIGH)

## Issue: Universal 2-Minute Timeout for All Operations

**Problem**: `HelperCommandTimeout = 2 * time.Minute` (helpers.go:17) applies to all helper operations, which is wildly inappropriate for long-running work.

**Current behavior**:
```go
const helperCommandTimeout = 2 * time.Minute

// Applied universally to:
// - Caddy reload (OK: <10 sec)
// - PHP-FPM pool create (OK: <10 sec)
// - Package install (WRONG: 10-30 min)
// - Node build (WRONG: 15+ min)
// - Composer update (WRONG: 20+ min)
// - Container pull (WRONG: 30+ min)
// - Database restore (WRONG: 60+ min)
```

**Impact**: Long-running operations fail with timeout errors, appear to fail in logs but continue running (due to process group issue), creating orphaned processes.

---

## Solution: Operation-Specific Timeouts

Define timeout classes based on operation characteristics:

```go
const (
    // Fast config mutations: reload config, create pool, apply ACL
    ConfigMutationTimeout = 30 * time.Second

    // Service lifecycle: start/stop service, create user, configure filesystem
    ServiceLifecycleTimeout = 60 * time.Second

    // Package/build jobs: npm install, composer update, pip install, cargo build
    PackageBuildTimeout = 15 * time.Minute

    // Database operations: dump, restore, migration
    DatabaseOperationTimeout = 60 * time.Minute

    // Container operations: pull image, load archive, prune volumes
    ContainerOperationTimeout = 30 * time.Minute

    // Backup/restore: site backup, full site restore
    BackupRestoreTimeout = 120 * time.Minute
)
```

---

## Implementation Phases

### Phase 1: Timeout Class System
1. Define timeout classes (above constants)
2. Update `RunHelperCommand()` signature to accept timeout parameter
3. Update all call sites to pass operation-specific timeout
4. Default to `ConfigMutationTimeout` (safest for unknown ops)

### Phase 2: Operation Classification
Review all helper invocations and assign appropriate timeouts:
- `appctl python-apply` → PackageBuildTimeout
- `appctl npm install` → PackageBuildTimeout
- `runnerctl build` → PackageBuildTimeout (15 min)
- `vhostctl` config operations → ConfigMutationTimeout
- `fpm-pool-create` → ServiceLifecycleTimeout
- `db-restore` → DatabaseOperationTimeout
- `backup create` → BackupRestoreTimeout

### Phase 3: Async for Long Operations (Recommended)
**Better solution**: Don't execute long-running work inline through helper abstraction at all.

Move to durable jobs:
- `npm install` → job (worker process, no timeout)
- `composer update` → job
- `pip install` → job
- `container pull` → job
- `database restore` → job (already done for backups)
- `backup create` → job (already done)

Benefits:
- Process doesn't block API response
- Can restart without losing progress
- Better resource isolation
- Parallel execution
- Can run on dedicated workers

---

## Current Helper Invocations

Operations that need timeout review:

```
appctl python-apply            → PackageBuildTimeout (pip install)
appctl node-tool install       → PackageBuildTimeout (npm/yarn/pnpm)
appctl wordpress restore       → PackageBuildTimeout (wp-cli, composer)
runnerctl build                → PackageBuildTimeout (arbitrary builds)
dbctl restore                  → DatabaseOperationTimeout
vhostctl reload/create         → ConfigMutationTimeout
fpm-pool-create                → ServiceLifecycleTimeout
certbot renew                  → ServiceLifecycleTimeout
podman operations              → ContainerOperationTimeout
git operations                 → ConfigMutationTimeout
```

---

## Call Stack

```
runHelperCommand(ctx, cfg, path, args...)
    ↓
HelperCommandContext(ctx, cfg.Sudo, path, args...)
    ↓
exec.CommandContext(ctx, sudo, args...)
    ↓
cmd.Run()
```

**Required change**: Pass timeout value to `RunHelperCommand()`:

```go
// Before:
RunHelperCommand(ctx, cfg, path, args...)

// After:
RunHelperCommand(ctx, cfg, ConfigMutationTimeout, path, args...)
```

---

## Security Implications

### Risk: Timeout Too Short
- Operations fail prematurely
- Incomplete state (half-installed package, partial data)
- Retries cascade into more failures
- User experience: "operation failed repeatedly"

### Risk: Timeout Too Long  
- Blocked API requests accumulate
- DOS via slow operations
- Combined with process group issue: orphaned processes

### Risk: No Timeout
- Runaway process exhausts resources
- Deadlock scenarios
- Memory leaks

**Balance**: Choose per-operation based on typical execution time + 50% margin.

---

## Monitoring

Add metrics for timeout enforcement:
- `helper_operation_timeout_seconds` by operation class
- `helper_operation_exceeded_timeout` (counter)
- `helper_operation_duration_seconds` by operation (histogram)

Use histogram percentiles to validate timeout choices:
- p50, p95, p99 of operation duration
- If p99 > timeout, increase timeout

---

## Testing

Add tests for timeout behavior:
- Fast operation completes before timeout (passes)
- Slow operation hits timeout (fails as expected)
- Process group cleanup works correctly on timeout

---

## References

- `internal/helper/helpers.go:17-18` - current universal timeout
- `internal/helper/helpers.go:110-120` - RunHelperCommand implementation
- Related issue: Process group cleanup (helpers.go:72-80)
- Related issue: Startup reconciliation blocking (main.go:373-388)
