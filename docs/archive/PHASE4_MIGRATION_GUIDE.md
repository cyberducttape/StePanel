# Phase 4: Gradual Migration Guide

**Status:** ⏳ QUEUED (Ready to begin)  
**Effort:** 2 weeks  
**Risk:** Low (parallel deployment strategy)

## Overview

Phase 4 replaces shell script helper calls with the typed Go root broker. This uses a gradual migration strategy that allows both systems to run in parallel, reducing deployment risk.

## Migration Strategy

### Stage 1: Parallel Deployment (Week 1)

Both systems deployed together:
```
┌─────────────────────────────┐
│  StePanel App (v2.5.0)      │
├─────────────────────────────┤
│                             │
│ ┌─────────────┐  ┌───────┐ │
│ │ Old code:   │  │ New:  │ │
│ │ Shell       │  │ Broker│ │
│ │ helpers     │  │       │ │
│ └─────────────┘  └───────┘ │
│      ↓               ↓      │
│   sudo -u           sudo    │
│  stepanel          /usr/.. │
│  stepanel-*       /stepan.. │
│                             │
│ BOTH AVAILABLE              │
│ NO NEW CODE USES BROKER YET │
└─────────────────────────────┘
```

**Actions:**
1. Deploy broker binary alongside shell scripts
2. Deploy broker bridge wrapper
3. NO code changes yet - all existing code continues using shell
4. Broker is dormant but available

**Testing:**
- Unit tests pass (already verified)
- Integration tests pass (already verified)
- Broker can be spawned manually
- Manual testing of broker RPC (optional)

### Stage 2: Selective Callsite Replacement (Week 2)

Start replacing shell helper calls with broker calls:

**Pattern: Shell Helper → Broker Bridge**

Before:
```go
// workers.go
func (a *App) recordWorkerError(key string, applyErr error) {
    // ... existing code ...
    _ = a.Workers.save(key, worker)  // Ignored error (fixed in P1)
}
```

After:
```go
// Use broker bridge for system operations
// (but Workers.save is an in-process store, not system operation)
```

**Actually replaceable operations** (shell helper calls):

| File | Function | Current | New |
|------|----------|---------|-----|
| sites.go | site creation | `runHelperCommand(ctx, config, config.SiteCtl, "create", site)` | `brokerBridge.SiteCreate(ctx, site, keys)` |
| sites.go | site deletion | `runHelperCommand(ctx, config, config.SiteCtl, "delete", site)` | `brokerBridge.SiteDelete(ctx, site)` |
| routes.go | vhost apply | `runHelperCommand(ctx, config, config.VHostCtl, "apply", ...)` | `brokerBridge.VhostApply(ctx, site, domain, ws)` |
| routes.go | vhost delete | `runHelperCommand(ctx, config, config.VHostCtl, "delete", name)` | `brokerBridge.VhostApply(ctx, ...)` |
| php.go | PHP config | `runHelperCommand(ctx, config, config.SiteCtl, "runtime", ...)` | `brokerBridge.AppApply(ctx, ...)` |
| backup_restore.go | DB restore | `runHelperCommand(ctx, config, config.DbCtl, ...)` | `brokerBridge.DBProvision(ctx, ...)` |

**Replacement process:**
1. Identify a shell helper call site
2. Check if broker bridge has a method for it
3. Replace with: `bridge.Method(ctx, args)`
4. Add error handling (errors no longer ignored)
5. Test with broker running

### Stage 3: Monitoring & Validation (Week 3-4)

Run both systems in parallel, monitor results:

```go
// Hybrid approach: try broker first, fall back to shell
func (a *App) applyVhost(ctx context.Context, site, domain string) error {
    // Try broker-based approach
    if a.brokerBridge != nil {
        if err := a.brokerBridge.VhostApply(ctx, site, domain, "caddy"); err == nil {
            a.metrics.brokerSuccess++
            return nil
        }
        a.metrics.brokerFailure++
    }
    
    // Fall back to shell (if broker fails or unavailable)
    return runHelperCommand(ctx, a.Config, a.Config.VHostCtl, "apply", site, domain)
}
```

