# Documentation Structure & Guide

This guide explains how StePanel documentation is organized and helps maintainers know where to add new documentation.

## 4 Canonical Sources

StePanel's documentation is organized around **4 authoritative documents**:

### 1. [README.md](../README.md)
**Purpose:** Project overview, quick start, basic architecture  
**Audience:** Anyone discovering StePanel for the first time  
**Contains:**
- What is StePanel and why use it
- Getting started (local development, container, release installation)
- Quick workflows (cpmove, WordPress, git deploy)
- Links to detailed guides
- Current production readiness status

**Update when:**
- Project goals change
- Getting started experience changes
- New quick workflows are added

### 2. [docs/SECURITY.md](./SECURITY.md)
**Purpose:** Threat model, security boundaries, known limitations  
**Audience:** Security-conscious operators, auditors  
**Contains:**
- What threats are defended against
- What's out of scope
- Known limitations and their mitigations
- Version history of security posture
- Related to: THREAT_MODEL.md (detailed version history)

**Update when:**
- Security boundaries change
- New vulnerabilities are discovered/fixed
- Threat model evolves

### 3. [docs/FEATURES.md](./FEATURES.md)
**Purpose:** Authoritative capability matrix and feature status  
**Audience:** Operators evaluating StePanel, feature developers  
**Contains:**
- "Available now" — shipped, tested features
- "Partial or operator-only" — limited scope features
- "Not yet production-complete for shared hosting" — roadmap items
- Status vocabulary (Stable, Beta, Experimental, Planned)
- Explicit non-claims (what StePanel does NOT do)

**Update when:**
- Features ship or change status
- Beta features are removed or stabilized
- Capabilities shift between "Available" and "Planned"

### 4. [docs/ROADMAP.md](./ROADMAP.md)
**Purpose:** Medium-term direction and planned work  
**Audience:** Contributors, operators planning multi-year roadmaps  
**Contains:**
- Next quarter priorities
- v0.8+, v1.0 goals
- Major architectural changes planned
- Timeline expectations

**Update when:**
- Release cycle changes
- Major initiatives are planned
- Community feedback shifts priorities

---

## Supporting Documentation by Category

All other documentation falls into these categories:

### Operational Guides
**When to create:** Procedures for running/managing StePanel  
**Examples:** [docs/OPERATIONS.md](./OPERATIONS.md), [docs/INSTALLATION.md](./INSTALLATION.md), [docs/SECRETS.md](./SECRETS.md)  
**Links from:** README (under "Documentation" section)  
**Constraint:** Must not duplicate feature status from FEATURES.md; reference it instead

### Workflow Guides
**When to create:** Step-by-step procedures for specific tasks  
**Examples:** [docs/CPMOVE_IMPORTS.md](./CPMOVE_IMPORTS.md), [docs/GIT_DEPLOYMENTS.md](./GIT_DEPLOYMENTS.md), [docs/ARCHIVE_IMPORTER.md](./ARCHIVE_IMPORTER.md)  
**Links from:** README (under "Quick workflows" section)  
**Constraint:** Should start with "This is Phase X complete; Phase Y is planned" to avoid confusion about status

### API Documentation
**When to create:** API contracts, endpoint details, request/response examples  
**Examples:** [docs/API_GUIDE.md](./API_GUIDE.md), [docs/openapi.yaml](./openapi.yaml)  
**Links from:** README and FEATURES.md  
**Constraint:** Generated docs should auto-sync; hand-written docs must match code

### Architecture & Design
**When to create:** System design, component interaction, implementation decisions  
**Examples:** [docs/ARCHITECTURE.md](./ARCHITECTURE.md), [docs/adr/](./adr/) (Architecture Decision Records)  
**Links from:** README (under "Architecture at a glance" section)  
**Constraint:** ADRs are immutable records; don't edit them; write new ADRs for new decisions

### Case Studies & Evidence
**When to create:** Measured recovery results, audit findings, detailed analysis  
**Examples:** [docs/lab-results/2026-09-06-recovery-drills.md](./lab-results/2026-09-06-recovery-drills.md), [docs/THREAT_MODEL.md](./THREAT_MODEL.md)  
**Links from:** SECURITY.md and README  
**Constraint:** Date-stamp these; they're point-in-time evidence

