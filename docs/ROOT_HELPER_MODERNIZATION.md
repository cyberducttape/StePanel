# Root Helper Modernization Plan (MEDIUM/HIGH)

## Issue 1: Bash Helpers Are Now Complex Subsystems

**Problem**: Root helpers started as simple glue scripts but now implement a full privileged control-plane subsystem in Bash.

**Current scope** (stepanel-appctl alone):
- systemd service management
- Worker process lifecycle
- Python runtime (venv, version management)
- Node tooling (npm/yarn/pnpm)
- Composer dependency management
- cgroups resource limits
- cron-like timer management
- Environment variable file generation

**Why Bash is problematic for this**:
- ❌ No type system (all arguments are strings)
- ❌ No API contracts (functions take variable args)
- ❌ Unstructured error handling (exit codes only)
- ❌ No safe parsing model (regex/string splits)
- ❌ Not fuzzable (no clear input boundaries)
- ❌ Silent failures (set -e doesn't catch all errors)
- ❌ Hard to audit (control flow via subshells, pipes)

**Risk**: A privilege boundary implemented in Bash with limited type safety is a class of bugs waiting to happen.

---

## Solution: Unified Go Helper Binary

**Proposed architecture**:
```bash
# Before:
stepanel-appctl python-apply SITE VERSION ...
stepanel-appctl nodejs-tool SITE install npm
stepanel-dbctl restore SITE DATABASE ...

# After:
stepanel-helper app python-apply <json payload>
stepanel-helper resource apply <json payload>
stepanel-helper task schedule <json payload>
```

**Single Go binary**, multiple commands, all with:
- ✅ Typed request structs (JSON)
- ✅ Explicit validation (before any action)
- ✅ Structured error responses
- ✅ Clear API contracts (protobuf or JSON schema)
- ✅ Fuzzable input parsing
- ✅ Comprehensive error handling

---

## Implementation Plan

### Phase 1: Architecture Design
1. Define command taxonomy (site, resource, app, database, task, etc.)
2. Define request/response types for each command
3. Design error response format
4. Plan argument validation strategy

### Phase 2: Core Infrastructure
1. Create `cmd/stepanel-helper/main.go`
2. Implement JSON request decoder
3. Implement privilege drop logic
4. Implement structured logging
5. Implement error response formatter

### Phase 3: Migrate Existing Commands
1. Migrate stepanel-sitectl → stepanel-helper site
2. Migrate stepanel-appctl → stepanel-helper app
3. Migrate stepanel-dbctl → stepanel-helper database
4. Migrate stepanel-vhostctl → stepanel-helper vhost
5. Migrate stepanel-proxyctl → stepanel-helper proxy
6. Migrate stepanel-gitctl → stepanel-helper git
7. Migrate stepanel-runnerctl → stepanel-helper runner

### Phase 4: Update Daemon
Update Go code to call new helper:
```go
// Before:
runHelperCommand(ctx, cfg, "/usr/local/sbin/stepanel-appctl", "python-apply", site, version, ...)

// After:
type PythonApplyRequest struct {
    Site    string `json:"site"`
    Version string `json:"version"`
    // ...
}
payload, _ := json.Marshal(PythonApplyRequest{...})
runHelperCommand(ctx, cfg, "/usr/local/sbin/stepanel-helper", "app", "python-apply", string(payload))
```

---

## Benefits

### Security
- Typed arguments prevent injection
- Explicit validation before action
- Clear error handling
- Fuzzable input surface
- Easier to audit privilege boundary

### Reliability
- Compiled code catches errors at build time
- Return types enforce success/failure handling
- Structured errors (not just exit codes)
- Better logging and debugging

### Maintainability
- Single codebase, not 8 separate bash scripts
- API contracts (request/response structs)
- Easier to refactor and improve
- Easier to test (unit tests in Go)

---

## Timeline Estimate

- Phase 1: 1 week (design)
- Phase 2: 1 week (infrastructure)
- Phase 3: 2-3 weeks (migrate commands)
- Phase 4: 1 week (update daemon)
- Total: 5-6 weeks

---

## Related Issues

- Sudo privilege boundary (docs/SUDO_THREAT_MODEL.md)
- Helper timeout classification (docs/HELPER_TIMEOUT_CLASSES.md)
- Process group cleanup (already fixed)

---

## Issue 2: Scheduled Tasks Need Resource Controls

**Problem**: Tasks feature allows arbitrary customer shell execution with minimal safeguards against DoS/resource exhaustion.

**Current implementation** (tasks.go:167-169, stepanel-appctl:256-260):
```bash
exec /bin/bash -lc <customer-command>
```

Executes as site user with systemd hardening (good), but missing:
- Command audit display
- Last-run result/output
- Output size limits
- Execution duration limits
- Failure counting
- Kill button
- Auto-disable on runaway
- Per-account task limit
- Execution frequency limit

**Risk**: Customer can turn task panel into fork-bomb-as-a-service interface.

---

## Scheduled Task Safeguards

### Audit & Visibility
- ✅ Command logged before execution
- ❌ Last run result not stored
- ❌ Output not captured/displayed
- ❌ Duration not tracked

### Resource Limits
- ✅ systemd CPU/memory limits
- ✅ cgroup process limits
- ❌ Output size limit
- ❌ Execution time limit
- ❌ File descriptor limit

### Failure Handling
- ❌ Consecutive failure count
- ❌ Auto-disable on runaway
- ❌ Kill button for hanging tasks
- ❌ Alert on repeated failures

### Rate Limiting
- ❌ Per-account task limit
- ❌ Execution frequency limit
- ❌ Max concurrent tasks

---

## Recommended Safeguards (Priority Order)

### High Priority
1. **Output capture**: Store last N lines of stdout/stderr
2. **Output limit**: Cap output at 1MB per execution
3. **Time limit**: Cap execution at 5 minutes (configurable)
4. **Last result**: Store exit code and timestamp
5. **Kill button**: UI to forcibly stop hanging task

### Medium Priority
6. **Failure count**: Track consecutive failures, disable after 10
7. **Rate limit**: Max 1000 executions per account per day
8. **Concurrent limit**: Max 5 concurrent tasks per account
9. **Auto-disable**: Disable task if failed 5 times in a row

### Low Priority (Nice to have)
10. Duration tracking
11. Performance alerts
12. Audit log entries
13. Trend analysis

---

## Implementation Estimate

- UI for task management: 1 week
- Output capture system: 1 week
- Rate limiting: 3 days
- Auto-disable logic: 3 days
- Total: 2-3 weeks

---

## Bottom Line

These aren't small feature requests:

1. **Root helper modernization** is a 5-6 week project to migrate privileged subsystem from Bash to Go with typed APIs
2. **Scheduled task safeguards** are a 2-3 week project to prevent customers from abusing the feature for resource exhaustion

Both are worth doing, but they're substantial. Budget accordingly.
