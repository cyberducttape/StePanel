# StePanel Production Readiness

**Last Updated:** 2026-09-26
**Status:** Operator Beta; release approval is governed by [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md)

**👉 For overall project status, see [CURRENT_STATUS.md](./CURRENT_STATUS.md) — the single authoritative source for version, progress, and release gates.**

This document describes deployment capabilities and limitations. It is not a release approval; the sole current release-gate document is [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md). Older scorecards and audits are historical evidence only.

## Deployment Classification

- **Development**: Local development only
- **Operator Beta**: Limited single-host deployment; operator expertise required
- **Single-Host Production Candidate**: Ready for single-host production deployments
- **Multi-Tenant Production**: Ready for multi-tenant SaaS deployments
- **GA (General Availability)**: Recommended for all deployment scenarios

## Current Status: Operator Beta

StePanel v0.7.0 is suitable for controlled evaluation and operator-led staging. It is not approved as a general single-host production release until the open gates in `V1_PRODUCTION_GATES.md` have recorded evidence.

### ✅ Strengths

- **Security**: Comprehensive TOTP, token rate-limiting, SSRF protection, archive validation
- **Isolation**: Process-tree isolation, filesystem bounds, symlink rejection, container security
- **Reliability**: Durable job system, recovery journals, backup verification
- **Operational**: Audit logging, real-time metrics, and operator-visible lifecycle workflows

### ⚠️ Single-Host Limitations

- **No active-active HA**: Durable jobs tied to local database; no cross-host failover
- **No backup HA**: Manual backup exports; no automatic failover to standby
- **Downtime on deploy**: Updates require restart; no canary/rolling deployment
- **Single point of failure**: All control-plane data on single host

### ⛔️ Not Ready For

- **Multi-tenant SaaS**: Requires RBAC per customer, audit segregation, resource quotas
- **High-availability**: No stateless API, no shared session store, no cross-region replication
- **Automated recovery**: Recovery drills are manual; no auto-remediation
- **Enterprise support**: SLA tracking, escalation routing not implemented

## Migration Capabilities by Workflow

| Workflow | Files | Database | Mail | Config | Rollback | Notes |
|----------|-------|----------|------|--------|----------|-------|
| **cpmove** | ✅ Implemented | ✅ Staged | ✅ Yes | ✅ Transform | ✅ Journaled | Most complete; recovery-tested |
| **WordPress Import** | ✅ Implemented | ✅ Implemented | N/A | ✅ Transform | ✅ Journaled | WP-specific optimizations; pending certification gate |
| **Generic Archive** | ✅ Implemented | ✅ Optional automated | ⚠️ Manual when no credentials | ✅ Transform | ✅ Journaled | Automatic managed restore requires `auto_restore_db` and valid database password; pending certification gate |
| **Git Deployment** | ✅ Implemented | N/A | N/A | ✅ Config | ✅ Git-based | Application-driven; pending certification gate |

**Legend:**
- ✅ **Implemented** = Code complete; feature is functional
- ⚠️ **Partially supported** = Implemented with manual workarounds or conditional requirements
- ❌ **Not supported** = Not implemented
- **Journaled** = Staged filesystem activation with recovery journal; not ACID across all resources
- N/A = Not applicable to this workflow

### Generic Archive Importer

The generic archive importer supports file migration and optional automated managed database restoration:

- ✅ Archive inspection and validation
- ✅ File extraction with permission preservation
- ✅ Configuration file updates
- ✅ Database dump location and validation
- ✅ Recovery journal staging (v0.7.0) — enables rollback on failure
- ✅ Automated database restoration when `auto_restore_db` and a valid database password are supplied
- ⚠️ Without credentials, the SQL dump remains an explicit operator follow-up
- ❌ Mail system migration
- ✅ Journaled staged activation with rollback — filesystem and database operations staged, recovery journal enables rollback on failure before activation to production

**About "Journaled Staged Activation":**
StePanel provides filesystem and database staging with recovery journals that enable rollback *before* activating to production. This is NOT an ACID transaction across the entire system (filesystem, database, systemd services, webserver, audit log, state store). An SRE must understand that:
- Filesystem is staged in a private directory before rename to production
- Database is prepared and validated before connection switch
- If activation is interrupted, recovery journals enable cleanup and retry
- Once activated to production, there is no automatic rollback; restoration uses backup workflows

Use generic archive import for:
- Migrating sites from other hosting providers
- Restoring StePanel-exported backups
- Extracting files with database dump support
- Single-host deployments with DBA assistance

Do NOT use for:
- Mail system migrations (use external mail provider)
- Zero-downtime cutover (requires manual database import step)
- Multi-host distributed deployments (v0.7.0 single-host only)

## Deployment Prerequisites

