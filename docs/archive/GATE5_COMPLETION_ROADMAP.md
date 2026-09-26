# Gate 5: Completion Roadmap

**Purpose:** Detailed plan for completing all Gate 5 requirements  
**Timeline:** 2-3 weeks  
**Status:** Phase 4 starting, Phases 5-7 planned

---

## Overview

Gate 5 requires proving deterministic recovery from failures. We're at 40% complete:

✅ **Done (40%):**
- Failure injection framework
- Durable checkpoint system
- Site creation journal
- Documentation & test harness

🔄 **In Progress (10%):**
- Site creation broker integration

⏳ **Remaining (50%):**
- Complete all 5 critical operations
- OS-level failure testing
- Determinism proof (100+ runs)

---

## Phase 4: Site Creation Broker Integration

**Timeline:** 1-2 hours  
**Owner:** Whoever starts this  
**Effort:** Low (template already prepared)

### Changes Required

**File:** `internal/rootbroker/operations.go`

**Current code (before):**
```go
func (b *Broker) handleSiteCreate(req *Request) (*Response, error) {
    if err := validateSiteCreate(req); err != nil {
        return nil, err
    }
    
    site := req.Site
    // ... execute site creation steps ...
    // No journal, no checkpoints
    
    return &Response{OK: true}, nil
}
```

**Updated code (after):**
```go
func (b *Broker) handleSiteCreate(req *Request) (*Response, error) {
    if err := validateSiteCreate(req); err != nil {
        return nil, err
    }
    
    site := req.Site
    jobID := req.JobID  // From durable job system
    actor := req.Actor
    
    // Load or create durable journal
    journal, err := loadOrCreateCreationJournal(b.recoveryRoot, jobID, site, actor)
    if err != nil {
        return nil, fmt.Errorf("load creation journal: %w", err)
    }
    
    // Step 1: Initialize
    if !journal.isComplete(stepInitialized) {
        if err := b.initializeSite(site); err != nil {
            return nil, fmt.Errorf("initialize: %w", err)
        }
        if err := journal.markComplete(stepInitialized); err != nil {
            return nil, fmt.Errorf("journal: %w", err)
        }
    }
    
    // Step 2: Persist metadata
    if !journal.isComplete(stepPersisted) {
        if err := b.persistSiteMetadata(site); err != nil {
            return nil, fmt.Errorf("persist: %w", err)
        }
        if err := journal.markComplete(stepPersisted); err != nil {
            return nil, fmt.Errorf("journal: %w", err)
        }
    }
    
    // ... remaining steps follow same pattern ...
    
    // Success: cleanup journal
    if err := journal.cleanup(); err != nil {
        return nil, fmt.Errorf("cleanup: %w", err)
    }
    
    return &Response{OK: true}, nil
}
```

### Test Integration

**Add to test suite:**
```bash
# Run with failure injection
go test ./internal/testing -v -run "TestDurable"

# Expected: All tests pass, zero half-states
```

### Verification

Before moving to next operation:
- [ ] All failure injection tests pass
- [ ] No half-states detected
- [ ] Recovery determinism verified
- [ ] Journal properly cleaned up on success

---

## Phase 5A: App Deployment Journal

**Timeline:** 2-3 hours  
**Pattern:** Same as site creation

### What Is App Deployment?

Site app update with rollback support:
1. Validate app archive
2. Extract to staging directory
3. Run pre-activation checks
4. Mark current as rollback target
5. Move new app to live location
6. Verify app responsive
7. Update app metadata

### Steps for Journal

```go
const (
    stepAppValidated      = "APP_VALIDATED"
    stepAppExtracted      = "APP_EXTRACTED"
    stepRollbackTargeted  = "ROLLBACK_TARGETED"
    stepAppActivated      = "APP_ACTIVATED"
    stepAppVerified       = "APP_VERIFIED"
    stepMetadataUpdated   = "METADATA_UPDATED"
)
```

### File to Create

**New file:** `app_deployment_journal.go`

