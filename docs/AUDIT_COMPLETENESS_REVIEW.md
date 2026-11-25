# Audit completeness review — 2026-11-25

This review answers the production gate's open item "a documented
completeness/reconciliation review for every destructive operation" for the
HTTP API on `main` after `bb72e1a3`. It complements the
[audit contract](./AUDIT_CONTRACT.md), which classifies each operation.

## Method

1. Every route in `http_routes.go` that accepts `POST`, `PATCH`, `PUT` or
   `DELETE` was listed (80 handlers, including `site.operation` jobs).
2. A Go AST walk followed each handler through same-package calls, four
   levels deep, and listed the reachable `BeginSecurityAudit`,
   `RevocationAudit` and `TelemetryAudit` calls and their literal event
   names.
3. Every handler with no reachable audit call, and every destructive
   handler reaching only telemetry, was read by hand, following queued jobs
   into their worker handlers.

The walk keys functions by name, so a method that shares a name with
another (for example `App.adminAPITokens` delegating to
`Auth.adminAPITokens`) is a false negative that step 3 resolved.

## Findings and changes

| Handler | Finding | Resolution |
| --- | --- | --- |
| `cloudAction`, `cloudDNS`, `cloudLoadBalancer` (worker `cloud.action`) | Provider mutations (instance stop/reboot/snapshot, DNS record create/update/delete, load-balancer node add/remove) were recorded only as best-effort telemetry after the provider call, and were unclassified | Now class A: the worker records the intent before calling the provider and refuses when it cannot (`TestCloudMutationAuditsIntentBeforeProviderCall`). SSH service actions stay class B |
| `archiveImportStart` (worker `archive.import`) | Created a site, its placement and database with telemetry only, attributed to the literal `admin`; the job owner was also the literal `admin`, so on a host whose `STEPANEL_ADMIN_USERNAME` differs every archive import failed authorization | Now class A with the requester from `Auth.AuditActor`; jobs are owned by the configured administrator (`TestArchiveImportUsesConfiguredAdministratorAndAuditsIntent`). The audit detail names the source host, never the URL's path or query |
| `backupSchedules` | Schedule removal is telemetry only | Classified class B: a schedule is configuration; backups themselves are unchanged |
| `issueCertificate` | Telemetry only | Classified class B (`certificate.issued`) |

## Handlers confirmed without change

| Handler | Audit |
| --- | --- |
| `adminAPITokens`, `apiTokens` | Class A creation, revocation (`auth.*api_token.*`) |
| `accounts`, `accountMembers`, `accountSuspend`, `accountUnsuspend`, `customerSessionsRevoke` | Class A intent-first or revocation (`hosting.account.*`, `tenant.member.*`, `account.*`) |
| `siteManage`, `siteTermination`, `siteSuspend` | Class A (`site.deleted`; journaled `site.termination.*`; `site.suspend*`) |
| `backupRestore*HTTP`, `wpressImport`, `importBackup` | Queued; the worker records class A `backup.<mode>`, `wordpress.restore`, `cpmove.restore` |
| `databaseResource`, `databaseSessionTerminate` | Class A (`database.deleted`, `database.credentials_rotated`); revocation (`database.session_terminated`) |
| `proxyManage`, `siteEnvironment`, `siteRedis`, `siteGitKey`, `webhookConfig`, `gitRollback` | Class A intent-first or revocation |
| `workers`, `tasks` | Class B, as classified (service and task control) |
| `securityAuditReconciliation` | Records its own reconciliation through the audit outbox |
| `customerMFASetup` | Generates a pending secret only; enrollment (`customerMFA`) is class A |
| `inspect`, `inspectArchive`, `migrationDoctor`, `proxyTest`, `cpmovePlan` | No persistent mutation (analysis, previews, plans) |
| `sites` (`POST /api/sites`) | Queued; the worker records class A `site.created` |

## Not covered

This review covers HTTP routes. Startup reconciliation, scheduled jobs and
offline CLI commands are covered by their own entries in the contract
(recovery records, `security.key_rotation.resealed`,
`webhook.secrets.disable-unreadable`, the identity-repair journal). The
review is a point-in-time reading of the code; the contract's rule that new
operations are classified in the same change keeps it current.
