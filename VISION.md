# StePanel Vision: Production Adoption Roadmap

## The Real Adoption Hurdle

Feature count doesn't drive adoption of hosting control panels.

**Trust at 3 AM** does.

When something has gone horribly wrong, will you trust StePanel to help you recover? Or will you be terrified of making it worse?

## Milestones to Production Trust

In order:

### 1. Quarantine the Archive Importer ⚠️ (v0.7 CRITICAL)
- ✅ **Fixed**: No longer reports false success
- ✅ **Fixed**: Database restoration and config rewrite no longer silent failures
- **Current**: Automatic DB restoration is available only through the managed
  helper and explicit request credentials; the no-credential path remains a
  documented manual follow-up.
- **Never**: Return "success" for skipped DB/config operations

This is the minimum bar. You've just cleared it. Don't regress.

### 2. Destructive Operation Tests (v0.8)
Build comprehensive tests for failure cases:

- Site deletion (what if midway fails?)
- Database deletion (partial failure recovery)
- Restore rollback (undo failed restore)
- Helper call failures (what if privilege escalation fails?)
- Disk-full during operation
- Process kill handling
- Corrupted state recovery
- Duplicate job submissions
- Partial backup recovery

Current adversarial test suite is good. Extend it to **recovery scenarios**.

### 3. Hostile Migration Corpus (v0.8)
Create a realistic test archive library:

- Path traversal attempts (thousands of patterns)
- Directory bombs (1M dirs, deeply nested)
- Duplicate filenames
- Malformed tar/zip headers
- Truncated streams mid-extraction
- FIFO/device entries
- Symlink races
- Weird WordPress configs (multisite, custom structure, bedrock)
- Sparse files (10GB sparse = small compressed)
- Archive bombs inside archives

Test against real-world migrations from other panels, not synthetic cases.

### 4. Tenant Escape Testing (v0.8)
Treat your own control plane as adversarial:

- User A: can I read User B's files?
- User A: can I inspect User B's processes?
- User A: can I reach User B's database sockets?
- User A: can I read deployment secrets?
- User A: can I abuse writable directories?
- User A: can I exploit the privileged helper?
- User A: can I interact with systemd?
- User A: can I escape container/cgroup limits?

This isn't theoretical. One broken path validation and you have privilege escalation.

### 5. Boring Installation (v0.8)
Someone installing StePanel shouldn't need to read source code:

- Alma Linux 8/9
- Rocky Linux 8/9  
- Debian 11/12
- Ubuntu 20.04/22.04/24.04
- Nginx + Apache
- MySQL 8.0 + MariaDB 10.5+
- PHP 7.4 through 8.3

Steps:
1. Provision host
2. `apt-get install stepanel` (or equivalent)
3. Create site
4. Deploy WordPress
5. Backup it
6. Restore it
7. Destroy it
8. Upgrade StePanel
9. Recover control plane

All without reading `main.go`.

### 6. Real Lab/Demo (v0.8)
Your README honestly admits: "representative data."

Publish an actual lab setup:

- Real Alma/Ubuntu host (not containers)
- Real WordPress installation (not mock)
- Real backup/restore cycle (not stub)
- Full upgrade cycle (v0.7 → v0.8)
- Real disaster recovery (host failure simulation)

Screencast or public CI artifact. Show end-to-end.

### 7. Documentation Collapse (v0.7 COMPLETE)
✅ Done: Single SECURITY.md  
✅ Done: ARCHITECTURE.md roadmap  
**Next**: Make "supported vs experimental vs planned" impossible to misread

Every feature gets a status badge:
- 🟢 Production (tested, audited, upgrade-safe)
- 🟡 Beta (works, edge cases exist, break-before-fix may happen)
- 🔴 Experimental (do not use for production)

No README footnotes. No "Phase 2" ambiguity. Badge on every feature.

### 8. Compatibility Matrix (v0.9)
Sysadmins need to know exactly what's tested:

