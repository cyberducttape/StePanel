# Security Policy

StePanel is a control plane, not a general-purpose root shell. It can inspect
untrusted migration archives and, when explicitly authorized, change site,
database, webserver, cloud, and service state. Treat the panel, its runtime
state, and every configured helper as security-sensitive infrastructure.

## Supported versions

Until the first stable release, security fixes are applied to the `main` branch. Production operators should pin a reviewed commit rather than track an unreviewed branch.

## Reporting a vulnerability

Do not open a public issue for a suspected vulnerability. Contact the repository owner privately through GitHub with:

- affected commit or version;
- reproduction steps or a minimal proof of concept;
- impact and likely attack prerequisites;
- any suggested mitigation.

Please allow time for investigation and a coordinated fix before public disclosure.

## Threat model

The primary threats are an attacker obtaining an administrator session, a
malicious or compromised uploaded archive, a compromised site attempting to
reach panel state, and unsafe arguments or environment inherited by a helper
process. Denial of service from oversized archives, expensive inspections,
wedged child processes, or full filesystems is also in scope.

The trust boundary is the authenticated panel operator. Site content,
uploaded archives, SQL dumps, Git repositories, remote cloud APIs, and remote
SSH hosts are not trusted. A successful login is therefore necessary but not
sufficient for safe operation: use HTTPS, MFA, host-level access controls,
backups, and restore drills. The panel does not provide tenant isolation or
make an untrusted site safe to administer from the same host.

## Privilege isolation

- Run the HTTP service as the dedicated unprivileged `stepanel` account.
- Keep sessions, audit state, job state, recovery journals, credentials, and
  backup staging outside site-controlled paths with restrictive ownership and
  permissions.
- Give the service account no general sudo access. Privileged operations must
  cross an individually installed, root-owned helper with a fixed interface;
  helpers must validate their arguments and perform their own authorization
  and filesystem checks.
- Keep database and webserver credentials out of requests, archives, logs, and
  child environments. Cloud and rclone credential files belong in protected
  host locations, not in a site root or repository checkout.
- Use a reverse proxy for TLS and network access control. Do not expose the
  backend listener directly to the internet.

## System-call and input safety

Request values are parsed into typed structures and checked against explicit
allowlists or bounded patterns before use. Commands are invoked with
`exec.CommandContext` and separate arguments rather than shell-concatenated
strings; timeouts and bounded output prevent a child from consuming workers or
memory indefinitely. Executable paths are configured as absolute paths or
resolved from the host PATH, and missing helpers fail closed.

Archive handling limits upload size and entry count, rejects absolute,
parent-traversing, symlink, and special-file entries, and stages content in a
private directory before restore. SQL identifiers are generated from validated
account names and escaped as SQL identifiers; existing destination databases
are refused for cpmove restores. Git deployment accepts only allowlisted HTTPS
hosts, disables interactive credentials, rejects unsafe checkout entries, and
never executes repository build hooks.

These controls reduce command injection and path traversal risk; they do not
turn arbitrary third-party content into trusted content. Review helper scripts,
service units, sudo policy, database grants, and filesystem ownership during
deployment and upgrades.

## Authorization boundaries

StePanel enforces several critical authorization boundaries with comprehensive test coverage:

- **Cross-tenant access denial**: Customers cannot read or modify resources owned by other customers. HTTP 403 responses prevent unauthorized access; audit logging captures all denial attempts. Tested via adversarial authorization matrix.

- **API token scope enforcement**: Tokens with limited scopes (e.g., "site:read", "deploy:write") cannot perform operations outside their scope. A token with only "site:read" cannot create backups, deploy code, or modify SSH access. Scopes are enforced at request handling time before privileged operations.

- **Privilege escalation prevention**: Customers cannot claim administrator identity through any request-level manipulation. Administrator status requires the configured admin username and is never inherited from API tokens, scopes, or request parameters. The IsAdministrator() boundary is tested to ensure non-admins are denied on all admin endpoints.

- **CSRF token requirement for mutations**: State-changing operations (POST, PUT, DELETE, PATCH) require a valid X-CSRF-Token header when issued from browser sessions. API token requests are exempt (they use bearer authentication, not cookies). Safe methods (GET, HEAD, OPTIONS) never require CSRF tokens.

- **Error message privacy**: Authorization denials do not reveal whether a resource exists. Error messages are generic to prevent attackers from probing for the existence of other customers' sites or resources.

All authorization boundaries are validated by an expanding test suite covering 25+ scenarios across API tokens, privilege boundaries, CSRF protection, and cross-tenant access.

## Audit and recovery expectations

Mutating requests require CSRF protection and persist a preflight audit event
before work begins. Restore and certificate jobs are durable, serialized per
site, and recoverable after restart. Audit chains are tamper-evident rather
than immutable: export them to independent, access-controlled retention when
they are needed for incident response. Snapshot a destination before a live
restore, review the job and audit result, and quarantine or remove failed
artifacts only after recovery state has been reconciled.

## Deployment requirements

StePanel provides administrator authentication, but production deployments
must still run behind HTTPS and a reverse proxy. Do not expose port 8080/8090
directly to the internet. Restrict backup staging permissions and use
snapshots before restoring into a live site. Production startup requires TOTP
MFA and an enforced offsite backup target; OIDC, role separation, and a
customer-scoped privileged restore workflow remain roadmap items.

Privileged helpers use context-bound commands with bounded output. Cloud CLI
children receive provider credentials and region settings but not panel-specific
session, audit, or database secrets. Keep provider credential files and rclone
configuration outside site-controlled paths and restrict them to the service
account or root.

Malformed recovery journals are moved into a root-only quarantine directory for
operator review. Do not delete quarantined journals until the related site,
database, and audit state has been reconciled.
