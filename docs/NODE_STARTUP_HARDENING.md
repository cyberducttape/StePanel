# Node Application Startup Hardening

**Status:** Implemented (2026-11-02). `stepanel-appctl apply` resolves the
exact Node.js runtime when it writes the unit and runs it by absolute path.

## What the unit runs

Before, the generated unit selected Node at service start:

```ini
ExecStart=/bin/bash -lc 'export NVM_DIR=/opt/stepanel/.nvm; . "$NVM_DIR/nvm.sh"; nvm use 22.14.0 >/dev/null; exec npm start'
```

The semicolon meant a failed `nvm use` still ran `npm start`, so a host with
another Node or npm on `PATH` could start the application on a runtime other
than the one requested. The unit now names the resolved runtime directly:

```ini
Environment=PATH=/opt/stepanel/.nvm/versions/node/v22.14.0/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=/opt/stepanel/.nvm/versions/node/v22.14.0/bin/node /opt/stepanel/.nvm/versions/node/v22.14.0/lib/node_modules/npm/bin/npm-cli.js start
```

There is no shell, no nvm, and no `PATH` lookup for the start command. The
resolved runtime comes first on `PATH`, so npm scripts that run `node` use the
same version.

## Resolution rules

`resolve_node_runtime` in `deploy/integrations/stepanel-appctl` refuses the
apply, before the unit is written, unless:

- `versions/node/vVERSION` exists under `/opt/stepanel/.nvm` and is not a
  symlink;
- its `bin/node` and `lib/node_modules/npm/bin/npm-cli.js` are regular,
  non-symlink files owned by root or by the runtime's owner (the panel
  identity that installs Node), writable by nobody else, and `node` is
  executable;
- `node --version`, run as the site user (never root) with an empty
  environment, reports exactly `vVERSION`.

A refused apply leaves a running application on its previous unit.

## Tests

`scripts/test-node-runtime-resolution.sh` (run by CI and
`make shell-contract-check`) covers a missing version, a malformed version, a
binary reporting another version, a binary that does not run, an install
without npm, a non-executable, world-writable, or symlinked `node`, and a
half-written upgrade that leaves the previous version resolvable. The
installed-host app lifecycle smoke starts a real unit through the same path.