### System Requirements

- **OS**: Linux (kernel 5.4+) with systemd
- **Storage**: 100GB+ for backups + archives
- **Memory**: 4GB minimum, 8GB recommended
- **Network**: Public HTTPS, 2 static IPs (panel + failover DNS)

### Security Prerequisites

- TLS certificates (Let's Encrypt or internal CA)
- Offsite backup target (S3, B2, or similar)
- Admin TOTP devices configured
- SSH key rotation policy

### Operational Prerequisites

- **Backup tests**: Monthly verification of recovery drills
- **Monitoring**: Prometheus + Grafana or equivalent
- **Runbooks**: Documented procedures for common issues
- **On-call**: 24/7 availability for critical incidents

## Production Readiness Validation

StePanel provides tools to verify deployment prerequisites are met:

### Startup Validation

In production mode (`STEPANEL_ENV=production`), StePanel performs mandatory checks on startup:

- ✅ **Filesystem quotas enforced**: Validates that `STEPANEL_WEB_ROOT` filesystem has usrquota/grpquota enabled. Required because StePanel advertises disk quotas in plans; quotas that aren't enforced create false security guarantees. Startup fails if quotas unavailable; remediation: `mount -o remount,usrquota /var/www`
- ✅ **Encryption keys configured**: Ensures backup encryption keys are loaded (minimum 32 characters)
- ⚠️ **Offsite backups configured**: Checks S3/B2 credentials for offsite backup target (warning only; can proceed without)

### Production Readiness Endpoint

Administrators can verify production prerequisites at runtime via:

```
GET /api/admin/production-readiness
```

Returns JSON with overall_status (healthy/degraded/critical) and per-check details:

```json
{
  "is_production": true,
  "overall_status": "healthy",
  "checks": [
    {
      "name": "Filesystem Quotas",
      "status": "pass",
      "severity": "critical",
      "message": "Quotas enforced via usrquota"
    },
    {
      "name": "Encryption Keys",
      "status": "pass",
      "severity": "critical"
    },
    {
      "name": "Offsite Backups",
      "status": "warning",
      "severity": "warning",
      "message": "No offsite backup policy detected"
    },
    {
      "name": "TLS Configuration",
      "status": "pass",
      "severity": "critical"
    }
  ]
}
```

Use this endpoint in deployment automation to validate prerequisites before routing traffic to StePanel.

## Blocking Issues for Multi-Tenant Production

The following must be resolved before multi-tenant deployment:

1. ✅ SSRF protection with DNS resolution (RESOLVED in v0.7.0)
2. ✅ Shell injection prevention (RESOLVED in v0.7.0)
3. ✅ Safe config file updates (RESOLVED in v0.7.0)
4. ✅ Archive type validation (RESOLVED in v0.7.0)
5. ✅ Path traversal prevention (RESOLVED in v0.7.0)
6. ❌ RBAC per-customer (NOT STARTED)
7. ❌ Audit log segregation (NOT STARTED)
8. ❌ Cross-host session replication (NOT STARTED)
9. ❌ Resource quota enforcement (NOT STARTED)
10. ❌ Multi-region replication (NOT STARTED)

## Path to GA (v1.0.0)

### v0.8.0 (Planned)

- [ ] High-availability datastore (etcd or PostgreSQL)
- [ ] Stateless API servers with load balancing
- [ ] Cross-host durable job routing
- [ ] Canary deployment support

### v0.9.0 (Planned)

- [ ] Multi-tenant RBAC
- [ ] Per-customer audit log isolation
- [ ] Resource quota enforcement
- [ ] Automated compliance reporting

### v1.0.0 (GA Target)

- [ ] Multi-region replication
- [ ] SLA monitoring and escalation
- [ ] Automated incident response
- [ ] Enterprise support tiers

## Support Matrix

| Issue Type | Single-Host | Multi-Tenant | SaaS GA |
|-----------|-------------|--------------|---------|
| Security patches | 72 hours | 24 hours | 4 hours |
| Bug fixes | 2 weeks | 1 week | 2 days |
| Feature requests | Quarterly | Monthly | Continuous |

## Deprecation Policy

- **Upcoming deprecations** are announced 2 releases in advance
- **Removed features** are retired 3 releases after deprecation
- **Security fixes** backported 1 year to prior minor versions

---

## Related Documents

- [SECURITY.md](./SECURITY.md) - Security features and threat model
- [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md) - current release gates and evidence
- [PRODUCTION_SCORECARD.md](./PRODUCTION_SCORECARD.md) - historical scorecard; not a release gate
- [PRODUCTION_GAP_ANALYSIS.md](./PRODUCTION_GAP_ANALYSIS.md) - historical gap analysis; verify against the gates
