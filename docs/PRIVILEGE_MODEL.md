# How StePanel requests root

**Verified against `main` on 2026-10-30.** This is the single answer to "how
can unprivileged StePanel make a privileged change?" Details live in
[ROOT_BROKER_INTEGRATION.md](ROOT_BROKER_INTEGRATION.md).

## The answer

The panel and worker run as the unprivileged `stepanel` user with no sudo
grant. On a production install, every privileged change is one JSON request
to the root broker (`stepanel-root`) over the Unix socket
`/run/stepanel-root-broker.sock`. The broker authenticates the caller,
validates the request, admits it under a resource lock and a deadline, and
runs one root-owned helper script with an argument vector. Nothing else on
the host accepts privileged work from the panel; outside production the
same code can run helpers directly, which `STEPANEL_ENV=production` forbids.

```
panel / worker (user stepanel)
  │  audit event written first (fail-closed), then
  ▼
Unix socket 0660 root:stepanel ── peer credentials checked (SO_PEERCRED gid)
  ▼
stepanel-root: decode (96 MiB cap, 30 s read deadline)
  → validate (typed rules or per-action helper schema)
  → admit (resource lock + concurrency bound; health bypasses)
  → run helper with a deadline from the action's timeout class
  ▼
deploy/integrations/stepanel-{site,app,db,vhost,proxy,git,runner}ctl, stepanel-certbot
```

## Each request carries

| Property | How it is enforced |
|----------|--------------------|
| **Caller identity** | The socket is mode `0660` and group `stepanel`; the broker rejects any peer whose group is not `stepanel`. |
| **Authorization** | The panel resolves the authenticated principal, token scopes, and tenant ownership. For customer file operations, the broker independently requires a tenant ID and checks `tenant_sites` for an exact `(site, tenant)` owner match before it resolves placement or runs `stepanel-filectl`. The Unix-socket peer alone never authorizes a file path. |
| **Narrow type** | Typed requests (`site`, `app`, `db`, `task`, `git`, `certificate`, `vhost`, `proxy`) have Go validation per field. Generic `helper` requests must match an allow-listed action with exact arity and a semantic type per argument (`helper_schema.go`). |
| **Resource lock** | Per site, database, account, or certificate domain; host-wide helpers (`proxyctl`, `vhostctl` route deletes, `dbctl` engine reads) use a host key; anything unscoped runs exclusively (`scheduler.go`). Host account changes share one lock. |
| **Deadline** | Every action has an explicit timeout class (`timeouts.go`): config 30 s, service lifecycle 60 s, dependency builds 15 min, container builds and clones 30 min, database mutations 60 min, and size-proportional work (dumps, restores, recursive ownership, site deletion) 120 min. The class is never shorter than the panel's own timeout for that action. |
| **Audit event** | The panel writes a fail-closed audit event before every mutating request (`Auth.Require`, and webhook deploys explicitly). |
| **Recovery** | Site creation, database restore, vhost changes, release activation, file restores, and termination keep journals that startup recovery replays or rolls back. |

## Tenant code runs as the tenant

Commands that load a site's own code run as that site's isolated account, never
as root or `stepanel`. WordPress is the main case: wp-cli executes
`wp-config.php` and WordPress core from the site tree. The typed `wordpress`
request names one operation (core download and install, database integrity
check, maintenance mode, option update, search-replace, `wp-config.php`
settings, and updates). The broker builds the wp-cli arguments
from fixed patterns, so uploaded archive metadata cannot become wp-cli options
such as `--exec` or `--require`. `stepanel-appctl wp` then runs the fixed
`/usr/local/bin/wp` against the site's `public` directory through
`runuser -u <site user>` with a cleared environment. The database password for
`wp-config.php` travels on stdin, never in argv. Composer and Node tooling use
the same `runuser` path.

## Schema-validated helper operations still in use

The broker uses dedicated typed requests for core privileged operations and a
schema-validated `helper` request for the following narrower operations. They
are tightly validated, but their contract lives in the argument schema rather
than a Go type:

| Helper | Generic actions still in use |
|--------|------------------------------|
| `appctl` | Python and worker lifecycle, Composer and Node tooling, environment and resource application, resource status |
| `sitectl` | SSH access, PHP workers, quotas, PHP runtime |
| `vhostctl`, `proxyctl` | Route and proxy apply/delete, `.htaccess` import |
| `dbctl` | Per-site listing, reconcile, drop, diagnostics, sessions, settings, termination (dumps and restores use the typed `db` request and stream through files) |
| `gitctl`, `runnerctl` | Repository clone, container build |

These operations remain candidates for future typed request contracts. Any
change to the accepted helper action inventory must update the broker schema,
this document, and its contract tests together.

The broker is a single privileged process on one host. Its customer file
manager requires both the panel's sealed site capability and an independent
broker-side match against durable site ownership before invoking the helper.
This catches a missing or inconsistent HTTP-layer ownership check and keeps
the privileged filesystem operation bound to the site's recorded tenant and
Unix account. Other broker operation families retain their operation-specific
validation and fencing contracts; this is not a universal broker-side
revalidation of every API token scope.

The panel service remains a trusted computing-base component: a fully
compromised `stepanel` process can submit requests directly over its permitted
socket and can state the durable owner as the tenant claim. This design does
not claim to contain a compromised panel process or mutually hostile tenants
sharing a kernel. The broker does not accept an HTTP session or API token as
authority for an arbitrary filesystem path. See
[SECURITY.md](../SECURITY.md#what-tenant-isolation-does-and-does-not-mean).
