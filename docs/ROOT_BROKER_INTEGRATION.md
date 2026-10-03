# Root Broker Integration Guide

**Status:** Native production installs use only the Unix-socket broker. A
missing production socket is a hard failure; stdin/subprocess transport is
retained only for explicitly non-production compatibility callers and tests.
**Last Updated:** 2026-10-02

This document explains how to integrate the typed Go root broker (`stepanel-root`) into the main StePanel application to replace shell script helpers.

## Architecture

```
┌─────────────────────────────┐
│  StePanel Application       │  (runs as unprivileged 'stepanel' user)
│  - Internal API handlers    │
│  - Site management logic    │
│  - Database orchestration   │
└──────────────┬──────────────┘
               │
        ┌──────▼──────┐
        │   Client    │  (internal/rootbroker/Client)
        │  (RPC/JSON) │  Connects to the Unix socket
        └──────┬──────┘
               │
        ┌──────▼──────────────────┐
        │ Root-owned systemd      │
        │ stepanel-root-broker    │
        │ Unix socket             │
        └──────┬──────────────────┘
               │
        ┌──────▼──────┐
        │  Broker     │  (runs as root)
        │  - Validates input
        │  - Routes to handlers
        │  - System operations
        └──────┬──────┘
               │
        ┌──────▼──────────────────┐
        │ System Operations       │
        │ - useradd/userdel       │
        │ - systemctl             │
        │ - mysql/postgresql      │
        │ - git, chmod, chown     │
        └─────────────────────────┘
```

## Setup

### 1. Build and Install the Broker Binary

```bash
# Build the root broker
go build -o /tmp/stepanel-root ./cmd/stepanel-root

# Install as root helper
sudo install -o root -g root -m 0755 /tmp/stepanel-root /usr/local/sbin/stepanel-root
```

### 2. Configure the broker service

Native installations use the root-owned service and peer-authorized socket;
they do not add a sudoers rule for the panel account:

```bash
sudo install -m 0644 deploy/stepanel-root-broker.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now stepanel-root-broker.service
```

Set `STEPANEL_ROOT_BROKER_SOCKET=/run/stepanel-root-broker.sock` in the panel
environment. The stdin/subprocess invocation in older versions is retained
only for non-production compatibility callers and tests; it is rejected when
`STEPANEL_ENV=production`.

### 3. Concurrency and admission

The socket broker serves each connection on its own goroutine and admits
operations through a resource-scoped scheduler (`internal/rootbroker/scheduler.go`):

- Operations on the same site, database, account, or certificate domain run
  one at a time; operations on unrelated resources run concurrently.
- At most `-max-concurrent` operations (default 8) execute at once. Time spent
  queued never shortens an operation's own timeout budget, but a request that
  cannot be admitted within that budget fails with `root broker is busy`.
- A request whose resource cannot be derived runs exclusively: it waits for
  running operations to drain, and new operations queue behind it.
- `health` probes bypass admission, so a broker busy with a long Composer
  install or certificate issuance still reports healthy.
- Host-wide state (web server configuration, systemd units, database engine
  catalogs) stays serialized by the helpers' own `flock` locks; host account
  changes (`useradd`, `userdel`, `usermod`) are serialized inside the broker.
- Each connection must send its request within 30 seconds and read its
  response within 30 seconds, requests are capped at 96 MiB, and at most 64
  connections are served at once.

## Integration Pattern

### Step 1: Create Client Instance

In your service or handler:

```go
package sites

import (
    "context"
    "stepanel/internal/rootbroker"
)

type SiteService struct {
    broker *rootbroker.Client
}

func NewSiteService(webRoot string) (*SiteService, error) {
    client, err := rootbroker.NewClient(
        "/usr/local/sbin/stepanel-root",
        webRoot,
    )
    if err != nil {
        return nil, err
    }
    return &SiteService{broker: client}, nil
}
```

### Step 2: Replace Shell Helper Calls

**Before (shell helper):**
```go
func (s *SiteService) CreateSite(ctx context.Context, name string) error {
    cmd := exec.CommandContext(ctx, "sudo", "stepanel-sitectl", "create", name)
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("site creation failed: %w", err)
    }
    return nil
}
```

