# ADR-0008: Reversible site suspension

- Status: Proposed
- Date: 2026-11-21

## Context

StePanel can terminate a site but not take it offline reversibly. An
operator who needs to stop a site for non-payment, abuse, or a compromise has
only two options: delete the site, or edit host configuration by hand. Hand
edits are then undone by the next route reconcile, PHP change, or app
deploy. `site.lifecycle.suspend` and `site.lifecycle.resume` are reported as
unsupported, and suspend/resume is a P1 item in the 1.0 gates.

Account suspension exists already, but it does something else. It blocks a
hosting account's panel logins and queued operations, and it never stops a
site from being served.

## Decision

**Site suspension** is a reversible, durable, administrator-only lifecycle
operation. It stops a site from serving or running code and keeps all of its
content, databases, and configuration. **Account suspension** keeps its
current meaning (panel access). Neither one implies the other.

### What suspension does

| Area | While suspended | On resume |
| --- | --- | --- |
| Web traffic | Every route for the site serves a static, generic `503 Service Unavailable` page with `Retry-After`; no PHP runs. TLS keeps working. The reason is never shown publicly. | The site's normal routes are re-applied from desired state. |
| PHP-FPM | The site's pool is disabled. A root-owned marker in the site's PHP state stops later pool writes (runtime or worker changes) from re-enabling it. | The marker is removed and the pool is rewritten from the current profile. |
| Managed apps, workers, scheduled tasks | Stopped and not started. | Restored from desired state. |
| SSH/SFTP/FTPS | Revoked for a site with its own `sp-` account. For a site placed in a hosting account (ADR-0007) the Unix user is shared with the account's other sites, so access is left as it is; that is account-level control. | Restored from the stored access policy (keys are kept). |
| Databases | Logins of the site's managed database accounts are locked and their sessions ended. Data is untouched. | Logins are unlocked. |
| Backups | Scheduled backups keep running, so a suspended site stays protected. | Unchanged. |
| Content | Never touched. | Unchanged. |

### Durability and recovery

- A `site_suspensions` row records the desired state (`suspended` or
  `active`), the reason, the actor, and timestamps. It is the single source
  of truth that every other path checks.
- `site.suspend` and `site.resume` are durable jobs serialized on the site
  mutation key (ADR-0006). Each runs its host steps through the existing
  lifecycle step journal, so a crash resumes at the next uncommitted step.
  Every step is idempotent.
- Suspension is a security operation (Class A in `AUDIT_CONTRACT.md`). It
  starts with `BeginSecurityAudit` and takes a mandatory reason. Resume
  restores access, so it is also Class A, not a revocation.
- After a restart, startup reconciliation re-applies host state for every
  site whose desired state is `suspended`, the same way pending routes are
  reconciled today.

### Enforcement

While a site is suspended, the panel refuses operations that would publish
or run code for it. That covers route and proxy apply, app and worker apply
or start, task apply or start, Git deploy and release activation, PHP
runtime and resource changes that would rewrite the pool, access-policy
changes, and file or database restores. Each refusal returns `409` and names
the suspension. Backups, backup verification, read-only views, termination,
and resume stay allowed. Route reconciliation applies the 503 route for a
suspended site, never its normal route.

### Who

Only administrators can suspend or resume a site, and they must give a
reason. Customers see that their site is suspended, but not the internal
reason, and cannot resume it.

### Scope

The 503 route is implemented for Apache and Caddy, the supported web
servers. On OpenLiteSpeed the capability reports suspension as unsupported
rather than half-applying it.

## Consequences

- Every mutation path for a site gains one suspension check. A shared helper
  keeps it in one place; a test enumerates the guarded operations so that a
  new path cannot skip it silently.
- The site helpers gain idempotent suspend/resume verbs (PHP pool marker,
  503 vhost, database login lock), so the privileged boundary stays narrow
  (ADR-0002).
- `SiteManager` does not gain suspend/resume methods: suspension changes
  host configuration and access, not the canonical site tree, which stays
  under Gate 1's filesystem authority.
- Capability reporting turns `site.lifecycle.suspend`/`resume` to available
  only where every required helper is present.