### Troubleshooting & FAQ
**When to create:** Answers to common questions, diagnostic procedures  
**Examples:** [docs/CUSTOMER_WORKFLOWS.md](./CUSTOMER_WORKFLOWS.md) (includes Q&A section)  
**Links from:** Relevant workflow guides  
**Constraint:** Keep short; point to full guides for complex topics

### Integration Guides
**When to create:** Third-party integrations, provider-specific setup  
**Examples:** [docs/INTEGRATIONS.md](./INTEGRATIONS.md), [docs/CONTAINER_REGISTRY_ALLOWLIST.md](./CONTAINER_REGISTRY_ALLOWLIST.md)  
**Links from:** README and OPERATIONS.md  
**Constraint:** Must document supported versions and tested configurations

---

## Documentation Checklist

When adding or updating documentation:

### For Feature Documentation
- [ ] Feature listed in FEATURES.md with correct status?
- [ ] Status matches code implementation (not aspirational)?
- [ ] Phase number matches CHANGELOG and code comments?
- [ ] Next steps explained (what comes in Phase X+1)?

### For Operational Guides
- [ ] References canonical docs instead of duplicating status?
- [ ] Includes version-specific warnings (if applicable)?
- [ ] Links to related configuration sections?
- [ ] Includes troubleshooting section?

### For All New Documentation
- [ ] Added to README's documentation section?
- [ ] Links to related documents?
- [ ] No contradictions with FEATURES.md?
- [ ] Uses consistent terminology?
- [ ] Markdown format is consistent (headings, code blocks)?

---

## Links & Relationships

```
README.md (entry point)
├── FEATURES.md (authoritative capability matrix)
├── SECURITY.md (threat model)
├── ROADMAP.md (future direction)
├── docs/INSTALLATION.md → OPERATIONS.md
├── docs/CPMOVE_IMPORTS.md → FEATURES.md
├── docs/ARCHIVE_IMPORTER.md → FEATURES.md
├── docs/API_GUIDE.md → docs/openapi.yaml
├── docs/ARCHITECTURE.md → docs/adr/
└── docs/INTEGRATIONS.md → provider-specific guides
```

---

## Examples of Good Documentation Updates

### Adding a new feature
1. Implement feature with Tier 1 code quality (CLAUDE.md standards)
2. Add test coverage
3. Update FEATURES.md under "Available now" or "Planned"
4. Create workflow guide if it's user-facing (WORKFLOW_NAME.md)
5. Update README links to the new guide
6. Add CHANGELOG entry
7. Update ROADMAP.md to remove from "Planned" if applicable

### Fixing a claim-vs-reality gap
1. Identify the mismatch (e.g., "feature X claimed but not implemented")
2. Decide: fix implementation or fix documentation?
3. Update the authoritative source (FEATURES.md, SECURITY.md, etc.)
4. Update related workflow guides
5. Add CHANGELOG entry explaining the resolution
6. Update git commit message with context

### Consolidating scattered documentation
1. Identify the topic (e.g., "database operations")
2. Collect all scattered references
3. Create single authoritative guide (e.g., DATABASES.md)
4. Update README to link to it
5. Update related workflow guides to reference it
6. Mark old docs as "see DATABASES.md instead"

---

## When to Say "No" to New Documentation

Documentation sprawl makes StePanel harder to maintain. Say "no" to:

- **Duplicate explanations** — If FEATURES.md explains a capability, don't explain it again in a workflow guide; reference it
- **Outdated version docs** — Delete v0.6.0 guides; use git tags for historical versions
- **Aspirational docs** — Don't document Phase 2 features as if they're complete; use "Planned" in FEATURES.md
- **Internal notes** — Use git commit messages and ADRs, not scattered .md files
- **Vendor-specific guides** — If Linode-specific, ask operators to reference Linode docs; StePanel docs stay generic

---

## Maintenance Schedule

- **Monthly:** Review CHANGELOG for new docs needed
- **Before release:** Verify FEATURES.md, SECURITY.md, ROADMAP.md accuracy
- **Quarterly:** Search for claim-vs-reality gaps
- **Annually:** Archive old release docs (move to git tags, delete from main branch)

