# ADR-0008: Reversible site suspension

- Status: Accepted
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
| Web traffic | Every site route serves a static, generic `503 Service Unavailable` page with `Retry-After`; no PHP runs. TLS keeps working. The reason is never shown publicly. On Caddy the route file is rewritten. On Apache every managed vhost includes a per-site suspension file, which certbot copies into the TLS vhost; suspend refuses if an enabled vhost for the site lacks that include. ACME challenges still pass on Apache so renewals keep working. Proxy routes to a stopped application return the proxy's upstream error. | Caddy re-applies the normal route; Apache removes the suspension file. |
| PHP-FPM | The site's pool is disabled. A root-owned marker in the site's PHP state stops later pool writes (runtime or worker changes) from re-enabling it. | The marker is removed and the pool is rewritten from the current profile. |
| Managed apps, workers, scheduled tasks | `stepanel-appctl suspend` records which of the site's units are enabled, then stops and disables all of them, so they stay down across reboots. While the record exists the helper refuses to enable or start any of them. | Exactly the recorded units are re-enabled and started. |
| SSH/SFTP/FTPS | For a site with its own `sp-` account, an empty access policy is applied (the stored policy and its keys are kept) and the account password, which holds the FTPS password, is locked with `usermod --lock`. For a site placed in a hosting account (ADR-0007) the Unix user is shared with the account's other sites, so access is left as it is; that is account-level control. | The password is unlocked and the stored access policy is re-applied. |
| Databases | Logins of the site's managed database accounts are locked and their sessions ended. Data is untouched. | Logins are unlocked. |
| Backups | Scheduled backups keep running, so a suspended site stays protected. | Unchanged. |
| Content | Never touched. | Unchanged. |

### Durability and recovery

- A `site_suspensions` row records the desired state (`suspended` or
  `active`), the reason, the actor, and timestamps. It is the single source
  of truth that every other path checks.
- `site.suspend` and `site.resume` are durable jobs serialized on the site
  mutation key (ADR-0006). Every host step is idempotent, so a retry runs
  the whole sequence again; no step journal is needed. A job whose request
  was superseded (for example a suspend job that runs after a resume was
  requested) changes nothing.
- Suspension is a security operation (Class A in `AUDIT_CONTRACT.md`). The
  request starts with `BeginSecurityAudit` and takes a mandatory reason;
  the job records `site.suspended` or `site.resumed` once the host matches.
  Resume restores access, so it is also Class A, not a revocation.
- After a restart, startup reconciliation re-applies host state for every
  site whose desired state is `suspended`, the same way pending routes are
  reconciled today.

### Enforcement

While a site has a suspension record (suspending, suspended, or resuming),
every mutation that takes the site's mutation lock is refused once the lock
is held. Checking under the lock means a suspension recorded before the
lock was granted is always seen, and the suspend job itself runs under the
same lock. This covers route, app, worker, task, PHP, access, deploy, and
restore paths without a separate check in each. Typed site operations are
also refused with `409` before they are queued. Only suspend/resume,
termination, and backup-schedule changes are exempt; backups, which do not
take the site lock, keep running. Startup host reconcilers skip suspended
sites, and a dedicated reconciler runs first to re-apply every suspended
site's host state. The privileged helpers add a second barrier: the PHP
pool marker and the appctl unit record refuse re-enabling while they
exist.

### Who

Only administrators can suspend or resume a site, and they must give a
reason. Customers see that their site is suspended, but not the internal
reason, and cannot resume it.

### Scope

The 503 route is implemented for Apache and Caddy, the supported web
servers. On OpenLiteSpeed the capability reports suspension as unsupported
rather than half-applying it.

## Consequences

- The suspension check lives in the site lock, so a new mutation path that
  takes the lock is guarded automatically. A mutation that does not take the
  lock (for example file edits through the file manager) is not blocked; it
  changes content, which a suspended site does not serve or run.
- The site helpers gain idempotent suspend/resume verbs (PHP pool marker,
  503 vhost, database login lock), so the privileged boundary stays narrow
  (ADR-0002).
- `SiteManager` does not gain suspend/resume methods: suspension changes
  host configuration and access, not the canonical site tree, which stays
  under Gate 1's filesystem authority.
- Capability reporting turns `site.lifecycle.suspend`/`resume` to available
  only where every required helper is present.
