# Documentation Archive

This directory contains historical documentation that is no longer actively maintained but is preserved for reference and traceability.

## What's Here

- **Audit findings** - Historical security audits and gap analyses retained for traceability
- **Detailed phase plans** - Implementation details from completed development phases
- **Historical release scorecards** - Previous assessments of production readiness
- **Legacy architecture docs** - Snapshots of architecture state at different release milestones
- **Experimental feature plans** - Features that were planned but may no longer be current

## Why Archived

These documents are preserved because:
1. They provide historical context for understanding implementation decisions
2. They contain detailed traceability of security issues and their fixes
3. They document architectural evolution over time
4. They may contain information needed for understanding recovery procedures

## Using This Archive

**Do not treat archived documents as current reference material.** They are historical only.

For current information:
- **Production readiness**: See `../PRODUCTION_READINESS.md`
- **Security status**: See `../SECURITY.md` and `../V1_PRODUCTION_GATES.md`
- **Architecture**: See `../ARCHITECTURE.md`
- **Features**: See `../FEATURES.md`
- **Roadmap**: See `../ROADMAP.md`

## Index

- `archive/PRODUCTION_SCORECARD.md` (archived) - Historical production readiness assessment (v0.7.0 era)
- `SECURITY_GAPS_FOUND.md` - Historical security audit with resolved and fixed findings
- `archive/ARCHIVE_IMPORT_LIFECYCLE.md` (archived) - v0.7.0-era archive import workflow (now uses transactional activation)
- `PHASE*_*.md` - Detailed implementation plans from completed development phases
- Other roadmaps and plans - Historical planning documents superseded by current roadmap

Archived in the 2026-10-10 documentation sprint (completed or superseded work):

- `ARCHITECTURE_REORGANIZATION_ROADMAP.md` - Former root `ARCHITECTURE.md`; proposed package layout that the tree did not adopt
- `REFACTORING_PLAN.md` - Root-package conversion plan; Phase 1 shipped (`package stepanel`, `cmd/` binaries)
- `HELPER_LAYER_COMPLETION_SUMMARY.md`, `HELPER_ARCHITECTURE.md`, `ROOT_HELPER_MODERNIZATION.md` - Helper-layer migration history; superseded by `../ROOT_BROKER_INTEGRATION.md` and `../SUDO_THREAT_MODEL.md`
- `GATE5_HARDENING_STRATEGY.md`, `GATE5_INTEGRATION_GUIDE.md`, `GATE5_FAILURE_INJECTION_TESTING.md` - Gate 5 design and framework plans; implemented, with current status in `../V1_PRODUCTION_GATES.md` and `../REAL_HOST_FAILURE_MATRIX.md`