**After (typed broker):**
```go
func (s *SiteService) CreateSite(ctx context.Context, name string) error {
    resp, err := s.broker.SiteCreate(ctx, name, "")
    if err != nil {
        return fmt.Errorf("RPC failed: %w", err)
    }
    if !resp.OK {
        return fmt.Errorf("site creation failed: %s", resp.Error)
    }
    
    var result rootbroker.SiteResponse
    if err := json.Unmarshal(resp.Details, &result); err != nil {
        return fmt.Errorf("failed to parse response: %w", err)
    }
    
    return nil
}
```

### Step 3: Error Handling

All operations return structured responses:

```go
type Response struct {
    OK      bool            `json:"ok"`
    Error   string          `json:"error,omitempty"`     // User-friendly message
    Details json.RawMessage `json:"details,omitempty"`   // Operation-specific data
}
```

Handle errors like:

```go
resp, err := broker.Execute(ctx, req)
if err != nil {
    // Network/communication error
    return fmt.Errorf("broker RPC failed: %w", err)
}
if !resp.OK {
    // Operation failed (validation, system error, etc.)
    return fmt.Errorf("operation failed: %s", resp.Error)
}

// Parse operation-specific response
var opResp SiteResponse
json.Unmarshal(resp.Details, &opResp)
```

## Request Types

Typed requests the broker executes today (verified against
`internal/rootbroker/broker.go`, 2026-10-02). Actions not listed return a
`not implemented by the root broker` error rather than doing anything.

| Request type | Supported actions | Not implemented (use the helper path) |
|--------------|-------------------|---------------------------------------|
| `site` | `create`, `delete`, `seal`, `prepare` | `access`, `resources`, `quota`, `quota-clear`, `runtime` (via `sitectl` helper requests) |
| `app` | `apply`, `delete`, `start`, `stop`, `restart` | `rollback` (orchestrated by the panel as a re-apply) |
| `db` | `inventory`, `dump`, `provision`, `restore-dump`, `restore`, `restore-wordpress`, `drop`, `drop-managed`, `cleanup-wordpress`, `rotate` | none |
| `task` | `apply`, `delete`, `kill`, `history` | none |
| `certificate` | `issue` | none |
| `git` | `generate`, `public`, `delete` (deploy keys) | `clone`, `verify-key` (`clone` via the `gitctl` helper) |
| `vhost` | none | `apply`, `apply-auth`, `delete` (via `vhostctl` helper requests) |
| `proxy` | none | `apply`, `reload` (via `proxyctl` helper requests) |
| `helper` | the allow-listed actions in `helper_schema.go` | anything not declared there |
| `health` | always (bypasses the scheduler) | none |

Database dumps and restores stream through files instead of the JSON
response: for `restore*` the broker reads `dump_path`; for `dump` with
`dump_path` it writes into an empty, single-link, non-root-owned regular file
the panel created under an approved staging root (`/var/www`,
`/var/lib/ste-panel`, `/var/backups/stepanel`), checked on the opened
descriptor. Production backups always use this path, so a dump's size is not
limited by the 64 MiB in-response cap.

Example:

```go
resp, err := client.Execute(ctx, &rootbroker.Request{
    RequestType: "db",
    DB: &rootbroker.DBRequest{
        Action:   "provision",
        Site:     "mysite",
        Database: "mysite_app",
        Username: "mysite_app",
        Password: secret, // 20+ characters; sent to the helper on stdin, never logged
        Encoding: "utf8mb4",
    },
})
```

## Validation

All input is validated at the broker entry point. Validation errors are returned immediately:

```go
resp, err := broker.Execute(ctx, req)
if !resp.OK && strings.Contains(resp.Error, "validation") {
    // Input validation failed - safe to show to user
    return fmt.Errorf("invalid request: %s", resp.Error)
}
```

**Validated inputs include:**
- Site names: lowercase alphanumeric, dash, underscore; 1-32 chars
- Domains: valid FQDNs with dots
- Ports: 1024-65535
- SSH keys: ed25519, ecdsa, rsa variants; no CR characters
- Bcrypt hashes: exactly 60 chars, valid prefix
- Database names: lowercase alphanumeric, underscore; max 64 chars
- PHP versions: X.Y[.Z] format
- Node versions: X.Y.Z format
- Git repos: SSH or HTTPS URLs; no traversal or newlines
- Git refs: alphanumeric, slash, dash, dot; max 256 chars

## Testing

