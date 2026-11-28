# Node application deployment

Enable NVM during installation:

```sh
sudo STEPANEL_INSTALL_NODE=1 \
  STEPANEL_ADMIN_PASSWORD='use-a-password-manager' \
  STEPANEL_PANEL_HOSTNAME=panel.example.com \
  STEPANEL_NODE_VERSIONS=20.18.0,22.14.0 ./install.sh
```

The panel lists versions installed in the StePanel service account's NVM
directory. Selecting a version writes a `.nvmrc` into the managed site root;
it does not execute an application or install packages on behalf of the user.
Managed Node units run as the deterministic Unix identity created for that
site, with a unique primary group and write access limited to that site's
public root.

Deploying an app asks the root-owned `stepanel-appctl` helper to atomically
install and start its hardened systemd unit. Application lifecycle operations
are serialized per site, so dependency installation and builds for different
sites can proceed concurrently. Node, Composer, and Python package installation
run as that site user (the Python virtualenv is bootstrapped and chowned first).
A host-wide lock is held only while publishing/removing systemd unit files
and coordinating `daemon-reload`; unit start/stop/restart work runs outside
it. A failed activation restores the prior unit and runtime state. Proxy deployment is a separate operation through the selected
webserver's `stepanel-proxyctl` helper. The service account cannot write either
the systemd unit directory or managed webserver configuration directories.
The backend must be an `http://` endpoint on localhost or a private IP, must
include a port from 1 through 65535, and cannot target link-local addresses
(including cloud metadata endpoints). This prevents the proxy endpoint from
becoming an SSRF or open-proxy primitive.

A customer's Node deployment is narrower. Its domain must be a verified
claim of the site for the customer's tenant (the rule site routes follow),
checked when the deployment is queued and again before the worker runs it.
Its backend must be the deployed
application itself, on localhost and the application's own port, so a
customer cannot publish the panel, another service or a private-network host
under their domain. Only an administrator may route a domain to another local
or private backend. No site may take a port that another site's application
or rollback manifest names, and a port something else is already listening on
is refused unless the site's application already runs there. Rollback is
refused if the previous release's port has since become unavailable.
Rollback does not move the proxy: rolling back to a release on another port
leaves the proxy on the current port until it is deployed again.

The installer enables the required proxy integration and installs
`/usr/local/sbin/stepanel-proxyctl`. The Apache, Caddy, and OpenLiteSpeed
helpers validate all arguments, render fixed configuration templates, and
restore the prior configuration and attempt to reload it after activation
failure. Optional HTTPS issuance is described in `docs/CERTIFICATES.md`.
Application process supervision is provided by the managed systemd unit; use
the rollback endpoint when a process release needs to be reverted.

Managed proxies can be listed, backend-tested, and removed through the
authenticated proxy API. A failed webserver validation or reload restores the
previous proxy configuration automatically.

Git site deployment is independent of Node process deployment. It can
atomically replace and roll back committed site files, but it does not run
`npm install`, execute build scripts, restart the systemd unit, or alter the
proxy. See [`GIT_DEPLOYMENTS.md`](GIT_DEPLOYMENTS.md) and coordinate file,
process, proxy, and database changes as separate audited operations.
