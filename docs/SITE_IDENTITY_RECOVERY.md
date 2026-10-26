# Offline site identity collision recovery

StePanel blocks startup if two existing site roots resolve to the same legacy
Unix account. Do not resolve this by editing `site_identities` alone: the host
account, files, PHP-FPM pool, SSH policy, and quotas must converge with the
database mapping.

The `stepanel-root identity-repair` maintenance command performs a forward-only
repair. It first records a private SQLite snapshot and a root-owned journal,
commits unique per-site mappings together, then idempotently reconciles and
verifies each site. If interrupted, leave services stopped and rerun `apply`;
the command resumes from its durable journal. It deliberately does not roll
back a committed database mapping to a shared account.

## Preconditions

- Use a maintenance or rescue boot where StePanel, the root broker, web
  servers, PHP-FPM, SSH/SFTP, FTPS, cron, and StePanel site/task services are
  stopped. The command checks active managed services and processes owned by
  legacy site accounts before changing anything.
- Take an independent host/filesystem backup. The command also creates a
  consistent SQLite snapshot under `/var/lib/stepanel/recovery`; the snapshot
  alone does not restore filesystem ownership or service configuration.
- Reconcile pending SSH/SFTP, resource, or quota state before proceeding.
  Every affected site must have a valid durable resource profile. The repair
  refuses hard-linked site files because recursive ownership changes could
  otherwise affect paths outside the site being repaired.
- FTPS credentials cannot be transferred from the colliding Unix account.
  Inspect the plan and use `--revoke-ftps` only when you accept revoking those
  credentials. Customers must receive new FTPS credentials after recovery.

## Procedure

Run the plan first and review every site/account assignment:

```sh
sudo /usr/local/sbin/stepanel-root identity-repair plan \
  --webroot /var/www \
  --control-plane-db /var/lib/ste-panel/stepanel-control.db
```

Apply the reviewed plan. The command prints the snapshot location and persists
its journal at `/var/lib/stepanel/recovery/site-identity-repair.json`:

```sh
sudo /usr/local/sbin/stepanel-root identity-repair apply \
  --webroot /var/www \
  --control-plane-db /var/lib/ste-panel/stepanel-control.db \
  --recovery-root /var/lib/stepanel/recovery \
  --state-dir /var/lib/ste-panel
```

If the plan reports that FTPS is configured and its revocation is approved,
repeat with `--revoke-ftps`. The same `apply` command resumes an incomplete
journal; do not remove or edit that journal. It records the mapping commit,
per-site host verification, and finalization. Filesystem ownership changes
are forward-only, so restoring only the SQLite snapshot after host work has
started can recreate the original mismatch.

Do not restart services until the command reports the journal as completed.
Then review the journal and logs, start the supported host services, verify
site HTTP/PHP and database checks, confirm file/inode quotas and access
revocation, and reset any revoked customer FTPS credentials. If reconciliation
fails, keep services stopped and rerun `apply`; unresolved host conditions are
reported instead of being marked complete.

The old shared account's quota is retained for operator review after the site
trees move to their new accounts. Confirm that it owns no remaining data or
services before manually removing the account or quota; the repair never
deletes a Linux account or silently clears a limit outside the affected trees.

The maintenance command only repairs identity collisions that prevent startup.
It does not certify tenant isolation, recover missing site data, or replace a
full-host recovery drill.
