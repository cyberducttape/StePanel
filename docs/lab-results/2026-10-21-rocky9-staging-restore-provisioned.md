# Rocky Linux 9 staging-restore interruption smoke — 2026-10-21

## Scope

Focused installed-host recovery run on a disposable Rocky Linux 9 KVM guest
with SELinux enforcing, Caddy, and MariaDB. The guest had the native systemd
panel, worker, and root-broker services installed. The application binary was
built from the checkout containing the staging-database cleanup fix; this was
not a release build or a full production-certification run.

The smoke used the existing disposable `ci-prov-provision2` fixture and
`deploy/lab/backup-recovery-smoke.sh` with
`DATABASE_RESTORE_RECOVERY=1` and
`STAGING_RESTORE_KILL_AT=restore:provisioned`.

## Results

- The backup worker was killed during archive creation; the durable backup
  completed after worker restart and lease recovery.
- The file-restore worker was killed at `restore:activate`; the durable restore
  completed after recovery.
- The database-restore worker was killed at `restore:database`; the durable
  restore completed after recovery.
- The staging-restore worker was killed after provisioning the target database
  and site. The non-replayable site operation reached `dead-letter`; recovery
  removed the staging database, site tree, route, and Unix account. The source
  site remained intact.
- The termination worker was killed at `terminate:site-state`; termination
  completed after recovery.
- The smoke script exited successfully.

The staging journal now records its temporary database as `panel`-managed, so
transaction recovery invokes `stepanel-dbctl drop-managed`. Previously it
recorded `wordpress`, causing recovery to use the WordPress-only cleanup path;
that helper rejected the generic `panel:` database registration and left
partial resources behind. A focused Go regression test also verifies the
`drop-managed` recovery action for panel-managed databases.

## Limits

This is one focused VM run. It does not close the full interruption matrix,
repeat-count, physical power-loss, broad ENOSPC, cross-distribution, or
production-certification gates. It does not constitute hostile multi-tenant
security validation.