```go
package main

import (
    "encoding/json"
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "strings"
    "time"
)

// App-deployment step journal.
//
// The app-deployment job updates an app with rollback support.
// Each step is idempotent: extracting to same location, activating
// same version, updating metadata with same data are all safe to retry.

const (
    deploymentJournalVersion = 1

    stepAppValidated      = "APP_VALIDATED"
    stepAppExtracted      = "APP_EXTRACTED"
    stepRollbackTargeted  = "ROLLBACK_TARGETED"
    stepAppActivated      = "APP_ACTIVATED"
    stepAppVerified       = "APP_VERIFIED"
    stepMetadataUpdated   = "METADATA_UPDATED"
)

var deploymentStepOrder = []string{
    stepAppValidated,
    stepAppExtracted,
    stepRollbackTargeted,
    stepAppActivated,
    stepAppVerified,
    stepMetadataUpdated,
}

type deploymentJournal struct {
    Version      int             `json:"version"`
    JobID        string          `json:"job_id"`
    Site         string          `json:"site"`
    ReleaseID    string          `json:"release_id"`
    Actor        string          `json:"actor"`
    StartedAt    time.Time       `json:"started_at"`
    UpdatedAt    time.Time       `json:"updated_at"`
    Completed    map[string]bool `json:"completed"`
    RollbackPath string          `json:"rollback_path,omitempty"`

    path string
}

// Same pattern as site creation journal:
// loadOrCreateDeploymentJournal, markComplete, cleanup
```

### Idempotency Checks

| Step | Idempotent? | Implementation |
|------|-------------|-----------------|
| Validate | ✅ | Re-run validation, same result |
| Extract | ✅ | Extract to same temp dir, overwrite ok |
| Target rollback | ✅ | Save old path (overwrite safe) |
| Activate | ✅ | Move same source to same target |
| Verify | ✅ | Re-run checks, same result |
| Update metadata | ✅ | Overwrite with same metadata |

---

## Phase 5B: Database Provisioning Journal

**Timeline:** 2-3 hours  
**Pattern:** Same as site creation

### Steps for Journal

```go
const (
    stepDatabaseCreated  = "DATABASE_CREATED"
    stepUserCreated      = "USER_CREATED"
    stepPrivilegesGranted = "PRIVILEGES_GRANTED"
    stepCredentialsSaved = "CREDENTIALS_SAVED"
    stepConnectivityTest = "CONNECTIVITY_TEST"
)
```

### Idempotency Checks

| Step | Idempotent? | Implementation |
|------|-------------|-----------------|
| CREATE DATABASE | ⚠️ | Handle "already exists" error |
| CREATE USER | ⚠️ | Handle "already exists" error |
| GRANT PRIVILEGES | ✅ | Idempotent in SQL |
| Save credentials | ✅ | Overwrite with same data |
| Test connectivity | ✅ | Re-run test, same result |

**Key:** For "CREATE DATABASE" and "CREATE USER", if they fail with "already exists", that's ok — the journal tracks whether creation succeeded.

---

## Phase 5C: Vhost Configuration Journal

**Timeline:** 2-3 hours  
**Pattern:** Same as site creation

### Steps for Journal

```go
const (
    stepConfigGenerated  = "CONFIG_GENERATED"
    stepConfigWritten    = "CONFIG_WRITTEN"
    stepConfigApplied    = "CONFIG_APPLIED"
    stepWebserverReloaded = "WEBSERVER_RELOADED"
    stepSSLVerified      = "SSL_VERIFIED"
)
```

### Idempotency Checks

| Step | Idempotent? | Implementation |
|------|-------------|-----------------|
| Generate config | ✅ | Same inputs → same config |
| Write config file | ✅ | Overwrite with same content |
| Apply to webserver | ✅ | Re-apply, idempotent |
| Reload webserver | ✅ | Reload is safe to repeat |
| Verify SSL | ✅ | Re-check, same result |

---

## Phase 5D: Database Restoration Journal

**Timeline:** 2-3 hours  
**Pattern:** Similar but with validation gates

### Why DB Restore Needs Durability

```
Scenario: Database restore interrupted
  1. Validate dump file ✓
  2. Create database ✓
  3. Begin SQL import ✓
  4. Import 50% complete
  5. SIGKILL ✗
  6. System reboots
  
  Question: What happened to the database?
  - Is it partially imported (inconsistent)?
  - Is it empty (rollback happened)?
  
  Answer: Journal tracks what succeeded, next retry either:
    a) Resumes import (if idempotent)
    b) Rolls back and retries
```

