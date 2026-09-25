# StePanel API Guide

This guide documents the API as an operator uses it. It focuses on request
flows, authentication, permissions, and safe mutation patterns. The
[OpenAPI contract](openapi.yaml) remains the machine-readable reference for
schemas and complete response definitions.

Set the panel address before running the examples:

```sh
export PANEL=https://panel.example.com
```

## Authentication

### Browser session

The web login is a form POST to `/login`. It sets two cookies:

- `stepanel_session`: HttpOnly authenticated session cookie.
- `stepanel_csrf`: CSRF value used for browser mutations.

Production login requires the administrator or customer username, password,
and TOTP code. Recovery codes can be supplied in the TOTP field when a
customer is using a one-time recovery code.

```sh
curl -fsS -c cookies.txt "$PANEL/login" >/dev/null
curl -fsS -i -L -b cookies.txt -c cookies.txt \
  -d 'username=admin' \
  --data-urlencode 'password=use-a-password-manager' \
  -d 'totp=123456' \
  "$PANEL/login"
```

Read the CSRF cookie before a mutating request:

```sh
csrf=$(awk '$6 == "stepanel_csrf" {print $7}' cookies.txt)
```

Send it in `X-CSRF-Token`. Multipart requests must use the header; they must
not put the token in the multipart body.

```sh
curl -fsS -b cookies.txt \
  -H "X-CSRF-Token: $csrf" \
  "$PANEL/api/sites/overview"
```

End the browser session with:

```sh
curl -fsS -b cookies.txt -X POST \
  -H "X-CSRF-Token: $csrf" \
  "$PANEL/logout"
```

### API tokens

Use a bearer token for automation:

```sh
export STEPANEL_TOKEN='returned-once-when-the-token-was-created'
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" \
  "$PANEL/api/jobs"
```

Customer tokens are limited to the owning account's assigned sites and jobs.
Administrator tokens are explicitly scoped as `admin:read` or
`admin:operate`. Read tokens cannot perform mutations. Token creation and
revocation require the administrator or customer browser session and the
CSRF token; the secret is returned only when created.

Manage customer tokens with `GET`/`POST /api/account/tokens` and
`DELETE /api/account/tokens/{id}`. Administrators manage scoped tokens with
`GET`/`POST /api/admin/tokens` and `DELETE /api/admin/tokens/{id}`.

## Response and safety rules

- Every response includes `X-Request-ID`; retain it when opening an incident.
- `401` means authentication is missing or expired. `403` means the identity
  is authenticated but lacks the required role, scope, site assignment, or
  CSRF proof.
- `409` means the requested state conflicts with a current operation or
  durable lifecycle state. Inspect the resource or job before retrying.
- `422` means validation or an explicit confirmation value failed.
- Mutations that perform asynchronous work return `202` with a `job_id` and
  `status_url`. Do not submit the same destructive request repeatedly.
- Use `Idempotency-Key` on cPanel restore requests. Reusing the same valid key
  returns the original restore job instead of starting a second restore.
- Destructive operations require exact confirmation strings documented by the
  endpoint and are audited before execution.

## Health and observability

These endpoints do not require authentication:

```sh
curl -i "$PANEL/livez"                 # process liveness
curl -fsS "$PANEL/readyz" | jq         # safe to receive new work
curl -fsS "$PANEL/api/health" | jq     # process health
curl -fsS "$PANEL/metrics"              # Prometheus text format
```

`/readyz` returns `503` only when the control plane cannot safely serve
requests—for example, while startup recovery is running, persistent storage
is unavailable, the SQLite integrity check fails, or session persistence is
unavailable. Dead letters, capacity, backup, reconciliation, and audit-outbox
backlog are reported separately by the authenticated operational endpoint:

```sh
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" \
  "$PANEL/api/health/operational" | jq
```

That separation keeps the administrative control plane reachable when an
operator needs to inspect or requeue a failed background job.

Administrator diagnostics and security posture:

```sh
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" "$PANEL/api/doctor" | jq
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" "$PANEL/api/security/center" | jq
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" "$PANEL/api/security/audit" | jq
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" \
  "$PANEL/api/audit/events?limit=100" | jq
```

