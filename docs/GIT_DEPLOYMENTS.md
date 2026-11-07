# Git site deployments

StePanel can deploy a pre-built site tree from an HTTPS public repository or a
private SSH repository authenticated by a per-site deploy key. The operation is
intended for repositories whose committed contents are already deployable.
StePanel does not execute repository scripts, package managers, hooks, or build
commands inside the control-plane process.

## Prerequisites

- Create the site and its isolated site root before deployment.
- Install the `git` client on the host.
- Add every permitted repository hostname to
  `STEPANEL_GIT_ALLOWED_HOSTS`. The default is
  `github.com,gitlab.com,bitbucket.org`.
- Use a public HTTPS URL without embedded credentials, query parameters, or a
  nonstandard port, or a private `git@host:owner/repository.git` URL.

## Private repositories (Shipped)

Create a site-scoped deploy key first:

```sh
curl -X POST https://panel.example.test/api/sites/git-key/example \
  -H 'X-CSRF-Token: <session token>'
```

Copy the returned public key into the repository provider's read-only deploy-key
screen, then use `git@github.com:acme/site.git` in the deployment request. The
private key is generated and retained under root ownership by
`stepanel-gitctl`; it is never returned through the API, stored in panel state,
or exposed to build containers. StePanel accepts only the `git` SSH user and
the existing exact repository-host allowlist. Retire a key with `DELETE` on the
same endpoint before replacing repository access. Before the first private
clone, install verified provider SSH host keys in root-owned
`/etc/stepanel/git-known-hosts`; the helper uses strict host-key checking and
will not trust a host key on first use.

The clone itself never runs as root or as the panel's `stepanel` service
account, for public (HTTPS) and private (SSH) repositories alike.
`stepanel-gitctl` prepares the quota-limited release stage as root, then runs
`git` (and, for private repositories, `ssh`) as the
dedicated `stepanel-git` system account (created by `install.sh`; no login
shell, no other groups, not the panel or any customer account) with no new
privileges and an empty capability set. For the duration of the clone the
stage belongs to that account alone (mode `0700`), so the tenant cannot swap
entries, and the deploy key and host keys are readable only through a private
per-clone copy under `/run` that is removed afterwards. Clones are serialized
host-wide, so the identity never holds two sites' keys or stages at once, and
any process it left behind is killed before root hands the finished tree to
the site's owner. Git may use only the clone's own transport (`https` for
public repositories, `ssh` with the site's deploy key and pinned host keys for
private ones), never prompts for or stores credentials, ignores system Git
configuration, runs no repository hooks, and does not follow HTTPS redirects,
so a redirect cannot lead outside `STEPANEL_GIT_ALLOWED_HOSTS`. Outbound
network access is limited by that host allowlist, not by a per-identity
firewall rule; add one for `stepanel-git` if your policy requires it. A failed
clone's files are removed as root before its stage
is returned, so the tenant never receives files the clone account owns.

If a clone process cannot be reaped (for example, it is stuck in
uninterruptible I/O), the clone fails closed: the stage and the per-clone key
copy stay root-owned, a record is written under
`/var/lib/stepanel/git-clone-quarantine`, and every later clone refuses to
start until no `stepanel-git` process remains. Each clone first runs the
recovery itself; an operator can run it directly with
`sudo /usr/local/sbin/stepanel-gitctl clone-recover`, which reclaims
quarantined stages only after confirming that no clone process is alive,
empties them, and returns them to their previous owner.

## Deploy

Send an authenticated JSON request with the session CSRF token:

```sh
curl -X POST https://panel.example.test/api/sites/git-deploy \
  -H 'Content-Type: application/json' \
  -H 'X-CSRF-Token: <session token>' \
  --data '{"site":"example","repository":"https://github.com/acme/site.git","ref":"main"}'
```

StePanel performs a shallow, single-ref checkout with credential prompting and
Git credential helpers disabled. It verifies the commit identifier, rejects
symlinks and special files, applies the configured entry limit, removes `.git`,
and atomically replaces the site's `public` directory. The prior directory is
retained under the site root as `.stepanel-previous-<id>`.

