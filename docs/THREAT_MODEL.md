# Threat model

This is StePanel's threat model for the single-host 1.0 contract
([PRODUCTION_READINESS.md](PRODUCTION_READINESS.md)). It is organized by
trust boundary, and for each boundary it lists the STRIDE threats, the
control that answers each one (with the code that implements it), and what
remains. It was last reviewed against `main` on 2026-11-13. It is the
project's own review, not the independent review that
[V1_PRODUCTION_GATES.md](V1_PRODUCTION_GATES.md) still requires.

Related documents: [SECURITY.md](../SECURITY.md) (reporting and support
policy), [SUDO_THREAT_MODEL.md](SUDO_THREAT_MODEL.md) (privileged helpers),
[SECURITY_CLAIMS_VERIFICATION.md](SECURITY_CLAIMS_VERIFICATION.md) (claims and
the tests behind them), [AUDIT_CONTRACT.md](AUDIT_CONTRACT.md).

## Assets

- Administrator and customer credentials, TOTP seeds, sessions and API tokens.
- The root boundary: the root broker and privileged helpers, which can create
  accounts, change ownership, and configure web, PHP, database and mail
  services.
- Customer site files, databases, mailboxes and backups, including other
  tenants' data on the same host.
- Control-plane state (SQLite database, job state) and the audit chain.
- Secrets: the session, account, audit, checkpoint, backup signing and backup
  encryption keys, webhook secrets, deploy keys, database credentials.

## Adversaries

| Adversary | Starting position |
| --- | --- |
| Remote attacker | Network access to the panel listener and to hosted sites |
| Tenant (customer) | A valid customer session or API token scoped to their own sites |
| Compromised site | Arbitrary code running as one site's Unix account (a hacked WordPress, a malicious plugin) |
| Malicious input | A crafted archive, Git repository, webhook delivery, `.htaccess` file or migration source |
| Compromised panel process | Code execution as the `stepanel` service account |
| Storage provider | Read and write access to offsite backup objects |

An administrator is trusted: they can delete or replace any site. Root on the
host is outside the model.

## The central invariant

The root broker authorizes a caller by its socket peer credentials: exactly
the `stepanel` UID and primary GID (`authorizePeerCredential`,
`cmd/stepanel-root/main.go`). It has no notion of tenants; the panel decides
which site a request may touch. So **anything that runs as the `stepanel`
account holds the broker's full request surface**, which is narrower than
root (typed requests, no arbitrary commands) but spans every tenant.

Tenant-controlled code and data must therefore never execute as `stepanel`:

| Path | How tenant code is kept out of the panel account |
| --- | --- |
| WP-CLI (loads `wp-config.php` and plugins) | Production runs it through the broker as the site's own user (`wpcli.go`) |
| Composer, Node and Python builds | Typed broker app operations as the site user; Composer runs with `--no-scripts --no-plugins` |
| Git clones | `stepanel-gitctl` runs Git as the dedicated `stepanel-git` identity with credential helpers and prompts disabled |
| Release pipeline builds | Rootless runner container, read-only source, artifact validated before activation |
| Commit reporting for Composer records | Reads `.git/HEAD` through an `os.Root`; never runs Git in the site tree (`gitHead`, `composer.go`; changed in this review) |
| Scheduled tasks | systemd units run as the site user (`stepanel-taskctl`) |

The panel does read and write tenant trees (restores, the file manager,
rollback copies). Those paths use descriptor-rooted operations so a tenant
cannot redirect them with symlinks; see the filesystem boundary below.

## Boundaries

### 1. Browser and API clients → panel