**Monitoring:**
- Count successful broker calls vs. shell calls
- Monitor error rates for broker vs. shell
- Compare performance (throughput, latency)
- Check for inconsistencies

**Logging:**
```go
a.logger.Printf("vhost applied via broker: %s (success)", domain)
a.logger.Printf("vhost apply failed via broker, fell back to shell: %s (error: %v)", domain, err)
```

### Stage 4: Complete Cutover (Week 4)

All calls migrated to broker:

```
Before: Code → Shell helper → System ops
After:  Code → Broker bridge → Broker → System ops
```

**Checklist:**
- [x] All shell helper callsites identified (grep `runHelperCommand`)
- [ ] All callsites replaced with broker calls
- [ ] Monitoring shows broker success rate > 99%
- [ ] No functionality regressions
- [ ] Error handling verified
- [ ] Performance acceptable

### Stage 5: Deprecation (Release N+1)

Mark shell scripts as deprecated, prepare for removal:

```bash
# In release notes:
# Shell helpers (stepanel-*ctl) are deprecated and will be removed in v2.6.0
# All privileged operations now go through the typed Go broker
```

**Actions:**
1. Remove shell script files from deployment
2. Archive scripts in git for reference
3. Update documentation
4. Update SECURITY.md with new threat model

## Implementation Pattern

### Basic Replacement

**Shell helper call (old):**
```go
err := runHelperCommand(ctx, a.Config, a.Config.SiteCtl, "create", site)
if err != nil {
    return fmt.Errorf("site creation failed: %w", err)
}
```

**Broker bridge call (new):**
```go
if a.brokerBridge == nil {
    return fmt.Errorf("broker not available")
}
err := a.brokerBridge.SiteCreate(ctx, site, sshKeys)
if err != nil {
    return fmt.Errorf("site creation failed: %w", err)
}
```

### Error Handling Improvement

**Old (ignored errors):**
```go
_ = a.Routes.save(route)  // PROBLEM: Error ignored
```

**New (proper error handling via P1 fix):**
```go
a.SaveRouteState(route)  // Returns error, marks readiness unhealthy if it fails
```

### Fallback Pattern (During Migration)

```go
// Use broker if available, fall back to shell
func (a *App) vhostApply(ctx context.Context, site, domain string) error {
    if a.brokerBridge != nil {
        if err := a.brokerBridge.VhostApply(ctx, site, domain, a.Config.Webserver); err == nil {
            return nil
        }
        // Log fallback, continue to shell
        a.logger.Printf("broker failed, using shell fallback: %v", err)
    }
    
    // Original shell-based implementation
    return runHelperCommand(ctx, a.Config, a.Config.VHostCtl, "apply", site, domain)
}
```

## App Integration Points

### Where BrokerBridge Gets Created

In `main.go` startup:

```go
// Initialize broker bridge (optional, graceful if unavailable)
brokerBridge, err := NewBrokerBridge(a.logger)
if err != nil {
    a.logger.Printf("warning: broker not available: %v", err)
    // Continue without broker - shell helpers still available
} else {
    a.brokerBridge = brokerBridge
}
```

### Where BrokerBridge Gets Used

In operation handlers:

```go
// sites.go - Site creation
func (a *App) createSite(ctx context.Context, name string) error {
    // Use broker if available
    if a.brokerBridge != nil {
        return a.brokerBridge.SiteCreate(ctx, name, sshKeys)
    }
    
    // Fall back to shell
    return runHelperCommand(ctx, a.Config, a.Config.SiteCtl, "create", name)
}

// routes.go - Virtual host management
func (a *App) applyVhost(ctx context.Context, site, domain string) error {
    if a.brokerBridge != nil {
        return a.brokerBridge.VhostApply(ctx, site, domain, a.Config.Webserver)
    }
    return runHelperCommand(ctx, a.Config, a.Config.VHostCtl, "apply", site, domain)
}

// php.go - Runtime configuration
func (a *App) applyPHPProfile(ctx context.Context, p PHPProfile) error {
    if a.brokerBridge != nil {
        return a.brokerBridge.AppApply(ctx, p.Site, p.Version, 0)
    }
    return runHelperCommand(ctx, a.Config, a.Config.SiteCtl, "runtime", p.Site, p.Version, ...)
}
```

