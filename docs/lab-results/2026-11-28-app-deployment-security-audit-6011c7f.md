# App deployment security audit and app crash drills (2026-11-28, `6011c7f6`)

This is an internal audit, not the independent security review the release
gates require. It records what was examined, what was found and fixed, and
what is still open, so that the independent review can start from it.

## How it started

The first KVM run of the new app crash drills
([46091872466](https://github.com/cyberducttape/StePanel/actions/runs/46091872466))
failed three scenarios that each deployed onto a port an earlier scenario's
app still held. Nothing stopped one site taking another site's application
port. That led to an audit of every customer-reachable path that publishes an
application, a proxy, or a domain.

## Findings and fixes

| # | Finding | Impact | Fix | Regression tests |
|---|---|---|---|---|
| 1 | A customer's Node deployment (`POST /api/deployments`, `site:deploy`) accepted any loopback or private-network proxy backend. | A tenant could publish the panel, another tenant's application, local services or LAN hosts under their own domain. The webserver proxy is outside the tenant egress block. | `3282c1e6`: a customer backend must be the application itself, on localhost and its own port. Administrators keep the wider rule. | `TestCustomerNodeBackendIsTheApplicationOnLocalhost`, `TestCustomerNodeDeploymentCannotProxyAnotherBackend` |
| 2 | Node deployments could take any port, including another site's application port or a port already in use. | A site's app could take over another site's backend, or roll back onto it later. | `3282c1e6`: no site may take a port another site's manifest or rollback manifest names, or one something else listens on unless its own app already runs there. Rollback is refused if the previous release's port is no longer available. | `TestAppPortConflict`, `TestAppPortListeningProbe`, `TestAppRollbackRefusesAPortAnotherSiteNowUses` |
| 3 | A customer's Node deployment never checked that its domain was a verified claim of the site. | One tenant could put a proxy on another tenant's domain, or on any domain pointed at the host. | `bb3c579f`: the domain must be verified for the customer's tenant when the deployment is queued and again before the worker runs it, as site routes and staging already require. The worker also re-checks the backend rule. | `TestCustomerNodeDeploymentRequiresAVerifiedDomain` |
| 4 | Python deployments (`POST /api/python/deploy`, customer-reachable) took any port. | A tenant's Python app could bind the port behind another site's proxy and be served under that site's domain once the site's own app stopped. | `6011c7f6`: the port check reads Node, Node rollback and Python manifests, and Python deployments use it. | `TestAppPortConflictCoversPythonApplications`, `TestPythonDeploymentRefusesAnotherApplicationsPort` |

## Surfaces examined and found sound

- Site routes (`/api/sites/deploy`) verify the domain before publishing and
  re-check it under the site and vhost locks. They also refuse to overwrite a
  vhost that has no managed route state.
- Staging creation verifies the target domain the same way.
- Git deployment accepts only HTTPS `.git` URLs or `git@host:path.git`, with
  no credentials, query or port, on hosts in `STEPANEL_GIT_ALLOWED_HOSTS`.
- Task webhooks are validated by the shared `safehttp` policy.
- Proxy deployment (`/api/proxy/deploy`), certificate issuance and the direct
  app routes (`/api/apps/*`) are administrator-only.
- The site file manager goes through the root broker and `stepanel-filectl`,
  which have their own validation and smoke test.

## Open

- **Manifest name collision.** A site named `foo-python` keeps its Node
  manifest at `foo-python.json`, the path of site `foo`'s Python manifest.
  The port check tells the two apart by the manifest's `site` field. A Node
  deploy refuses to overwrite a manifest whose site does not match, and the
  Python startup reconcile skips non-pending manifests. The remaining effect
  is that the two sites can block or overwrite each other's app state. This
  needs a manifest layout that cannot collide, or a reserved site-name
  suffix.
- **Rollback does not move the proxy.** Rolling back to a release on another
  port leaves the proxy on the current port until it is deployed again.
- **Cross-site port race.** The port check runs under the deploying site's
  lock, not a host-wide port lock. Two sites deploying the same free port at
  the same moment could both pass it; the second unit then fails to bind.

## App crash drills on an installed host

On Rocky Linux 9 under KVM, the guest was killed at each point and then
rebooted. Recovery had to leave one whole generation, with manifest, rollback
manifest, unit, listener and proxy agreeing, and a fresh action afterwards had
to succeed.

| Scenario | Point | Run | Result |
|---|---|---|---|
| app-rollback | runtime-applied | [46095808123](https://github.com/cyberducttape/StePanel/actions/runs/46095808123) | pass, back to the current release |
| app-start | runtime-applied | 46095808123 | pass, back to stopped |
| app-restart | runtime-applied | 46095808123 | pass, running |
| app-stop | runtime-applied | 46095808123 | pass, back to running |
| app-deploy | node-deploy:deploy-app | 46095808123 | pass, retried job completed |
| app-rollback | manifest-written | [46099231944](https://github.com/cyberducttape/StePanel/actions/runs/46099231944) | pass, back to the current release |
| app-rollback | committed | [46099238863](https://github.com/cyberducttape/StePanel/actions/runs/46099238863) | pass, kept the rolled-back release |

These ran on `3282c1e6`. The later fixes (`bb3c579f`, `6011c7f6`) add request
and worker checks, but do not change the activation or recovery code the
drills exercise. Repeats on the current tree remain open.
