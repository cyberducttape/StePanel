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

## Roll back

Rollback activates the newest preserved release and retains the release it
replaces:

```sh
curl -X POST https://panel.example.test/api/sites/git-rollback \
  -H 'Content-Type: application/json' \
  -H 'X-CSRF-Token: <session token>' \
  --data '{"site":"example","confirm":"ROLLBACK example"}'
```

The preserved release is safety-checked again before activation. Deployment
and rollback restore site ownership/isolation through the site helper and write
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
