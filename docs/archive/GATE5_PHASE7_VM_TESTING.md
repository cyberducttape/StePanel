# Gate 5: Phase 7 - VM-Level Failure Testing

**Status:** 🚀 Ready for Implementation  
**Date:** 2026-09-26  
**Purpose:** Prove recovery works with real OS-level failures

---

## Overview

Phases 1-6 proved recovery works in controlled test environments using failure injection. Phase 7 proves recovery works with **real OS-level failures**:

- Real SIGKILL (not simulated)
- Real ENOSPC (actual disk full)
- Real database offline
- 100+ deterministic recovery runs
- SLA verification (< 5 seconds)

---

## Test Infrastructure

### VM Setup

**Three disposable VMs:**

1. **VM A - StePanel Application**
   - Runs StePanel broker and handlers
   - Test site operations
   - Receives SIGKILL, ENOSPC injections

2. **VM B - Backup Storage**
   - Stores backup files
   - Simulates off-site backup location
   - Tests backup availability

3. **VM C - Database Server**
   - MySQL/MariaDB database
   - Can be stopped/started for offline tests
   - Tests database unavailability recovery

### Networking

```
VM A (StePanel) ←→ VM C (Database)
     ↓
VM B (Backups)
```

VM A has read-write access to B and C. Network can be partitioned for network failure tests.

---

## Test Scenarios

### Scenario 1: Real SIGKILL During Site Creation

**Goal:** Prove site creation survives real process death

**Steps:**
1. Start site creation on VM A
2. Let it progress to random point (0.5-2 seconds)
3. Kill StePanel process with `kill -9 <pid>`
4. Reboot VM A
5. Verify site is either fully created OR fully rolled back

**Expected Result:**
- Zero half-states
- Deterministic recovery sequence
- < 5 second recovery time

**Iterations:** 10+ runs

**Verification:**
```bash
site_status=$(curl http://vm-a:8080/api/sites/status)
# Must be one of: created, rolled_back
# NOT: partially_created, inconsistent_state
```

### Scenario 2: Real ENOSPC During Metadata Persistence

**Goal:** Prove operations fail gracefully when disk full

**Steps:**
1. Fill disk on VM A to 99%
2. Start site creation
3. Operation should fail with "no space on device"
4. Free disk space (delete fill file)
5. Retry same operation
6. Should succeed (resume from journal)

**Expected Result:**
- Clean failure with ENOSPC error
- Retry succeeds without leftover state
- Journal tracks recovery

**Iterations:** 10+ runs

**Verification:**
```bash
# First attempt: should fail with ENOSPC
error=$(curl http://vm-a:8080/api/sites/create 2>&1)
grep -q "no space on device" <<< "$error"

# Free space
ssh vm-a "rm /var/www/.fill"

# Retry: should succeed
curl http://vm-a:8080/api/sites/create
```

### Scenario 3: Real Database Offline

**Goal:** Prove operations timeout gracefully when database unavailable

**Steps:**
1. Start database provisioning on VM A
2. Stop database on VM C: `systemctl stop mysql`
3. Operation should timeout (connection refused)
4. Start database: `systemctl start mysql`
5. Retry same operation
6. Should succeed

**Expected Result:**
- Clean timeout after 30 seconds
- Retry succeeds
- Database state consistent

**Iterations:** 10+ runs

**Verification:**
```bash
# Stop database
ssh vm-c "sudo systemctl stop mysql"

# Attempt to provision: should timeout
timeout 35 curl http://vm-a:8080/api/databases/provision || echo "Timed out as expected"

# Start database
ssh vm-c "sudo systemctl start mysql"

# Retry: should succeed
curl http://vm-a:8080/api/databases/provision
```

### Scenario 4: Determinism (100+ Runs)

**Goal:** Prove recovery sequence is deterministic

**Steps:**
1. Run same operation 100+ times
2. Inject same failure at same point each time
3. Capture recovery event sequence
4. Verify all runs have identical sequence

**Expected Result:**
- 100/100 runs have identical event order
- Timestamps differ (but sequence same)
- No random state corruption

**Iterations:** 100+ runs

**Verification:**
```bash
for i in {1..100}; do
  events=$(curl http://vm-a:8080/api/sites/create 2>&1 | \
           grep -o 'event: [a-zA-Z_]*' | cut -d' ' -f2 | tr '\n' ',')
  echo "$events" >> /tmp/determinism.log
done

# All lines should be identical
sort /tmp/determinism.log | uniq | wc -l
# Should be: 1
```

### Scenario 5: SLA Verification (< 5 seconds)

**Goal:** Prove recovery time meets SLA

**Steps:**
1. Inject failure and measure recovery time
2. Repeat 20+ times with different operations
3. Calculate average recovery time
4. Verify all runs < 5 seconds

**Expected Result:**
- Average recovery: 2-4 seconds
- Max recovery: < 5 seconds
- Zero SLA violations

**Iterations:** 20+ runs