## Jobs

List and poll durable work:

```sh
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" "$PANEL/api/jobs" | jq
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" \
  "$PANEL/api/jobs/$JOB_ID" | jq
```

Request cancellation with `POST /api/jobs/{id}`. Cancellation is durable, but
the worker may finish its current safe checkpoint first:

```sh
curl -fsS -X POST -H "Authorization: Bearer $STEPANEL_TOKEN" \
  "$PANEL/api/jobs/$JOB_ID" | jq
```

Administrators may requeue a dead-letter job only after investigating the
failure and repairing the underlying condition:

```sh
curl -fsS -X POST \
  -H "Authorization: Bearer $STEPANEL_TOKEN" \
  -H "X-CSRF-Token: $csrf" \
  "$PANEL/api/jobs/$JOB_ID/retry" | jq
```

## Site workflow

Inspect assigned site workspaces:

```sh
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" \
  "$PANEL/api/sites/overview" | jq
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" \
  "$PANEL/api/sites/overview/$SITE" | jq
```

Common site resources:

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `GET` | `/api/sites` | Administrator site route inventory |
| `GET` | `/api/sites/usage/{site}` | Bounded regular-file usage |
| `GET` | `/api/sites/logs/{site}` | Allowlisted, bounded site logs |
| `GET`/`PUT`/`DELETE` | `/api/sites/environment/{site}` | Encrypted site environment metadata |
| `GET`/`PUT` | `/api/sites/php/{site}` | PHP-FPM runtime profile |
| `GET`/`PUT` | `/api/sites/resources/{site}` | CPU, memory, I/O, task, and PHP-worker limits |
| `GET`/`PUT`/`DELETE` | `/api/sites/access/{site}` | SSH/SFTP policy and public keys |
| `GET`/`PUT`/`DELETE` | `/api/sites/redis/{site}` | Logical Redis/Valkey allocation |
| `GET`/`PUT`/`DELETE` | `/api/tasks/{site}/{name}` | Hardened systemd scheduled task |
| `GET`/`PUT`/`POST`/`DELETE` | `/api/workers/{site}/{name}` | Managed site worker |

Customer route activation requires a DNS ownership proof:

```sh
curl -fsS -X POST -H "Authorization: Bearer $STEPANEL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"site":"example","domain":"www.example.com"}' \
  "$PANEL/api/sites/domains/claim" | jq

# Publish the returned value as TXT at _stepanel.www.example.com, then:
curl -fsS -X POST -H "Authorization: Bearer $STEPANEL_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"site":"example","domain":"www.example.com"}' \
  "$PANEL/api/sites/domains/verify" | jq
```

The TXT record is revalidated when the customer activates the route. This is
panel ownership proof, not registrar, DNS-zone, DNSSEC, or ACME lifecycle
management. Administrators have an explicit operator bypass where documented.

## Backups and restores

List backups and queue a verified backup:

```sh
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" "$PANEL/api/backups" | jq
curl -fsS -X POST -H "Authorization: Bearer $STEPANEL_TOKEN" \
  -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' \
  -d '{"site":"example","include_databases":true}' \
  "$PANEL/api/backups" | jq
```

Verify an archive before restoring it:

```sh
curl -fsS -X POST -H "Authorization: Bearer $STEPANEL_TOKEN" \
  -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' \
  -d '{"backup":"/var/lib/ste-panel/backups/example.tar.zst"}' \
  "$PANEL/api/backups/verify" | jq
```

Administrator-only restore operations include:

- `POST /api/backups/restore-files`
- `POST /api/backups/restore-database`
- `POST /api/backups/restore-offsite-files`
- `POST /api/backups/restore-offsite-database`
- `POST /api/backups/restore-to-staging`
- `POST /api/backups/restore-offsite-to-staging`

Restore-to-staging creates an isolated, no-index site. It does not promote a
staging site to production and does not provide transactional live snapshot
cloning.

## Migration and delivery

Inspect a cPanel archive without restoring it:

