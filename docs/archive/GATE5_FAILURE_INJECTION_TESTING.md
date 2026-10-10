# Gate 5: Failure Injection Testing Framework

> **Archived 2026-10-10.** The framework is implemented; real-host results now live in [REAL_HOST_FAILURE_MATRIX.md](../REAL_HOST_FAILURE_MATRIX.md) and [lab-results/](../lab-results/). Current Gate 5 status: [V1_PRODUCTION_GATES.md](../V1_PRODUCTION_GATES.md#gate-5-failure-injection-testing).

**Status:** 🚀 Framework Complete, Ready for Implementation  
**Date:** 2026-09-26  
**Purpose:** Prove StePanel survives and recovers deterministically from failures at every operation boundary

## The Problem

Unit tests prove operations work in the happy path. They do NOT prove recovery:

```
Site Creation Workflow:
  ├─ Initialize (mkdir, useradd)
  ├─ Validate & Lock
  ├─ Persist site metadata  ← CRASH HERE
  ├─ Configure PHP
  ├─ Provision database
  ├─ Create vhost
  ├─ Mark complete
  └─ Return success

After SIGKILL at persistence:
  Machine reboots
  StePanel starts
  Reconciliation runs
  
Question: Does it recover safely?
NO HALF-STATES?
DETERMINISTIC?
```

The failure injection framework proves recovery at **every boundary**, not just overall.

## Framework Architecture

### Core Components

**FailureInjector** - Injects failures at specific points
```go
injector := NewFailureInjector()
injector.SetFailure(FailurePointMidOp, FailureTypeSIGTERM)

// At operation boundaries:
if err := injector.InjectAt(ctx, "site_create", FailurePointInit); err != nil {
    return err  // Failure injected here
}
```

**WorkflowFailureTest** - Defines a complete workflow with failure boundaries
```go
WorkflowFailureTest{
    Name: "site_creation_with_failure_injection",
    ExecuteWorkflow: func(ctx, injector) error {
        // Step 1: Initialize
        injector.InjectAt(ctx, "site_create", FailurePointInit)
        // Step 2: Persist
        injector.InjectAt(ctx, "site_create", FailurePointFirstWrite)
        // ... more steps
    },
    VerifyRecovery: func() error {
        // Check: no half-states exist
        // Check: system is consistent
    },
}
```

**RecoveryValidator** - Verifies recovery consistency
```go
validator := NewRecoveryValidator()
validator.AddCheck("no_half_states", checkNoHalfStates, true)
validator.AddCheck("audit_trail_complete", checkAudit, true)
validator.ValidateRecovery(state)
```

## Failure Points

Every operation has these failure injection points:

```
FailurePointInit           - Before starting (validation, setup)
FailurePointPreOp          - Before operation (locks, checks)
FailurePointFirstWrite     - First durable state write
FailurePointMidOp          - Middle of operation (most complex part)
FailurePointFinalWrite     - Final durable state write
FailurePointCleanup        - After operation complete
```

## Failure Types

### Real Failures

**Process Termination:**
- `SIGKILL` - Immediate process death (not graceful)
- `SIGTERM` - Graceful termination request

**Resource Exhaustion:**
- `FilesystemFull` (ENOSPC) - Disk full, no space for writes
- `PermissionDenied` (EACCES) - Lost permissions mid-operation
- `TooManyFiles` (EMFILE) - File descriptor limit hit

**Database Issues:**
- `SQLiteBusy` - Database locked, operation blocked
- `SQLiteCorrupt` - Corrupted database file

**System Issues:**
- `HelperTimeout` - Helper subprocess timeout
- `HelperUnreachable` - Helper not responding
- `ContextCanceled` - Operation context cancelled
- `NetworkPartition` - Network unreachable

## Workflow Tests

### Site Creation Workflow

Tests that site creation recovers from failures at every boundary:

```go
SiteCreationWorkflow(siteName, stateStore)
  ├─ Init: mkdir, useradd
  ├─ Pre-op: validation, locks
  ├─ First write: persist site metadata
  ├─ Mid-op: PHP config, database, vhost
  ├─ Final write: mark complete
  └─ Verify: No half-states, either fully created or fully rolled back
```

**Recovery Properties:**
- Must not have site initialized but not persisted
- Must not have database provisioned but vhost not created
- Must not have PHP config but database not created
- Final state: either fully created OR fully rolled back (no in-between)

### Database Provisioning Workflow

Tests database creation resilience:

```go
DatabaseProvisioningWorkflow(siteName, dbName, stateStore)
  ├─ Init: CREATE DATABASE
  ├─ Mid-op: CREATE USER
  ├─ Final write: Persist credentials
  └─ Verify: Database and user exist together, or neither exist
```

**Recovery Properties:**
- Database and user must be created together
- Credentials must be persisted if user created
- No half-state: database without user

### Vhost Configuration Workflow

Tests virtual host setup:

```go
VhostConfigurationWorkflow(siteName, domain, stateStore)
  ├─ Init: Validate inputs
  ├─ Pre-op: Generate config
  ├─ First write: Write config file
  ├─ Mid-op: Apply to webserver
  ├─ Final write: Reload webserver
  └─ Verify: Config and webserver in sync
```

**Recovery Properties:**
- If webserver reloaded, config must be written
- If config written, must be applied to webserver
- No half-state: config without reload

## Running Tests

```bash
# Run all workflow failure tests
go test ./internal/testing -v -run Workflow

# Run specific workflow
go test ./internal/testing -v -run TestSiteCreationRecovery

# Run recovery determinism test (5 runs)
go test ./internal/testing -v -run TestRecoveryDeterminism
```

## Test Results

```
PASS: TestSiteCreationRecovery
  ✓ Survives failures at Init
  ✓ Survives failures at Pre-op
  ✓ Survives failures at FirstWrite
  ✓ Survives failures at MidOp (3 points)
  ✓ Survives failures at FinalWrite
  ✓ Survives failures at Cleanup
  ✓ No half-states detected

PASS: TestDatabaseProvisioningRecovery
  ✓ Database and user created together
  ✓ Credentials persisted together
  ✓ No orphaned databases

PASS: TestVhostConfigurationRecovery
  ✓ Config and webserver in sync
  ✓ No stale configurations

PASS: TestRecoveryDeterminism
  ✓ Recovery identical across 5 runs
  ✓ Event sequences match exactly
```

## What This Framework Proves

Gate 5 requires proving:
- [x] The modeled workflows do not produce half-states under the injected
      failure points
- [x] The modeled recovery sequence is deterministic

This framework:
1. **Injects failures at every operation boundary** (Init, Pre-op, FirstWrite, MidOp, FinalWrite, Cleanup)
2. **Exercises modeled failure types** (including a real child-process SIGKILL
   test); filesystem-full, SQLite-busy, and similar failures are injected
   errors, not host-level resource exhaustion
3. **Verifies recovery consistency** (no half-states, state is valid)
4. **Confirms determinism** (5 runs produce identical recovery)

## Integration with Production Gate

To fully meet Gate 5, add:

### 1. OS-Level Process Kill (partial evidence only)
```bash
# Repository-level real child-process boundary
go test ./internal/testing -run TestFailureInjectorSIGKILLUsesRealSignal -count=1

# Installed-host worker/panel drills
bash deploy/lab/install-smoke.sh
```
These checks do not prove host power-loss recovery or every operation boundary.

### 2. Filesystem Exhaustion (open)
```bash
# Use the disposable-VM harness only after a real provider backend is configured.
./scripts/vm_test_harness.sh test-enospc
```
The current harness fails closed because no provider backend is shipped.

### 3. Database Unavailability (partial evidence only)
```bash
# Use the disposable-VM harness after configuring its provider backend.
./scripts/vm_test_harness.sh test-db-offline
```
Installed-host smoke evidence covers a selected MariaDB outage/restart path;
the complete operation matrix remains open.

## State Verification Pattern

All workflows use the same pattern for proving "no half-states":

```go
VerifyRecovery: func() error {
    state := stateStore.GetState(resource)
    
    // Rule 1: If X happened, Y must have happened
    if state.Has("database_created") && !state.Has("database_persisted") {
        return fmt.Errorf("half-state: database created but not persisted")
    }
    
    // Rule 2: Must be in one of these states
    isFullyCreated := state.Has("final_write")
    isRolledBack := !state.Has("first_write")
    
    if !isFullyCreated && !isRolledBack {
        return fmt.Errorf("unknown state")
    }
    
    return nil
}
```

## Audit Trail Verification

Successful recovery requires proper audit events:

```
Expected audit sequence:
  1. "site.create.initiated"
  2. "site.create.persisted"
  3. "site.php_configured"
  4. "site.database_provisioned"
  5. "site.vhost_created"
  6. "site.complete"

If failure at step 3:
  - Must have steps 1-2
  - Must NOT have steps 4-6
  - OR recovery must complete all steps
  - No partial audit trails allowed
```

## Determinism Proof

Recovery is deterministic if:
1. Same input (same failure point + type) produces same recovery sequence
2. Event timestamps differ, but order identical
3. 5+ consecutive runs produce identical sequences

Test case:
```go
for i := 0; i < 5; i++ {
    result := runWorkflowWithFailure(FailurePointMidOp, FailureTypeSIGTERM)
    if result.Events != expected {
        return "recovery not deterministic"
    }
}
```

## Next Steps

### Immediate
- [x] Run repository failure-injection tests for the modeled workflows
- [x] Verify modeled "no half-states" invariants
- [ ] Record recovery timing and determinism on the disposable host matrix

### Short-term
- [ ] Expand OS-level process-kill tests across all critical operations
- [ ] Test filesystem full conditions (ENOSPC)
- [ ] Test database unavailability (connection timeout)

### Medium-term
- [ ] Run failure injection on disposable VMs
- [ ] Test complete site lifecycle with random failures
- [ ] Measure recovery time (target: < 5 seconds)
- [ ] Verify audit trail is complete

## Success Criteria for Gate 5

- ✅ Framework: Complete and tested for modeled failures
- ✅ Modeled no-half-state and deterministic workflow checks pass
- ⏳ OS-level failures: selected SIGKILL recovery evidence only
- ⏳ Resource exhaustion: real ENOSPC recovery remains open
- ⏳ Database failures: selected outage evidence only
- ⏳ All 5 operations: full host-level matrix remains open

## Related Documents

- [V1_PRODUCTION_GATES.md](../V1_PRODUCTION_GATES.md) - Gate 5 requirements
- [internal/testing/failure_injection.go](../../internal/testing/failure_injection.go) - Framework implementation
- [internal/testing/workflow_tests.go](../../internal/testing/workflow_tests.go) - Workflow test definitions

---

**This framework supports production-readiness verification; it is not approval evidence until the pending real-failure scenarios and release gates are completed.**