| OS | Nginx | Apache | MySQL 8 | MariaDB | PHP 7.4 | PHP 8.0 | PHP 8.3 | CI |
|---|---|---|---|---|---|---|---|---|
| Alma 9 | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | CI |
| Rocky 9 | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | CI |
| Ubuntu 22.04 | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | CI |
| Debian 12 | ✅ | ✅ | ✅ | ✅ | ❌ | ✅ | ✅ | CI |

Update this matrix on every release. Run the combinations in CI automatically.

### 9. Doctor Command (v0.9)
Operators should be able to file issues with:

```bash
$ stepanel doctor > /tmp/health.txt
$ # ... omit secrets, review, then attach to GitHub issue
```

Output includes:
- Version, distro, kernel
- Helper binary checks (size, permissions, build date)
- DB/webserver status and version
- Storage available, cgroup/quota capabilities
- State schema version, migration status
- Recent failed jobs (last 10)
- Backup health (missing, orphaned, corrupt)
- CPU/memory/disk trends (if available)

This is 80% of what you'll ask for in support anyway.

### 10. Upgrade and Rollback Confidence (v1.0)
Control panels live for years. Upgrade safety matters more than features.

Checklist:
- [ ] State schema versioning (can downgrade)
- [ ] Atomic upgrades (no partial state)
- [ ] Backward compatibility tests (v0.8 → v0.9 → v0.10)
- [ ] Rollback procedure (tested, documented, automated)
- [ ] Zero-downtime upgrade option (if applicable)
- [ ] Database migration safety (transactions, rollback journals)
- [ ] Configuration migration (old → new config format)

Publish: "StePanel upgrade safety policy" with guarantees.

## The Killer Feature: Pre-Flight & Blast-Radius Engine

You already have pieces of this.

Before **any** destructive operation, show:

```
Delete Site: WordPress.example.com

⚠️  BLAST RADIUS
├─ Affected resources
│  ├─ Database: wordpress_example (24 GB)
│  ├─ Files: /var/www/wordpress.example (1.2 GB)
│  ├─ Backups: 28 (7 days, 850 GB total)
│  └─ Deployments: 12 (last 3 months)
├─ Dependencies
│  ├─ Certificates: Let's Encrypt renewal (11 days to expiry)
│  ├─ DNS: A record points here (manual cleanup needed)
│  └─ Monitoring: Grafana dashboard (will show "missing")
├─ What we will backup
│  └─ Full backup: incremental backup will run before deletion
├─ Can you rollback?
│  └─ ✅ Yes (until next backup pruning in 30 days)
├─ Estimated downtime
│  └─ Immediate (DNS redirect needed for email)
├─ Disk space requirement
│  └─ 2 GB free (we have 80 GB available)
└─ Health check
   └─ ✅ All systems nominal
```

After execution:

```
✅ Deletion complete (42 seconds)

VERIFICATION
├─ Database deleted ✅
├─ Files removed ✅
├─ Certificates revoked ✅
├─ Backups preserved ✅ (28 backups, 850 GB)
├─ DNS records still active ⚠️ (manual cleanup needed)
└─ Health check: Nominal ✅

RECOVERY
└─ Rollback available until 2026-10-20 ✅
   (via: stepanel site restore wordpress_example)

Audit ID: audit-20260920-145823
```

That's genuinely different.

It's **change management for people who don't have a full SRE platform.**

And it fits perfectly with:
- Your privilege boundary architecture
- Your audit logging infrastructure
- Your recovery journal system
- Your backup strategy

## Why This Roadmap

Production adoption requires:

1. **Trust** (don't fake success, show impact before you change things)
2. **Predictability** (what's tested, what's supported, what happens when it fails)
3. **Recoverability** (can I undo this? Is my data safe?)
4. **Boring operational experience** (no surprises at 3 AM)

The features are already good.

The trust infrastructure is what's missing.

Build that, and you have a product that hosting companies and hosters will choose not because it has more buttons, but because it makes them sleep better at night.