```sh
curl -fsS -X POST -b cookies.txt -H "X-CSRF-Token: $csrf" \
  -F 'backup=@account.tar.gz' "$PANEL/api/cpmove/inspect" | jq
```

Queue a cPanel restore. The idempotency key makes client retries safe:

```sh
curl -fsS -X POST -b cookies.txt -H "X-CSRF-Token: $csrf" \
  -H 'Idempotency-Key: restore-example-20260907' \
  -F 'backup=@account.tar.gz' \
  -F 'username=example' -F 'confirm=IMPORT' \
  -F 'restore_databases=true' "$PANEL/api/cpmove/import" | jq
```

Other delivery endpoints:

| Method | Endpoint | Purpose |
| --- | --- | --- |
| `POST` | `/api/sites/git-deploy` | Checkout and atomically activate a validated release |
| `POST` | `/api/sites/git-rollback` | Administrator rollback to the retained release |
| `POST` | `/api/sites/git-webhook` | Deploy a signed Git webhook payload |
| `POST` | `/api/runner/build` | Run a bounded immutable-image build |
| `GET` | `/api/deployments` | Read durable build and activation records |
| `POST` | `/api/deployments/run` | Run the preview build-to-activation pipeline |
| `POST` | `/api/staging` | Create a recovery-journaled staging site |
| `POST` | `/api/certificates/issue` | Queue a Let’s Encrypt request |
| `GET` | `/api/wpress/preflight` | Check WordPress restore dependencies |
| `POST` | `/api/wpress/import` | Queue an All-in-One WP Migration restore |

See the dedicated [Git deployment](GIT_DEPLOYMENTS.md), [developer
workflows](DEVELOPER_WORKFLOWS.md), [cPanel import](CPMOVE_IMPORTS.md), and
[WordPress import](WPRESS_IMPORTS.md) guides for request bodies and provider
constraints.

## Databases

Database access is always scoped to the authenticated site/account boundary:

```sh
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" "$PANEL/api/databases" | jq
curl -fsS -H "Authorization: Bearer $STEPANEL_TOKEN" \
  "$PANEL/api/databases/example_db" | jq
```

Customer database operations use `POST /api/databases`,
`PATCH /api/databases/{name}/credentials`, and
`DELETE /api/databases/{name}`. Deletion creates a safety dump first and
refuses to continue if that backup fails. Passwords are never returned by the
API.

Administrator-only database operations include:

- `GET /api/database`: selected engine and administration UI status.
- `GET /api/database/diagnostics`: bounded health counters.
- `GET /api/database/settings`: read-only effective settings allowlist.
- `GET /api/database/sessions`: active session metadata without SQL text.
- `DELETE /api/database/sessions/{id}`: exact-confirmation session termination.

## Administrator operations

The following APIs require administrator identity or the corresponding
administrator token scope:

`/api/accounts`, `/api/services`, `/api/cloud`, `/api/cloud/action`,
`/api/cloud/dns`, `/api/cloud/loadbalancer`, `/api/cloud/snapshots`, `/api/ssh`,
`/api/ssh/action`, `/api/capabilities`, `/api/apps`, `/api/proxy`,
`/api/ftp`, `/api/reconcile/resources`, `/api/reconcile/tasks`,
`/api/security/scan`, `/api/security/center`, `/api/security/audit`,
`/api/audit/events`, `/api/doctor`, `/api/cpmove/*`, and the administrator
restore, termination, rollback, and migration endpoints.

Customer accounts are managed with `GET`/`POST /api/accounts`,
`PATCH`/`DELETE /api/accounts/{username}`, and the administrator recovery
endpoints under `/api/accounts/{username}`. Account assignment and resource
changes are durable and may return pending state when host enforcement needs
reconciliation.

## Machine-readable contract

Use [openapi.yaml](openapi.yaml) with Swagger UI, Redoc, `openapi-generator`,
or another API tool. It defines request schemas, response codes, security
schemes, multipart uploads, and the complete endpoint inventory. Keep this
guide and the YAML synchronized when adding or changing an endpoint.