```bash
go test ./internal/rootbroker ./cmd/stepanel-root
```

These run without root. Host account and ownership changes go through an
injected `hostOps` fake, so the suite covers:

- Request validation for every request type and the helper argument schema
  (`validator_test.go`, `helper_schema_test.go`)
- RPC round trips and broker routing (`integration_test.go`, `broker_test.go`)
- Recovery journals for site creation, database restore, and vhost changes
  (`journal_test.go`)
- Scheduler admission: per-resource serialization, the concurrency bound,
  exclusive requests, and health bypass (`scheduler_test.go`)
- The socket server: concurrent connections, stalled peers, and malformed
  requests (`cmd/stepanel-root/main_test.go`)

Real host operations (`useradd`, PHP-FPM pools, quotas) are exercised by the
installation smoke tests and the disposable-VM recovery drills, not by
`go test`.

## How the broker relates to the helper scripts

In production the broker is the privilege boundary:

- Long-lived root daemon over a peer-authorized Unix socket

It does not replace the root helper scripts in `deploy/integrations/stepanel-*`;
it puts one validated entry point in front of them:

```
Panel (unprivileged)
  └─ typed request over a peer-authorized Unix socket
       └─ stepanel-root: validate request → acquire resource locks
            └─ run helper script with an argument vector (no shell string)
```

Typed requests carry their own Go validation. The remaining generic `helper`
requests are checked against a per-action argument schema
(`helper_schema.go`). Replacing those with dedicated typed requests, and
retiring the generic path, is the remaining migration work.

## Gradual Migration Strategy

1. **Phase 1: Foundation** ✅ (Complete)
   - Broker types and validator implemented
   - Client RPC layer implemented
   - Unit tests pass

2. **Phase 2: Native socket deployment** ✅ (Complete)
   - Install the root broker as a separate systemd service
   - Authorize the panel and worker through the socket group
   - Remove the native panel sudoers grant

3. **Phase 3: Mutation migration** ✅ (Complete for native installs)
   - Route site, application, vhost, TLS, Git, and large database restore
     mutations through the typed broker
   - Keep stdin/helper execution only for lab, development, and legacy upgrade
     compatibility

4. **Phase 4: Hardening and coverage** (Ongoing)
   - Expand adversarial transition coverage and broker action matrices
   - Remove remaining compatibility paths when older installations no longer
     require them

## Troubleshooting

### "permission denied" Running Broker

Check the broker service and socket:
```bash
sudo systemctl status stepanel-root-broker.service
sudo test -S /run/stepanel-root-broker.sock
```

The panel account must be a member of the socket's `stepanel` group. Do not
restore a sudoers grant as a workaround; fix the broker service or socket
permissions instead.

### Broker Process Hangs

Check for:
- File descriptor leaks
- Orphaned processes
- DNS timeouts

Use timeout:
```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
resp, err := client.Execute(ctx, req)
```

### Validation Failures

Check error message:
```go
if !resp.OK {
    log.Printf("broker error: %s", resp.Error)  // e.g., "site name too long"
}
```

Validator runs at broker entry point, so all errors are deterministic.

## Security Model

The broker follows these principles:

1. **Least Privilege**: Only runs what's needed
2. **Input Validation**: All inputs checked before operations
3. **Explicit Types**: No ambiguous string parsing
4. **Audit Trail**: All operations logged with context
5. **Failure Safety**: Site creation, database restore, and vhost changes keep recovery journals so an interrupted operation can be rolled back or resumed; other operations rely on the helper's own rollback and are not atomic across resources
6. **No Secrets in Logs**: Passwords/keys not logged (but URLs safe to log)

## Performance

There are no broker benchmarks yet. Concurrency is described in
[Concurrency and admission](#3-concurrency-and-admission): unrelated
resources run in parallel up to `-max-concurrent` (default 8), and health
probes are never queued behind long operations.

## Related Documents

- [HELPER_LAYER_ROADMAP.md](archive/HELPER_LAYER_ROADMAP.md) — Overall strategy and timeline
- [CLAUDE.md](../CLAUDE.md) — Code quality standards (applies to broker)
- [SECURITY.md](SECURITY.md) — Security threat model
- [/internal/rootbroker/types.go](../internal/rootbroker/types.go) — Request/response types
- [/internal/rootbroker/validator.go](../internal/rootbroker/validator.go) — Validation rules