## Monitoring & Metrics

### What to Track

```go
type BrokerMetrics struct {
    SuccessfulCalls  int64
    FailedCalls      int64
    FallbackCalls    int64  // Fell back to shell
    AvgLatency       time.Duration
    LastError        string
    LastErrorTime    time.Time
}
```

### Alerting

```go
// Alert if broker failure rate exceeds threshold
if brokerMetrics.FailedCalls > 10 || 
   (brokerMetrics.FailedCalls * 100 / brokerMetrics.TotalCalls) > 5 {
    alertOp("broker failure rate too high")
}
```

### Logging Pattern

```go
a.logger.Printf("BROKER: operation=%s site=%s status=success", "site_create", site)
a.logger.Printf("BROKER: operation=%s site=%s status=fallback error=%v", "vhost_apply", domain, err)
```

## Risk Mitigation

### Rollback Plan

If broker has issues in production:

1. **Immediate:** Keep shell scripts deployed (don't remove yet)
2. **Quick fix:** Disable broker in config (`USE_BROKER=false`)
3. **Fallback:** App detects broker unavailable, uses shell helpers
4. **Recovery:** Fix broker bug, redeploy

### Testing Before Cutover

- [x] Unit tests passing (Phase 3 verified)
- [x] Integration tests passing (Phase 3 verified)
- [ ] Shadow testing: run broker in parallel with shell, compare results
- [ ] Stress testing: 100+ concurrent operations
- [ ] Failure injection: test graceful degradation
- [ ] Performance baseline: compare latency vs. shell

### Canary Deployment

1. Deploy to test environment first
2. Run for 24+ hours with full workload
3. Monitor metrics, logs, error rates
4. Fix any issues found
5. Deploy to production

## Success Criteria

- [x] Broker compiles and deploys successfully
- [x] Broker RPC communication works end-to-end
- [x] All input validation passes
- [ ] 100% of shell helper callsites identified
- [ ] All callsites have broker equivalents
- [ ] Broker success rate > 99% in production
- [ ] No functionality regressions
- [ ] Performance baseline met (< 50ms additional latency)
- [ ] Error handling improved (no silent failures)

## Timeline

| Week | Activity | Status |
|------|----------|--------|
| Week 1 | Deploy broker alongside shell scripts | 🔄 Ready |
| Week 2 | Replace first batch of callsites (10-20%) | 🔄 Ready |
| Week 3 | Replace second batch (40-60%), monitor | ⏳ Planned |
| Week 4 | Complete migration, remove shell scripts | ⏳ Planned |

## Related Documents

- [HELPER_LAYER_ROADMAP.md](HELPER_LAYER_ROADMAP.md) - Overall strategy
- [ROOT_BROKER_INTEGRATION.md](ROOT_BROKER_INTEGRATION.md) - Integration API
- [PHASE3_INTEGRATION_TESTING.md](PHASE3_INTEGRATION_TESTING.md) - Phase 3 tests
- [broker_bridge.go](../broker_bridge.go) - Bridge implementation

## FAQ

**Q: What if broker is not available?**  
A: Fallback to shell helpers. App checks `a.brokerBridge != nil` before using broker.

**Q: Can we run both in parallel?**  
A: Yes, and recommended during migration. Shadow testing validates broker before full cutover.

**Q: What about performance?**  
A: ~1ms RPC overhead per call. Acceptable for most operations (typically 10-100ms total).

**Q: Can we roll back easily?**  
A: Yes. Keep shell scripts deployed. Disable broker (`USE_BROKER=false` in config). App falls back automatically.

**Q: How do we know when migration is complete?**  
A: When all `runHelperCommand` calls go through `brokerBridge` instead of directly to shell.

---

**Next Steps:** Begin Phase 4 implementation by identifying and replacing first batch of shell helper callsites.
