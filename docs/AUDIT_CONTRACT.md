# Audit Contract

Every audit call in StePanel belongs to exactly one of two classes. The class
is visible at the call site because each class has its own API
(`audit_contract.go`); the older `recordAudit`, `AuditAs`, `MustAudit` and
`ShouldAudit` functions no longer exist.

## Class A: security ledger events

These are high-impact operations: they grant or change access, expose or
change credentials, destroy data, or change the code or configuration a site
serves.

```
intent  := BeginSecurityAudit(log, actor, action, target, detail)  // "<action>.initiated"
            ↳ error → refuse the request (HTTP 503); nothing has changed
mutation
intent.Completed(detail)   // "<action>"
intent.Failed(detail)      // "<action>.failed"
```

- **No mutation without a durable intent.** If the intent cannot be recorded,
  the operation is refused and nothing changes.
- **Every intent gets an outcome.** Handlers with several exit paths
  `defer intent.Finish(&outcome, failure)` and set `outcome` at the point
  where the change takes effect (for example, right after a release is
  activated), so a later error cannot mislabel a live change as failed.
- **Outcome failures never misreport the mutation.** Once the intent is
  durable, failing to record the outcome is logged as `[CRITICAL]` and the
  outbox retries. The intent alone shows that the operation was attempted.
- `SecurityAuditRequired` records a single Class A event without an outcome.
  It is used for pre-mutation gates: the per-request `http.request` record,
  `auth.login.succeeded` before a session is issued, startup-recovery records,
  and the termination sequence in `internal/sitelifecycle`.

### Revocations

Operations that only **remove** access are Class A ledger events, but they
are never blocked by the ledger. An audit outage, or an attacker who fills the
disk, must not be able to stop an administrator from cutting off a
compromised account. Revocations run first and are then recorded with
`RevocationAudit`, which records durably and logs `[CRITICAL]` if it cannot.
Restoring access (unsuspending) is a grant and is recorded intent-first.

### What "durably recorded" means

The event is persisted in the SQLite audit outbox. Publishing it to the
signed, hash-chained audit file is retried by the outbox and is not part of
the durability decision: an event that is in the outbox but not yet
published is still recorded. (Treating a publication failure as "not
recorded" would refuse the operation while the outbox later published an
intent for something that never ran.) Processes without an outbox, such as
CLI subcommands and tests, append to the signed log directly.

## Class B: telemetry

`TelemetryAudit` records informational events: login failures, reconciliation
results, background job progress, scans, service restarts. It is best effort
and never affects the operation; failures are logged.

## Classification

| Operation | Class | Events |
|---|---|---|
| Customer and administrator API token creation | A, intent-first | `auth.api_token.created`, `auth.admin_api_token.created` |
| API token revocation, legacy-token hard cutoff | A, revocation | `auth.api_token.revoked`, `auth.admin_api_token.revoked`, `auth.api_token.legacy_revoked` |
| Hosting account creation, plan or site assignment | A, intent-first | `hosting.account.created`, `hosting.account.updated` |
| Account suspension (admin, PATCH, automatic) | A, revocation | `account.suspended`, `account.suspension.temporary`, `account.suspended.auto`, `hosting.account.suspended` |
| Account unsuspension | A, intent-first | `account.unsuspended`, `hosting.account.unsuspended` |
| Credential recovery, MFA reset, recovery codes, password change, MFA enrollment | A, intent-first | `hosting.account.*` |
| Session revocation (admin or self-service) | A, revocation | `hosting.account.sessions-revoked*` |
| Login removal, tenant member creation, removal, role change | A, intent-first | `hosting.account.login-removed`, `tenant.member.*` |
| Tenant member suspension only | A, revocation | `tenant.member.updated` |
| Git deployment (session or webhook), rollback, release pipeline | A, intent-first | `site.git-deployed`, `site.git-rolled-back`, `site.release.pipeline` |
| Webhook configuration update | A, intent-first | `webhook.config.updated` |
| Webhook disable | A, revocation | `webhook.config.disabled` |
| Site route publication and removal | A, intent-first | `site.deployed`, `site.deleted` |
| Site termination | A, journaled sequence | `site.termination.initiated`, `site.terminated` |
| App, Python and proxy deployments, proxy removal | A, intent-first | `app.deployed`, `python.deployed`, `proxy.deployed`, `proxy.deleted` |
| `.htaccess` migration into the web server | A, intent-first | `site.htaccess-migrated` |
| Database provisioning, credential rotation, deletion | A, intent-first | `database.provisioned`, `database.credentials_rotated`, `database.deleted` |
| Database session termination | A, revocation | `database.session_terminated` |
| Backup, cpMove and WordPress restores | A, intent-first | `backup.<mode>`, `cpmove.restore`, `wordpress.restore` |
| SSH access policy, SSH key addition, Git deploy key creation | A, intent-first | `site.ssh-access.updated`, `site.ssh-key.added`, `site.git-deploy-key.created` |
| SSH key removal, Git deploy key deletion | A, revocation | `site.ssh-key.removed`, `site.git-deploy-key.deleted` |
| Environment variable changes and removal | A, intent-first | `site.environment.updated`, `site.environment.deleted` |
| Redis allocation removal | A, intent-first | `site.redis.deleted` |
| Backups, verification, rehearsals, restore-to-staging, retention | B | `site.backup.*`, `backup.verify`, `backup.rehearsal.*`, ... |
| Service start/stop/restart, Node version, PHP, resources, tasks, workers | B | `app.<action>`, `python.<action>`, `site.php.updated`, `task.*`, ... |
| Login failures, logout, throttling, tenant access denials, scans | B | `auth.login.failed`, `*.throttled`, `tenant.access_denied`, ... |

When adding an operation, classify it here in the same change.

## Actor attribution

The actor is `Auth.AuditActor(r)`: the authenticated requester, or
`auth-disabled` when authentication is turned off (non-production only).
Unattended paths use `webhook`, `scheduler` or `system`. With authentication
enabled and no identity, the actor is empty and Class A operations are
refused rather than recorded unattributed. The configured panel
administrator name (`Auth.Username`) is never used as an actor; a test
enforces this.
