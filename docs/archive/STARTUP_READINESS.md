# Startup Readiness Model

## Issue: Blocking Reconciliation on Startup (HIGH)

**Problem**: Control plane spends minutes reconciling before HTTP server accepts requests, creating poor UX during restarts/incidents.

**Current (blocking) flow**:
```
Start process
  ↓
Reconcile routes (0-2 min)
  ↓
Reconcile SSH access (0-2 min)
  ↓
Reconcile workers (0-2 min)
  ↓
... (6 more domains)
  ↓
Start HTTP server (NOW accepting requests)
```

**Total startup time**: 8 × 2 min = 16+ minutes before HTTP server listens.

---

## Solution: Early HTTP Start with Degraded Readiness

**Proposed flow**:
```
Start process
  ↓
Start HTTP server (IMMEDIATELY accepting requests)
  ├─ /livez = 204 (process alive)
  └─ /readyz = 503 (degraded, reconciliation in progress)
  ↓
(Background: Reconcile all domains concurrently via worker jobs)
  ↓
Startup complete (reconciliation finished)
  └─ /readyz = 200 (ready)
```

**Total startup time**: ~100ms until HTTP available, full reconciliation runs in background.

---

## Implementation Plan

### Phase 1: Early HTTP Start (This PR)
1. Move reconciliation calls **after** HTTP server start
2. Run reconciliation in background goroutine
3. Add `StartupReady` flag to App struct
4. Update `/readyz` to check `StartupReady`
5. Keep `/livez` unchanged (always 204)

### Phase 2: Async Reconciliation (Future)
1. Convert reconciliation to durable jobs
2. Run via worker system
3. Parallelize independent domains
4. Reduce startup time further

---

## Endpoints

### `/livez` (Liveness)
- **Meaning**: Is the process alive?
- **Response**: Always 204 No Content
- **When to use**: Kubernetes/systemd to detect process death
- **SLA**: Must respond in <100ms

### `/readyz` (Readiness)
- **Meaning**: Is the control plane ready to serve requests?
- **Response**: 
  - 200 OK + `{"ready": true}` when fully initialized
  - 503 Service Unavailable + `{"ready": false}` when degraded
- **Checks during startup**:
  - Audit state: OK (required for logging)
  - Job store: OK (required for operations)
  - Offsite backup: OK if enabled
  - Storage capacity: OK if minimum threshold set
  - **Startup reconciliation**: NOT OK while in progress
- **When to use**: Load balancers, health dashboards, readiness probes
- **SLA**: Must respond in <500ms

---

## Security Model

During reconciliation-in-progress (startup):
- **Allowed**: Read-only operations (backups list, site info, settings)
- **Blocked**: Mutations that depend on reconciliation state
  - Site operations (those modify routes, SSH, workers, etc.)
  - Resource enforcement
  - Environment variable changes
- **Load balancer behavior**: Removes from rotation until `/readyz` returns 200

---

## Rollback Plan

If reconciliation in background causes issues:
1. Add environment variable `STEPANEL_BLOCKING_STARTUP=1`
2. When set, move reconciliation back into startup path (old behavior)
3. Reduces startup speed but restores blocking semantics
4. Monitor for any causality issues
5. Investigate and fix root cause before removing flag

---

## Monitoring

Add metrics:
- `startup_reconciliation_duration_seconds` - time to complete all reconciliation
- `startup_ready_at_seconds` - when /readyz first returned 200
- `http_server_start_latency_ms` - time from process start to HTTP listening

---

## References

- `main.go` lines 373-388: Current blocking reconciliation
- `health.go`: Liveness and readiness endpoints
- `main.go` lines 567-574: HTTP server startup