**Verification:**
```bash
for i in {1..20}; do
  start=$(date +%s%N)
  
  # Inject failure and measure recovery
  # ... (see test scenarios above)
  
  end=$(date +%s%N)
  duration_ms=$(( (end - start) / 1000000 ))
  duration_sec=$(( duration_ms / 1000 ))
  
  echo "$duration_sec" >> /tmp/recovery_times.log
done

# Check all times
awk '{if ($1 > 5) print "VIOLATION: " $1 "s"}' /tmp/recovery_times.log
# Should have no output

# Calculate average
awk '{sum+=$1; n++} END {print "Average: " sum/n "s"}' /tmp/recovery_times.log
```

---

## Test Execution

### Prerequisites

```bash
# Install dependencies
gcloud auth login
gcloud config set project <PROJECT_ID>

# Create VMs (see vm_test_harness.sh)
./scripts/vm_test_harness.sh setup

# Install StePanel on all VMs
# ... (deployment steps)
```

### Run All Tests

```bash
#!/bin/bash
cd /home/stephanl/StePanel

# SIGKILL test
./scripts/vm_test_harness.sh test-sigkill
[ $? -eq 0 ] || exit 1

# ENOSPC test
./scripts/vm_test_harness.sh test-enospc
[ $? -eq 0 ] || exit 1

# Database offline test
./scripts/vm_test_harness.sh test-db-offline
[ $? -eq 0 ] || exit 1

# Determinism test (100+ runs)
./scripts/vm_test_harness.sh test-determinism
[ $? -eq 0 ] || exit 1

# SLA verification
./scripts/vm_test_harness.sh verify-sla
[ $? -eq 0 ] || exit 1

echo "✅ All Phase 7 tests PASSED"

# Cleanup
./scripts/vm_test_harness.sh cleanup
```

### Expected Timeline

- Setup: 15-20 minutes
- SIGKILL test: 5-10 minutes
- ENOSPC test: 5-10 minutes
- Database offline: 5-10 minutes
- Determinism test (100 runs): 20-30 minutes
- SLA verification: 10-15 minutes
- Cleanup: 5 minutes

**Total: ~1-1.5 hours**

---

## Success Criteria

**Phase 7 is complete when:**

- ✅ SIGKILL test: 10/10 runs pass (no half-states)
- ✅ ENOSPC test: 10/10 runs pass (graceful failure + recovery)
- ✅ Database offline test: 10/10 runs pass (timeout + recovery)
- ✅ Determinism test: 100/100 runs identical
- ✅ SLA test: 20/20 runs < 5 seconds
- ✅ Audit trail: All events logged correctly
- ✅ Zero mysterious failures

---

## Monitoring

### During Tests

Monitor VM health in real-time:

```bash
# Watch StePanel logs
ssh vm-a "tail -f /var/log/stepanel/broker.log"

# Watch recovery journal
ssh vm-a "watch -n 1 'ls -la /var/lib/stepanel/recovery/'"

# Watch database status
ssh vm-c "watch -n 1 'systemctl status mysql'"
```

### Metrics Captured

For each test run:
1. **Recovery time** (seconds)
2. **Event sequence** (determinism)
3. **Error messages** (consistency)
4. **Journal state** (consistency)
5. **Audit events** (completeness)

---

## Post-Testing

### Analysis

```bash
# Aggregate results
./scripts/analyze_vm_test_results.sh /tmp/test_results/

# Generate report
# - Recovery time statistics
# - Determinism matrix (100 runs)
# - SLA compliance
# - Failure summaries
```

### Report Contents

1. **Executive Summary**
   - Total tests: X
   - Pass rate: Y%
   - SLA compliance: Z%

2. **Detailed Results**
   - Per-scenario results
   - Recovery time histogram
   - Determinism proof (all sequences identical)

3. **Recommendations**
   - Any SLA violations?
   - Any inconsistencies?
   - Production readiness assessment

---

## Related Files

- `scripts/vm_test_harness.sh` - Main test harness
- `scripts/analyze_vm_test_results.sh` - Results analysis (to be created)
- `docs/GATE5_COMPLETION_ROADMAP.md` - Phase 7 specifications
- `docs/GATE5_PROGRESS.md` - Progress tracking

---

## Gate 5 Final Status

After Phase 7 completion:

✅ Framework complete
✅ No half-states proven
✅ Deterministic recovery proven
✅ All 6 operations durable
✅ Workflow integration tested
✅ **Real OS-level failures tested** ← Phase 7
✅ SLA verified
✅ **Gate 5 APPROVED** ← Production Ready

---

## Next: Production Deployment

After Gate 5 approval, StePanel is production-ready:
- Deploy to staging environment
- Run continuous failure injection tests
- Monitor 30 days without issues
- Deploy to production
- Monitor production failures
- Publish production readiness report

---

**Phase 7 proves that StePanel survives real-world failures reliably, deterministically, and safely.**