### Steps for Journal

```go
const (
    stepDumpValidated   = "DUMP_VALIDATED"
    stepDatabaseDropped = "DATABASE_DROPPED"
    stepDatabaseCreated = "DATABASE_CREATED"
    stepDumpImported    = "DUMP_IMPORTED"
)
```

---

## Phase 6: Complete Workflow Testing

**Timeline:** 4-6 hours  
**Goal:** Prove all 5 operations work together

### Test Sequence

```
1. Create site [uses site creation journal]
2. Deploy initial app [uses deployment journal]
3. Create database [uses provisioning journal]
4. Configure vhost [uses vhost journal]
5. Restore from backup [uses restoration journal]
6. Terminate site [uses termination journal - already exists]
```

### Failure Testing

For each operation, inject failures at all points:

```bash
go test ./internal/testing -v -run "TestComplete"
```

Expected: Zero half-states, deterministic recovery

### Coverage Matrix

| Operation | FailurePoints | FailureTypes | Combinations |
|-----------|---------------|--------------|--------------|
| Site create | 6 | 4 | 24 |
| App deploy | 4 | 3 | 12 |
| DB provision | 3 | 3 | 9 |
| Vhost config | 5 | 3 | 15 |
| DB restore | 3 | 3 | 9 |
| Site termination | 8 | 2 | 16 |
| ─────────────── | ─ | ─ | ─── |
| **Total** | | | **85** |

Success criteria: **All 85 scenarios pass**

---

## Phase 7: VM-Level Failure Testing

**Timeline:** 8-12 hours  
**Goal:** Prove recovery works with real OS-level failures

### Setup

**Create disposable VMs:**
1. VM A: StePanel + test site
2. VM B: StePanel + backup storage
3. VM C: MySQL database

### Test Scenarios

#### Test 1: SIGKILL During Site Creation

```bash
#!/bin/bash
set -e

# Start site creation in background
ssh vm-a "stepanel-ctl create-site test-site" &
PID=$!

# Wait for mkdir to complete, then kill
sleep 0.5
kill -9 $PID

# Reboot VM
ssh vm-a "sudo reboot"
sleep 10

# Verify recovery
ssh vm-a "stepanel-ctl site-status test-site"
# Expected: Site either fully created OR fully rolled back (not half-state)
```

#### Test 2: ENOSPC During Metadata Persistence

```bash
#!/bin/bash
set -e

# Fill disk to 99%
ssh vm-a "dd if=/dev/zero of=/tmp/fill bs=1M count=<SIZE> &"

# Start site creation
ssh vm-a "stepanel-ctl create-site test-site"
# Expected: Fails with "no space on device"

# Free up space
ssh vm-a "rm /tmp/fill"

# Retry same creation
ssh vm-a "stepanel-ctl create-site test-site"
# Expected: Resumes from journal, completes successfully
```

#### Test 3: Database Offline

```bash
#!/bin/bash
set -e

# Stop database
ssh vm-c "sudo systemctl stop mysql"

# Try to provision database
ssh vm-a "stepanel-ctl create-db test-site test-db"
# Expected: Fails with "connection refused"

# Restart database
ssh vm-c "sudo systemctl start mysql"

# Retry
ssh vm-a "stepanel-ctl create-db test-site test-db"
# Expected: Resumes, completes successfully
```

### Determinism Proof

**Run 100 times and verify identical recovery:**

```bash
#!/bin/bash
for i in {1..100}; do
    # Create site
    timeout 5 ssh vm-a "stepanel-ctl create-site test-$i" || true
    
    # Kill mid-creation
    if [ $((RANDOM % 2)) -eq 0 ]; then
        pkill -9 stepanel-root
    fi
    
    # Reboot
    ssh vm-a "sudo reboot"
    sleep 10
    
    # Recover
    ssh vm-a "stepanel-ctl create-site test-$i"
    
    # Verify
    ssh vm-a "stepanel-ctl site-status test-$i" | grep -q "created" || exit 1
    
    echo "Run $i: PASS"
done
echo "✅ 100 deterministic recoveries proven"
```

