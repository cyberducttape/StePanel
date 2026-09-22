# StePanel Production Readiness

**Last Updated:** 2026-09-20  
**Status:** Single-Host Production Candidate (v0.7.0)

This is the authoritative document for StePanel's production readiness status. Other documents (SECURITY.md, PRODUCTION_SCORECARD.md, PRODUCTION_GAP_ANALYSIS.md) reference this.

## Deployment Classification

- **Development**: Local development only
- **Operator Beta**: Limited single-host deployment; operator expertise required
- **Single-Host Production Candidate**: Ready for single-host production deployments
- **Multi-Tenant Production**: Ready for multi-tenant SaaS deployments
- **GA (General Availability)**: Recommended for all deployment scenarios

## Current Status: Single-Host Production Candidate

StePanel v0.7.0 is ready for **single-host production deployments** with the following caveats:

### ✅ Strengths

- **Security**: Comprehensive TOTP, token rate-limiting, SSRF protection, archive validation
- **Isolation**: Process-tree isolation, filesystem bounds, symlink rejection, container security
- **Reliability**: Durable job system, distributed locks, recovery drills, backup verification
- **Operational**: Audit logging, real-time metrics, site lifecycle management

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
| **cpmove** | ✅ Full | ✅ Staged | ✅ Yes | ✅ Transform | ✅ Transactional | Most complete workflow |
| **WordPress Import** | ✅ Full | ✅ Full | N/A | ✅ Transform | ✅ Transactional | WP-specific optimizations |
| **Generic Archive** | ✅ Full | ⚠️ Manual | ⚠️ Manual | ✅ Transform | ✅ Transactional | DB dump located and manual restore instructions provided |
| **Git Deployment** | ✅ Full | N/A | N/A | ✅ Config | ✅ Git-based | Application-driven |

**Legend:**
- ✅ = Fully supported
- ⚠️ = Partially supported or manual step required
- ❌ = Not supported
- N/A = Not applicable to this workflow

### Generic Archive Importer

The generic archive importer supports end-to-end file and database restoration:

- ✅ Archive inspection and validation
- ✅ File extraction with permission preservation
- ✅ Configuration file updates
- ✅ Database dump location and validation
- ⚠️ Database restoration (manual via provided SQL dump in v0.7.0, automated in v0.8.0)
- ❌ Mail system migration
- ✅ Transactional guarantees

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
- [PRODUCTION_SCORECARD.md](./PRODUCTION_SCORECARD.md) - v0.7.0 release checklist
- [PRODUCTION_GAP_ANALYSIS.md](./PRODUCTION_GAP_ANALYSIS.md) - Known limitations and blockers
