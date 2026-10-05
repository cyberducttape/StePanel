# Threat model

## Assets

- Administrator/customer credentials, MFA material, and session cookies.
- Uploaded cPanel backups and extracted site files.
- Database credentials and imported SQL data.
- Server configuration and audit history.

## Trust boundaries

1. The browser is untrusted and can submit arbitrary form fields.
2. Uploaded archives are untrusted data and may contain malicious paths or files.
3. StePanel runs as a service account and must not become a general root command
   execution interface.
4. Caddy/Apache/OpenLiteSpeed, PHP, and database services are external system dependencies.

## Existing controls

- Authenticated mutating routes require CSRF validation.
- Sessions are signed, expiring, and protected with HttpOnly/SameSite cookies.
  Customer credential, MFA, and suspension changes increment a durable session
  generation; this invalidates existing customer sessions even if registry
  revocation cannot be persisted at the same moment.
- TOTP adds replay-resistant second-factor validation; accepted counters are
  persisted in the control-plane database when the production database is
  configured. Customer TOTP material is
  AES-GCM encrypted at rest when `STEPANEL_ACCOUNT_KEY` is configured, bound
  to its username so a seed cannot be moved to another account (see
  SECRETS.md, "Context-bound encryption").
- Login attempts are rate-limited.
- Uploads are size-limited and staged privately.
- Archive paths are checked for absolute paths and traversal.
- Restore destinations are account-scoped and SQL restoration is opt-in.
- Audit events distinguish actor from target, are sequence/HMAC linked, and
  fail closed before authenticated mutating handlers when persistence is down.
- Active Caddy and Apache snippets are root-owned and rendered by fixed-template helpers.
- `.htaccess` input is translated into a narrow directive set; the privileged
  Caddy helper revalidates every translated line and never receives the source
  file.
- Concurrent restores targeting the same site are rejected.
- New database destinations are rolled back on restore failure; existing
  databases and database users are never silently reused.
- Background job state and site-overwrite transactions are persisted before
  mutations begin; interrupted site transactions are rolled back at startup.
- Host site workloads use deterministic per-site Unix identities and unique
  primary groups. The selected webserver receives group access without making site users
  members of its shared group, and the control plane uses explicit ACLs.
- Git deployment accepts only allowlisted providers and repository forms. Public
  HTTPS clones disable credential prompts/helpers; private SSH clones use
  per-site deploy keys, a root-controlled known-hosts file, and
  `StrictHostKeyChecking=yes`. Both paths reject symlinks and special files,
  strip repository metadata, and do not execute repository-provided build
  scripts in the control plane.
- Staging Basic Auth is enforced only through the Caddy and Apache helpers.
  Unsupported OpenLiteSpeed requests are rejected instead of accepting a
  protection setting that cannot be applied.

## Residual risks

- A compromised administrator can request destructive restores.
- A restored WordPress application is untrusted code after extraction.
- Remote database deployments may give the service account a powerful
  administration credential; compromise requires credential rotation and a
  database integrity review. Local installs instead use the restricted helper.
- TLS and multi-user OIDC remain deployment responsibilities or roadmap items;
  the built-in administrator is still a single shared identity.
- Backup checksums detect corruption but are not signatures. An actor able to
  modify both an archive and its manifest can replace both.
- An allowlisted Git provider and repository contents remain trusted inputs.
  Compromise of either can publish malicious application code even though the
  deployment path prevents control-plane command execution. Rootless build
  runners reduce control-plane exposure but may access the network when a build
  requires dependency downloads; build inputs and resulting artifacts remain
  untrusted application code.

## Operator requirements

Run StePanel behind HTTPS, restrict the listener to a private interface or
reverse proxy, snapshot the destination before restores, and review the audit
log after every import. Replicate completed backup directories to independently
controlled immutable or off-host storage and verify them again there.
