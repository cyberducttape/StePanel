# Node application lifecycle

The application deployment endpoint creates a per-site manifest and asks the
root-owned `stepanel-appctl` helper to create a restricted systemd unit. The
unit runs `npm start` with the selected NVM version, restarts after failure, and
only writes inside the managed site root.

The root helper takes a per-site lifecycle lock. Slow dependency operations
are run as that site user and do not block other sites. Systemd unit
publication and manager reload use a separate short host-wide lock; service
starts, stops, and restarts happen after that lock is released.

Required deployment fields are:

- site account
- installed Node version
- application port between 1024 and 65535
- optional domain for the proxy configuration

The application must already exist in the site's `public` directory and must
provide a working `npm start` script. The constrained Git endpoint can replace
that public tree with pre-built repository contents, but build pipelines and
environment secret storage remain separate deployment steps. See
[`GIT_DEPLOYMENTS.md`](GIT_DEPLOYMENTS.md).

## Rollback

When a deployment replaces an existing manifest, StePanel saves the prior
release as `<site>.json.bak`. An authenticated operator can roll it back with:

```sh
curl -X POST https://panel.example.test/api/apps/example/rollback \
  -H 'X-CSRF-Token: <session token>'
```

The root-owned app helper applies the previous Node version, root, and port
before the manifest is switched. This rollback changes the process definition;
it does not restore application files, database data, or secrets. Git file
rollback is a separate `/api/sites/git-rollback` operation documented in
[`GIT_DEPLOYMENTS.md`](GIT_DEPLOYMENTS.md).