Git checkout, build-pipeline, activation, and rollback operations are serialized
per site. Operations for different sites may proceed concurrently; same-site
pipelines cannot race over the shared `.stepanel-artifact` path or release
pointer. The request queues a durable job and answers `202` with its
`job_id`; the completed job's `output` carries the exact deployed commit.
Signed webhook deliveries are queued the same way, and the worker re-reads the
site's webhook policy before the deployment starts.

Webhook authority is bound to a secret generation and to the site's lifecycle,
not to the site name. Each queued delivery records the generation of the
secret that authenticated it (never the secret itself), and the worker refuses
it unless that generation is still current. Rotating the secret, or disabling
the webhook and configuring it again, advances the generation, so deliveries
accepted under the previous configuration are refused
(`webhook.deploy.revoked`) instead of deployed; updating only the repository or
ref allowlist does not. Configuring a disabled webhook re-enables it. Site
termination revokes the site's webhook (secret, allowlists, and any queued
delivery) before ownership is detached, and creating a site clears any webhook
authorization left under its name, so a reused name inherits nothing.

Each site needs its own signing secret. The signature covers the delivery
timestamp, delivery ID, and body, not the target site, so the secret is what
ties a delivery to one site: configuring a secret another site already uses
is refused (`422`). Sites that shared a secret before that check existed are
named in a startup warning, and their deliveries are refused (`403`,
`webhook.auth.shared-secret`) until each is configured with its own secret;
rotating one site of a pair clears both. Secrets are sealed at rest with
`STEPANEL_ENVIRONMENT_KEY` (see [SECRETS.md](SECRETS.md)), and startup stops
if a sealed secret does not open with the configured key
([ENCRYPTION_KEYS.md](ENCRYPTION_KEYS.md)).

Each signed delivery is queued at most once. Its delivery ID keys the durable
deployment job for as long as the signature is valid (the signed timestamp
plus five minutes), so a retry of a delivery that failed to queue succeeds,
and a replay of one that was queued answers `409`. Repository allowlists
compare the scheme and host case-insensitively and the path exactly. Entries
saved by earlier releases were lowercased, so the case of their path is
unknown: on github.com and gitlab.com, which resolve repository paths
case-insensitively, they still match any case; on every other host they
match only the lowercased path. The startup log names sites with such
entries; saving the webhook again with the repository's exact URL clears
the warning.

## Roll back

Rollback activates the newest preserved release and retains the release it
replaces:

```sh
curl -X POST https://panel.example.test/api/sites/git-rollback \
  -H 'Content-Type: application/json' \
  -H 'X-CSRF-Token: <session token>' \
  --data '{"site":"example","confirm":"ROLLBACK example"}'
```

The preserved release is safety-checked again before activation. When a
release stops being active, the panel records a SHA-256 digest of its
contents (every path, permission, file's bytes and symlink target) in the
control-plane database, which the site account cannot write. The rollback
preview re-hashes the target and reports `content_integrity`:

- `verified`: the contents match the digest recorded at retirement.
- `modified`: they do not, or the tree can no longer be read. The rollback is
  refused (`409`, `site.git-rollback.refused`); deploy the commit again.
- `unrecorded`: the release was retired before digests were recorded, or was
  too large to hash within `STEPANEL_GIT_RELEASE_MAX_TREE_BYTES`.

The active release is not covered: a running application may write caches or
uploads into it, so its contents are fixed only from retirement. Hashing reads
the whole retained tree, once when it is retired and again for each preview
and rollback. The site account can still write retained releases; the digest
detects such a change, it does not prevent it. Deployment and rollback
restore site ownership/isolation through the site helper and write
tamper-evident audit events.

## Operational boundaries

- Git deployment changes site files only. It does not migrate databases,
  change runtime configuration, restart Node, or issue certificates.
- The repository host allowlist is exact; subdomains are not implicitly trusted.
- Use the dedicated sandboxed runner for Composer, npm, framework builds,
  tests, artifact signing, and secret injection. Its artifact output is not
  activated automatically yet; a first-class release pipeline is Preview work.
- Preserved releases consume site storage. StePanel automatically retains the
  configured rollback window and prunes older validated releases at startup
  and periodically; monitor the exposed release-storage metrics and retain
  backups independently.
- Database changes require a separate migration and rollback plan.