| STRIDE | Threat | Control |
| --- | --- | --- |
| Spoofing | Password guessing, stolen session | bcrypt passwords; per-client and per-account login limits; TOTP with persisted replay counters; sessions HMAC-signed, 12-hour expiry, `HttpOnly`, `SameSite=Strict`, `Secure` behind TLS (`auth.go`) |
| Spoofing | Stolen API token | Tokens stored as SHA-256 of a high-entropy secret, scoped, expiring and rate-limited (`api_tokens.go`); scoped tokens follow the owner's current role |
| Tampering | Cross-site request forgery | CSRF token bound to the session on every mutating route |
| Repudiation | Denying a change | Class A security ledger written before the mutation, HMAC-chained with signed checkpoints ([AUDIT_CONTRACT.md](AUDIT_CONTRACT.md)) |
| Information disclosure | Reading another tenant's sites | Handlers take a `SiteCapability` issued after an ownership check; customer roles (owner, manager, developer, viewer) and token scopes narrow it further |
| Denial of service | Large uploads, slow clients, event streams | Split server timeouts, upload admission against free space, stream concurrency limits |
| Elevation | Customer reaching administrator routes | Administrator-only routes check the role; customer scopes are an allowlist |

Residual: the built-in administrator is a single account; OIDC and
per-operator administrator identities are roadmap items. A stolen
administrator session is game over for the host's tenants.

### 2. Panel → root broker and helpers

| STRIDE | Threat | Control |
| --- | --- | --- |
| Spoofing | Another local user calling the broker | Root-owned Unix socket; `SO_PEERCRED` must match the service UID and GID; refusals are logged with the peer's PID |
| Tampering | Malformed or oversized requests | Typed JSON requests with per-type validation, size budgets and read deadlines (`serveConn`) |
| Elevation | Turning a request into arbitrary root commands | No generic exec request; helpers take fixed argument shapes; [SUDO_THREAT_MODEL.md](SUDO_THREAT_MODEL.md) covers the helper surface |
| Denial of service | Exhausting broker connections | Bounded connection slots |

Residual: see the central invariant. A compromised panel process can issue
any typed request for any site.

### 3. Site accounts ↔ shared filesystem

Site accounts write their own trees, including retained releases and staging
for Git checkouts. The panel and root act on those trees.

