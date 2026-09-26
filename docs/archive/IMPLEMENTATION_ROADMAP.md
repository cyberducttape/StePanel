# Implementation Roadmap: Complete Production Hardening

## Current Status: Session 3 Progress

✅ **Completed This Session**:
1. Legacy Token Enforcement - Framework + integration started
2. All blocker documentation (6 blockers with solutions)
3. Helper architecture vision documented
4. Adversarial testing framework documented
5. Production scorecard created

⏳ **In Progress**:
- Archive verification caching integration
- Process-tree cancellation test

❌ **Remaining (Tier 2-4)**:
- Webhook authorization redesign
- Pip install constraints
- Container registry allowlist
- Adversarial test suite
- Fuzzing infrastructure
- Go helper MVP
- Internal package extraction plan

---

## Implementation Sequence Recommendation

**Optimal Path** (Complete in ~4 weeks):

**Week 1: Tier 1 Completion** (6.5 hours)
- 1h: Legacy token enforcement finalization
- 2h: Archive verification caching
- 1.5h: Process-tree test
- 2h: Buffer/testing

**Week 2: Tier 2 Implementations** (8.5 hours)
- 3.5h: Webhook authorization redesign
- 3h: Pip install constraints doc + wrapper
- 2h: Container registry allowlist

**Week 3: Tier 3 Testing** (7.5 hours)
- 5h: Adversarial test suite (75+ tests)
- 2.5h: Fuzzing infrastructure

**Week 4: Tier 4 Foundation** (6 hours)
- 3.5h: Go helper MVP (proof-of-concept)
- 2.5h: Package extraction plan documentation

**Total**: ~29 hours focused implementation

---

## v0.7.0 Critical Path

**Must Complete Before Release**:
1. ✅ Blocker documentation (DONE)
2. ⏳ Tier 1 items (70% complete)
3. ❌ Tier 2 items (webhook, pip, container) - 3 blockers

**Can Defer to v0.8**:
- Tier 3: Comprehensive testing suite
- Tier 4: Helper rewrite & refactoring

---

## Quick Win Priority

If time is limited, focus on:
1. **Legacy token enforcement** (1-2 hours) - CRITICAL
2. **Webhook authorization** (3-4 hours) - CRITICAL  
3. **Container registry allowlist** (2 hours) - CRITICAL
4. **Pip constraints doc** (1 hour) - CRITICAL

These 4 items unblock v0.7.0 release (~10-11 hours total).