### Observability Requirements

For each test run, collect:
1. Journal files (on disk)
2. Audit logs (what completed)
3. System logs (when failures occurred)
4. Recovery time (how long to detect + fix)

---

## Success Metrics

### Phase 4 (Site Creation Integration)

- [ ] All failure injection tests pass (6+ scenarios)
- [ ] Zero half-states detected
- [ ] Recovery determinism verified
- [ ] Broker integration complete

### Phase 5 (All Operations Durable)

- [ ] All 5 operations have journals
- [ ] All 85 test scenarios pass
- [ ] Audit trail complete for all operations
- [ ] Recovery time SLA < 5 seconds

### Phase 6 (Complete Workflow)

- [ ] All operations tested together
- [ ] Multi-operation failures handled
- [ ] Cross-operation consistency verified

### Phase 7 (VM-Level Testing)

- [ ] Real SIGKILL recovery proven
- [ ] Real ENOSPC recovery proven
- [ ] Real database offline recovery proven
- [ ] 100+ deterministic recovery runs
- [ ] < 1% failure rate on recovery

### Final Gate 5 Approval

- [x] Framework complete
- [x] No half-states proven
- [ ] Deterministic recovery (100+ runs)
- [ ] All 5 operations durable
- [ ] OS-level failures tested
- [ ] Resource exhaustion tested
- [ ] Database failures tested
- [ ] < 5 second recovery SLA
- [ ] Complete audit trail

---

## Timeline Estimate

| Phase | Task | Effort | Status |
|-------|------|--------|--------|
| 4 | Site creation broker integration | 1-2h | 🔄 Starting |
| 5A | App deployment journal | 2-3h | ⏳ Blocked on 4 |
| 5B | Database provisioning journal | 2-3h | ⏳ Blocked on 4 |
| 5C | Vhost configuration journal | 2-3h | ⏳ Blocked on 4 |
| 5D | Database restoration journal | 2-3h | ⏳ Blocked on 4 |
| 6 | Complete workflow testing | 4-6h | ⏳ Blocked on 5 |
| 7 | VM-level testing | 8-12h | ⏳ Blocked on 6 |
| | **Total** | **21-33h** | |

**Realistic timeline:** 2-3 weeks working full-time, or 4-6 weeks part-time

---

## Key Principles to Remember

1. **Idempotency first:** Every step must be safe to re-run
2. **Atomic writes:** Journal updates must be all-or-nothing
3. **Determinism:** Same input always produces same recovery sequence
4. **No re-execution:** Once marked complete in journal, skip on retry
5. **Leave journal on error:** Enables recovery on next attempt
6. **Clean up on success:** Removes recovery file when done
7. **Audit everything:** Every step change goes to audit log

---

## Questions Answered

**Q: Why do we need journals for all operations?**  
A: So recovery can resume from where it failed, not start over and risk different outcome.

**Q: What if a step fails because of a bug, not a crash?**  
A: Journal doesn't help — bug needs fixing. But if it's environmental (disk full, timeout), journal lets recovery retry.

**Q: How do we verify determinism?**  
A: Run same operation 100 times with same failure injected. All runs must have identical event sequence.

**Q: What's the recovery time target?**  
A: < 5 seconds from failure detection to ready. Load journal (< 10ms) + resume from checkpoint (< 1s) + verify (< 100ms).

**Q: Can we test this locally?**  
A: Yes! Phases 4-6 use failure injection framework (runs in tests). Phase 7 needs VMs for real SIGKILL/ENOSPC.

---

## Related Documents

- `GATE5_HARDENING_STRATEGY.md` - Why we do this
- `GATE5_INTEGRATION_GUIDE.md` - How to implement
- `GATE5_FAILURE_INJECTION_TESTING.md` - Framework details
- `GATE5_PROGRESS.md` - What's done

---

**Gate 5 is complete when:** All operations are durable, all tests pass (85+ scenarios), and real-world failure recovery is proven deterministic (100+ runs).

**After Gate 5:** StePanel is production-ready. Deployments can fail, systems can crash, but sites recover cleanly.