| STRIDE | Threat | Control |
| --- | --- | --- |
| Tampering | Symlink or hard-link swaps redirecting a root `chown`/`chmod` | Seal walks descriptor-relative with `openat2(RESOLVE_BENEATH\|RESOLVE_NO_SYMLINKS)` (`internal/rootbroker/secure_seal.go`); the installer pins `fs.protected_hardlinks` and `fs.protected_symlinks` |
| Tampering | Changing a retained release before a rollback activates it | Rollback copies the tree descriptor by descriptor into panel-owned staging, verifies the copy against the digest recorded at retirement, and activates the copy ([GIT_DEPLOYMENTS.md](GIT_DEPLOYMENTS.md#roll-back)) |
| Information disclosure | Using a panel read to reach another tenant's files | The file manager and restores use rooted descriptors (`internal/sitefiles`, `os.Root`); the rollback copy refuses symlinked components |
| Denial of service | Filling the disk | Per-site user quotas on `/var/www` and `/home`; the capacity ledger reserves space above `STEPANEL_MIN_FREE_BYTES` before staging |
| Elevation | Reading or writing other sites | Per-site Unix users and groups; the web server reads through group access without site users joining a shared group; PHP-FPM pools per site |

Residual: per-site isolation is Unix permissions, not a container or VM.
A local kernel privilege escalation from site code breaks every boundary.

### 4. Untrusted inputs

| Input | Threat | Control |
| --- | --- | --- |
| cPanel, WPress and site archives | Traversal, links, devices, decompression bombs | One archive policy: type allowlist, path normalization, `os.Root` extraction with exclusive creates, entry, size and metadata limits (`internal/archivesafe`) |
| Git repositories | Malicious contents, hooks, oversized trees | Allowlisted hosts and exact URL grammar; shallow single-ref checkout; symlinks and special files rejected; `.git` removed; tree size and entry limits; no repository scripts run in the panel |
| Webhook deliveries | Forged or replayed deployments | Per-site HMAC secret, sealed at rest; signed timestamps with a replay window; repository allowlist; a secret shared by two sites is refused; secret generations revoke queued deliveries ([GIT_DEPLOYMENTS.md](GIT_DEPLOYMENTS.md)) |
| `.htaccess` | Web-server configuration injection | Translated into a narrow directive set; the privileged helper revalidates each line |
| Migration sources | Hostile remote hosts | SSH with `BatchMode=yes` and `StrictHostKeyChecking=yes`; inspected data treated as untrusted |
| Outbound URLs | Server-side request forgery | `internal/safehttp` resolves names and refuses private, loopback and link-local destinations at connect time |

Residual: a compromised allowlisted Git host or repository can publish
malicious application code. The deployment path keeps that code out of the
panel, but it runs as the site.

### 5. Control-plane state and secrets

| STRIDE | Threat | Control |
| --- | --- | --- |
| Tampering | Editing the audit log | Sequence numbers and an HMAC chain; checkpoints signed with a separate key that production requires; appends re-validate the chain after any change |
| Information disclosure | Secrets at rest | `secretbox` (HKDF-derived keys, the record's context as associated data) for TOTP seeds and webhook secrets; webhook fingerprints are keyed HMACs; keys live in the root-owned, mode 0600 `/etc/ste-panel.env`, which systemd reads for the service, and the local database credential is handed over with `LoadCredential` |
| Tampering | Interrupted operations leaving inconsistent state | Durable jobs; journals for site transactions and release activations; recovery runs before the panel serves |
| Denial of service | Unreadable secrets after a lost key | Startup stops; `stepanel webhook-secrets disable-unreadable` recovers ([ENCRYPTION_KEYS.md](ENCRYPTION_KEYS.md)) |

### 6. Backups and offsite storage

| STRIDE | Threat | Control |
| --- | --- | --- |
| Tampering | Replacing an archive and its manifest | Manifests signed with `STEPANEL_BACKUP_SIGNING_KEY`, which is kept outside the backup root |
| Information disclosure | Reading offsite copies | Archives encrypted per backup (HKDF subkey, AES-256-GCM); only the key ID leaves the host |
| Tampering | Deleting offsite copies | Out of scope for the panel; use object lock or immutable retention at the provider |
| Spoofing | One site restoring another's backup | Each site's backups live in their own directory; the manifest's site must match the directory and the requesting site |

## Findings from this review

| Severity | Finding | Status |
| --- | --- | --- |
| Low | The Composer record ran `git -C <document root> rev-parse HEAD` as the panel account inside a tenant-controlled tree, with inherited Git configuration. Modern Git's ownership check refused it, and `rev-parse` runs no configured commands, but nothing in StePanel guaranteed either. | Fixed: `gitHead` reads `.git/HEAD` and the ref through `os.Root` and accepts only an object ID. |

Reviews earlier in November found and fixed the rollback, webhook and backup
issues recorded in the [CHANGELOG](../CHANGELOG.md) (findings F-01 to F-08,
S-01 to S-07, the rollback copy review, and R-01 to R-08). One residual from
them remains: on filesystems without `renameat2(RENAME_EXCHANGE)` (for
example NFS), release activation leaves the document root briefly absent.

## Residual risks

- A compromised administrator, or a compromised panel process, can affect
  every tenant (see the central invariant).
- Site isolation is Unix users, groups, ACLs and quotas on one kernel.
- Restored WordPress and other applications are untrusted code from the moment
  they are extracted.
- Remote database deployments may give the panel an administrative database
  credential; local installs use the restricted helper instead.
- Allowlisted Git providers and their repositories are trusted for the code
  they deliver.
- Offsite deletion and retention are the storage provider's controls.

## Operator requirements

Run StePanel behind HTTPS and keep the listener on a private interface or
behind a reverse proxy. Keep the backup signing and encryption keys outside
the backup root and escrow them separately. Use object lock or immutable
retention for offsite backups. Keep site homes on a local filesystem that
supports `RENAME_EXCHANGE`. Never set the safety-bypass variables the
installer warns about (`STEPANEL_SKIP_*`, `STEPANEL_LAB_*`) on a production
host. Snapshot a destination before a restore, and review the audit log after
every import.
