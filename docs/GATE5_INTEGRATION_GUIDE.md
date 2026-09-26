# Gate 5: Durable Checkpoint Integration Guide

**Purpose:** Step-by-step guide for integrating site creation journal into broker operations

**Status:** Ready for implementation

## Overview

The goal is to make site creation **durable** by:
1. Loading/creating a journal file before starting
2. Checking journal for completed steps
3. Executing incomplete steps
4. Atomically marking steps complete
5. Cleaning up on success

## Key Concept: Idempotent Retries

Once a step is marked complete in the journal, it's **never re-executed**:

```
First Run:
  Step 1 executes → Step 1 marked complete ✓
  Step 2 executes → Step 2 marked complete ✓
  Step 3 executes → CRASH ✗

Recovery (Same jobID):
  Load journal → sees Step 1 complete, Step 2 complete
  Step 1: Skip (already done)
  Step 2: Skip (already done)
  Step 3: Execute again (will succeed this time)
  Step 4: Execute
  ... resume and complete
```

This guarantees **no re-execution of completed steps** → no half-states.

## Step-by-Step Integration

### 1. Generate Unique Job ID

Every site creation gets a unique, reproducible job ID:

```go
jobID := generateJobID()  // Returns UUID or timestamp-based ID
// Used as: /recovery/site-creation-{jobID}.json
```

The same jobID should be used for all retries of the same operation. This is typically provided by the durable job system.

### 2. Load or Create Journal

At the start of site creation:

```go
func (b *Broker) handleSiteCreate(req *Request) (*Response, error) {
    jobID := req.JobID  // From durable job system
    site := req.Site
    actor := req.Actor
    
    // Load existing journal or create new one
    journal, err := loadOrCreateCreationJournal(b.recoveryRoot, jobID, site, actor)
    if err != nil {
        return nil, fmt.Errorf("load creation journal: %w", err)
    }
    
    // ... rest of creation ...
}
```

**What happens:**
- First run: Creates new journal at `/recovery/site-creation-{jobID}.json`
- Retry: Loads existing journal, sees completed steps

### 3. Wrap Each Step

For every step, check journal first:

```go
// Step 1: Initialize (mkdir, useradd)
if !journal.isComplete(stepInitialized) {
    if err := initializeSite(site); err != nil {
        return nil, fmt.Errorf("site initialization failed: %w", err)
    }
    if err := journal.markComplete(stepInitialized); err != nil {
        return nil, fmt.Errorf("journal: %w", err)
    }
}

// Step 2: Persist metadata
if !journal.isComplete(stepPersisted) {
    if err := persistSiteMetadata(site); err != nil {
        return nil, fmt.Errorf("site metadata persistence failed: %w", err)
    }
    if err := journal.markComplete(stepPersisted); err != nil {
        return nil, fmt.Errorf("journal: %w", err)
    }
}

// Step 3: Configure PHP
if !journal.isComplete(stepPHPConfigured) {
    if err := configurePHP(site); err != nil {
        return nil, fmt.Errorf("PHP configuration failed: %w", err)
    }
    if err := journal.markComplete(stepPHPConfigured); err != nil {
        return nil, fmt.Errorf("journal: %w", err)
    }
}

// ... and so on for remaining steps
```

**Key Pattern:**
```
if !journal.isComplete(stepName) {
    if err := executeStep(); err != nil {
        return err  // Let caller retry
    }
    if err := journal.markComplete(stepName); err != nil {
        return err  // Journal persistence is critical
    }
}
```

### 4. Handle Completion

On success, clean up the journal:

```go
// All steps complete
if err := journal.cleanup(); err != nil {
    return nil, fmt.Errorf("cleanup journal: %w", err)
}

return &Response{
    OK: true,
    Details: json.Marshal(map[string]string{
        "site": site,
        "created_at": time.Now().UTC().String(),
    }),
}, nil
```

### 5. Handle Errors

On error, **leave the journal on disk**. The next retry will:
1. Load the same journal
2. Skip completed steps
3. Resume from where it failed
4. Try again

