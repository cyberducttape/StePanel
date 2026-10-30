# StePanel Project Status

**Released version:** v0.7.0 (Operator Beta)
**Branch:** `main` carries unreleased work on top of v0.7.0
**Production decision:** **NO-GO** for v1.0
**Last reviewed:** 2026-10-30

This page is a short summary. It does not approve releases and does not
repeat details kept elsewhere.

## Where to find what

| Question | Authoritative document |
| --- | --- |
| Can this source be released? | [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md), the only release-gate document |
| What evidence exists for an exact commit? | The [Release evidence workflow](https://github.com/cyberducttape/StePanel/actions/workflows/evidence.yml), or `go run ./cmd/stepanel-evidence --commit <rev>` |
| What work remains, in what order? | [DEVELOPMENT_PLAN.md](../DEVELOPMENT_PLAN.md) |
| What happened recently, with results? | [PROGRESS.md](../PROGRESS.md) and the `[Unreleased]` section of [CHANGELOG.md](../CHANGELOG.md) |
| Which features exist, and at what status? | [FEATURES.md](./FEATURES.md) |
| Where is the product going after 1.0? | [ROADMAP.md](./ROADMAP.md) |
| What can a deployment rely on today? | [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) |
| Dated host and recovery evidence | [lab-results/](./lab-results/); older hand-written snapshots are in [the evidence history](./archive/EVIDENCE_HISTORY.md) |

## Summary

StePanel v0.7.0 is suitable for controlled single-host deployments run by
experienced operators. The feature set for a single-host, operator-managed
PHP/WordPress product is in place; what blocks 1.0 is evidence that it
recovers from real host failures, repeatedly, on the supported matrix. The
open P0 work is repeated installed-host recovery cohorts, application
recovery certification, proven alert delivery, a measured capacity envelope,
and upgrade/rollback certification (milestones M1–M5 in the development
plan).

## Deployment classification

**Current:** Operator Beta
**Target for v1.0:** Single-host production

| Classification | When ready | Notes |
|----------------|-----------|-------|
| **Development** | ✅ Now | Local development, full feature set |
| **Operator Beta** | ✅ Now (v0.7.0) | Single-host deployment; operator expertise required |
| **Single-host production** | When every P0 gate in V1_PRODUCTION_GATES.md closes | Requires repeated real-host recovery evidence |
| **Multi-tenant production** | v2.0+ | Tenant roles and host resource envelopes exist; requires audit segregation, an HA datastore, and cross-host job routing |

## Known limitations (Operator Beta)

### Single-host constraints

- **No active-active HA:** durable jobs are tied to the local database; there
  is no cross-host failover.
- **No backup HA:** scheduled backups can require an offsite copy, but there
  is no automatic failover to a standby host.
- **Downtime on upgrade:** upgrades restart the panel; there is no canary or
  rolling deployment.
- **Single point of failure:** all control-plane data is on one host.

### Not ready for

- **Multi-tenant SaaS:** requires audit segregation, an HA datastore,
  cross-host job routing, and a hostile-workload isolation boundary.
- **High availability:** no stateless API, shared session store, or
  cross-region replication.
- **Automated recovery:** restore rehearsals run automatically, but
  restoring a production site is an operator action; there is no
  auto-remediation.
- **Enterprise SLAs:** SLA tracking and escalation routing are not
  implemented.

## Site lifecycle workflows

| Workflow | Status | Notes |
|----------|--------|-------|
| **cpmove import** | ✅ Implemented and recovery-tested | Journaled activation; exportable migration evidence |
| **WPress import** | ✅ Implemented; certification pending | Serialized-safe prefix conversion |
| **Generic archive import** | ✅ Implemented | Optional automated database restore |
| **Git deployment** | ✅ Implemented and recovery-tested | Atomic activation and rollback |
| **Backup restore** | ✅ Implemented | Files, databases, and restore to staging |
| **New WordPress site** | ✅ Beta | Durable job with database, HTTPS route, first verified backup, and daily schedule |
| **WordPress staging promotion** | ✅ Beta | Previewed; promotes into a new database and keeps the previous one |

Imports, deployments, restores, and promotions stage work in a private
directory under a recovery journal and roll back before production
activation when a step fails.
