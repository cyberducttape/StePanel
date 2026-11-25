# Independent security review package

This document prepares the independent security review that the
[production gates](./V1_PRODUCTION_GATES.md) require. It tells a reviewer
what StePanel promises, where the trust boundaries are, which code to read
first, and what automated analysis has already been done, so review time is
spent on attack paths rather than orientation. It is not a review and makes
no claim that one has happened.

**Prepared:** 2026-11-25 against `main` at `defd0db2`.

## What is promised

StePanel is an operator-managed, single-host PHP/WordPress panel. Its
security promise is limited to that scope:

- A customer account can read and change only its own sites, files,
  databases, backups and settings.
- Tenant-controlled code and data never run as the panel's `stepanel`
  account or as root (the [central invariant](./THREAT_MODEL.md#the-central-invariant)).
- Every privileged host change goes through a typed request to the root
  broker or a fixed helper; there is no generic command channel
  ([ADR-0002](./adr/0002-privileged-helper-boundaries.md)).
- High-impact operations leave a tamper-evident, durably recorded audit trail
  ([audit contract](./AUDIT_CONTRACT.md)).
- Secrets at rest are sealed and bound to their record; master keys are
  rotatable ([encryption keys](./ENCRYPTION_KEYS.md)).

Explicitly out of scope: hostile multi-tenant isolation beyond Unix users on
one kernel, high availability, and a compromised administrator (see
[residual risks](./THREAT_MODEL.md#residual-risks)).

## Where to look first

Ordered by the damage a flaw would do.

| Boundary | Entry points | Key code |
| --- | --- | --- |
| Panel → root broker | 97 typed request kinds over a Unix socket authorized by peer UID/GID | `cmd/stepanel-root/main.go` (`authorizePeerCredential`), `internal/rootbroker/broker.go`, `internal/rootbroker/validator.go` |
| Root helpers | 22 scripts run as root from the broker | `deploy/integrations/stepanel-*` (start with `stepanel-filectl`, `stepanel-sitectl`, `stepanel-userctl`, `stepanel-dbctl`, `stepanel-gitctl`) |
| Browser and API → panel | 138 routes; sessions, CSRF, scoped API tokens, tenant authorization | `http_routes.go`, `auth.go`, `tenancy.go`, `api_tokens.go`, `csrf_protection_test.go` |
| Untrusted archives and dumps | cPanel, WPress, tar/zip imports; SQL dumps loaded into shadow databases | `internal/archivesafe`, `internal/importer`, `cpmove.go`, `wpress.go`, `stepanel-dbctl restore-dump` |
| Tenant filesystem | File manager, restores, rollback copies through descriptor-rooted operations | `site_files.go`, `internal/sitefiles`, `stepanel-filectl` |
| Outbound requests | Archive URLs, webhooks; public destinations only, checked at connect time | `internal/safehttp` |
| Secrets and audit | Sealed secrets, key rotation, audit hash chain and checkpoints | `internal/secretbox`, `internal/audit`, `audit_contract.go`, `audit_outbox.go` |

Suggested attack hypotheses:

1. Make the broker or a helper act on another tenant's path (symlink races,
   `..`, bind views, placement records).
2. Get tenant code to run as `stepanel` (WP-CLI, Composer, Git hooks, build
   scripts, PHP-FPM pool configuration).
3. Cross tenants through the API: object IDs in jobs, backups, tokens,
   members, file paths, or a token whose scope or owner changed.
4. Escape archive extraction or SQL restore (definers, triggers, `LOAD DATA`,
   client commands, `psql` meta-commands).
5. Reach an internal address through a webhook or archive URL (DNS
   rebinding, redirects, IPv6 forms).
6. Make a Class A operation happen without its audit intent, or rewrite
   audit history.

## Automated analysis already done

| Tool | Result |
| --- | --- |
| `go vet`, `staticcheck@2026.1` | Clean (CI) |
| `govulncheck` | No vulnerabilities (CI and local, 2026-11-24) |
| CodeQL | No open alerts on recent `main` runs |
| `shellcheck -S warning` | Clean apart from variables set by sourced helpers |
| Fuzzing | `FuzzSafeUser`, backup-name and database-identifier fuzzers, plus the scheduled Security fuzzing workflow |
| `gosec v2.22.4` (2026-11-25) | 351 findings, triaged below |
| `semgrep` (`p/golang`, `p/gosec`, `p/secrets`, 2026-11-25) | 12 findings, all overlapping the gosec triage; no secrets |

Triage of the static analysis:

- **Fixed:** `markDelivered` named SQL columns from a channel string without
  the allowlist `recordFailure` had (internal callers only; `defd0db2`).
- **False positives, verified:** SQL built only from constant predicates,
  placeholder lists, schema migration column names or integers (`jobs.go`,
  `git_deploy.go`, `internal/sqliteutil`, `cmd/stepanel-root`); weak random
  used only for stream jitter (`main.go`); per-chunk backup nonces under a
  per-backup derived key (`internal/backup/encryption.go`); a bounded tar
  read during backup verification (`backups.go`); the HTTP server's
  `ReadHeaderTimeout`, set after the struct literal (`main.go`); cookie flags
  (`Secure` follows the transport; the CSRF cookie is readable by design for
  the double-submit check); SHA-1 only where TOTP (RFC 6238) requires it.
- **Expected by design:** variable file paths (`G304`) and subprocesses
  (`G204`) in a panel that manages files and runs fixed helpers; each path is
  validated by the callers listed above. Integer conversions (`G115`) were
  read for untrusted sizes; archive sizes are bounded before conversion and a
  negative upload remainder fails closed.
- **Covered elsewhere:** unchecked errors (`G104`) on durable mutations are
  enforced by `TestDurableMutationErrorsAreChecked`; the rest are
  best-effort cleanup.

## Evidence a reviewer can rerun

- Adversarial cross-tenant tests: `adversarial_test.go`,
  `tenant_isolation_test.go`, `privilege_escalation_test.go`,
  `deploy/lab/tenant-isolation-smoke.sh`.
- Root helper smokes on an installed host: `deploy/lab/filectl-smoke.sh`,
  `deploy/lab/dbctl-client-command-smoke.sh`, `deploy/lab/sitectl-acl-smoke.sh`.
- Installed-host drills: `deploy/lab/local-kvm-certification.sh` (boot,
  install, abrupt loss, ENOSPC, alerts, capacity).
- Audit chain verification: `stepanel verify-audit <log>`.

## Reporting

Report findings privately as described in [SECURITY.md](../SECURITY.md).