```go
if err := executeStep(); err != nil {
    // Don't cleanup journal!
    // Caller will retry, load journal, and resume
    return nil, fmt.Errorf("step failed: %w", err)
}
```

## Complete Example Implementation

```go
func (b *Broker) handleSiteCreate(req *Request) (*Response, error) {
    // Validate input
    if err := validateSiteCreate(req); err != nil {
        return nil, err
    }

    jobID := req.JobID
    site := req.Site
    actor := req.Actor
    
    // Load or create durable journal
    journal, err := loadOrCreateCreationJournal(b.recoveryRoot, jobID, site, actor)
    if err != nil {
        return nil, fmt.Errorf("load creation journal: %w", err)
    }

    // Step 1: Initialize site (mkdir, useradd, etc.)
    if !journal.isComplete(stepInitialized) {
        if err := b.initializeSite(site); err != nil {
            return nil, fmt.Errorf("failed to initialize site: %w", err)
        }
        if err := journal.markComplete(stepInitialized); err != nil {
            return nil, fmt.Errorf("journal step 1: %w", err)
        }
    }

    // Step 2: Persist site metadata (site config file, etc.)
    if !journal.isComplete(stepPersisted) {
        if err := b.persistSiteMetadata(site); err != nil {
            return nil, fmt.Errorf("failed to persist metadata: %w", err)
        }
        if err := journal.markComplete(stepPersisted); err != nil {
            return nil, fmt.Errorf("journal step 2: %w", err)
        }
    }

    // Step 3: Configure PHP
    if !journal.isComplete(stepPHPConfigured) {
        if err := b.configurePHP(site); err != nil {
            return nil, fmt.Errorf("failed to configure PHP: %w", err)
        }
        if err := journal.markComplete(stepPHPConfigured); err != nil {
            return nil, fmt.Errorf("journal step 3: %w", err)
        }
    }

    // Step 4: Create database
    if !journal.isComplete(stepDatabaseCreated) {
        if err := b.createDatabase(site); err != nil {
            return nil, fmt.Errorf("failed to create database: %w", err)
        }
        if err := journal.markComplete(stepDatabaseCreated); err != nil {
            return nil, fmt.Errorf("journal step 4: %w", err)
        }
    }

    // Step 5: Create vhost
    if !journal.isComplete(stepVhostCreated) {
        if err := b.createVhost(site); err != nil {
            return nil, fmt.Errorf("failed to create vhost: %w", err)
        }
        if err := journal.markComplete(stepVhostCreated); err != nil {
            return nil, fmt.Errorf("journal step 5: %w", err)
        }
    }

    // Step 6: Mark completion
    if !journal.isComplete(stepCompleted) {
        if err := journal.markComplete(stepCompleted); err != nil {
            return nil, fmt.Errorf("journal step 6: %w", err)
        }
    }

    // Success: cleanup journal
    if err := journal.cleanup(); err != nil {
        return nil, fmt.Errorf("cleanup journal: %w", err)
    }

    // Audit the successful creation
    if err := b.audit(actor, "site.created", site); err != nil {
        // Log but don't fail — site is already created
        b.logger.Printf("audit logging failed: %v", err)
    }

    return &Response{
        OK: true,
        Details: mustMarshal(map[string]string{
            "site": site,
            "status": "created",
        }),
    }, nil
}
```

## Testing the Integration

### Unit Test: Journal Persistence

```go
func TestSiteCreationJournalPersistence(t *testing.T) {
    // Create site, simulate crash after step 2
    journal, _ := loadOrCreateCreationJournal(recoveryRoot, jobID, site, actor)
    
    // Mark steps as complete
    journal.markComplete(stepInitialized)
    journal.markComplete(stepPersisted)
    
    // Simulate process death: create new journal instance
    journal2, _ := loadOrCreateCreationJournal(recoveryRoot, jobID, site, actor)
    
    // Verify journal loaded correctly
    assert.True(journal2.isComplete(stepInitialized))
    assert.True(journal2.isComplete(stepPersisted))
    assert.False(journal2.isComplete(stepPHPConfigured))
}
```

### Integration Test: Failure Recovery

```go
func TestSiteCreationRecoveryFromCrash(t *testing.T) {
    injector := NewFailureInjector()
    
    // Inject failure at step 3 (PHP config)
    injector.SetFailure(FailurePointMidOp, FailureTypeSIGTERM)
    
    // First attempt: fails at step 3
    err1 := broker.handleSiteCreate(req, injector)
    assert.NotNil(err1)  // Failed
    
    // Second attempt: same jobID, should resume from step 3
    injector.Disable()
    err2 := broker.handleSiteCreate(req, injector)
    assert.Nil(err2)  // Should succeed
    
    // Verify site is fully created (no half-states)
    assert.True(siteExists(site))
    assert.True(siteHasPHP(site))
    assert.True(siteHasDatabase(site))
    assert.True(siteHasVhost(site))
}
```

## Error Handling Strategy

### Recoverable Errors

**Error:** `mkdir failed: permission denied`

**Action:** Leave journal on disk, let caller retry

```
First attempt: mkdir fails
Second attempt: mkdir called again (will fail again)
               But now operator has fixed permissions
               mkdir succeeds this time
               Resume from next step
```

### Unrecoverable Errors

**Error:** `site name invalid`

**Action:** Fail immediately, don't journal

```go
// Before journaling, validate everything
if err := validateSiteCreate(req); err != nil {
    return nil, err  // Don't touch journal
}
// Only after validation succeeds, load journal
journal, err := loadOrCreateCreationJournal(...)
```

### Catastrophic Errors

**Error:** Journal file corrupted

**Action:** Fail with clear message, operator can manually recover

```go
journal, err := loadOrCreateCreationJournal(...)
if err != nil {
    // Could not load journal — corrupted?
    return nil, fmt.Errorf("unable to load recovery state: %w\n"+
        "Manual recovery needed. Contact support.", err)
}
```

## Monitoring & Observability

### Metrics to Track

1. **Creation success rate** - % of sites created without retry
2. **Retry rate** - % of creations that needed retry
3. **Recovery time** - Time from failure to successful creation
4. **Journal cleanup** - % of journals cleaned up (should be ~100%)

### Logging Pattern

```go
b.logger.Printf("site.create.start site=%s jobID=%s", site, jobID)
b.logger.Printf("site.create.step step=initialized site=%s", site)
b.logger.Printf("site.create.step step=persisted site=%s", site)
// ...
b.logger.Printf("site.create.complete site=%s duration=%s", site, elapsed)
```

### Audit Trail

Every step should be auditable:

```
Audit Entry: site.creation.initiated
  - Site: mysite
  - Actor: admin@example.com
  - JobID: abc123...
  - Timestamp: 2026-09-26T10:15:30Z

Audit Entry: site.creation.step_complete
  - Site: mysite
  - Step: initialized
  - Timestamp: 2026-09-26T10:15:31Z

Audit Entry: site.creation.step_complete
  - Site: mysite
  - Step: persisted
  - Timestamp: 2026-09-26T10:15:32Z

// ... (if failure here, recovery detected) ...

Audit Entry: site.creation.recovered
  - Site: mysite
  - ResumedFrom: persisted
  - Timestamp: 2026-09-26T10:15:45Z

Audit Entry: site.creation.complete
  - Site: mysite
  - Duration: 15 seconds
  - Timestamp: 2026-09-26T10:15:45Z
```

## Verification Checklist

Before calling a step implementation durable:

- [ ] Journal is loaded/created at operation start
- [ ] Each step checks `journal.isComplete(stepName)` before executing
- [ ] Success marking is atomic (temp file + rename)
- [ ] Journal is persisted before continuing
- [ ] Errors don't cleanup journal (allows retry)
- [ ] Success cleans up journal (removes recovery file)
- [ ] Audit entries logged for each step
- [ ] Recovery works: same jobID resumes correctly
- [ ] Idempotency verified: step is safe to re-run

## Related Files

- `site_creation_journal.go` - Journal implementation
- `internal/rootbroker/operations.go` - Where integration happens
- `site_lifecycle.go` - Reference implementation (termination)
- `internal/testing/workflow_tests.go` - Failure injection tests

---

**Following this guide makes site creation production-ready: durable, recoverable, deterministic.**
