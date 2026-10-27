# Version compatibility and rollback policy

This page states which StePanel versions can be upgraded to the current
source, what each version stores, and how an upgrade is rolled back. It only
claims paths that have recorded evidence; everything else is untested.

## Versions, state and schema

| Version | Published | Control-plane state | Schema version | Site layout |
| --- | --- | --- | --- | --- |
| v0.6.0 | GitHub release, 2026-09-05 | JSON state files | none | `/var/www/sites/SITE/public` |
| v0.7.0 | GitHub release (latest), 2026-09-15 | JSON state files | none | `/var/www/sites/SITE/public` |
| `0.8` | GitHub pre-release named "updates", 2026-09-22 | SQLite control plane | 4 | `/var/www/sites/SITE/public` |
| Unreleased (`main`) | not released; `version.go` still reports 0.7.0 | SQLite control plane | 21 | New sites in hosting accounts: `/home/USER/public_html` or `/home/USER/DOMAIN` ([ADR-0007](adr/0007-home-account-layout.md)); existing legacy sites keep `/var/www/sites` |

The `0.8` tag lacks the `v` prefix used by every other release and has no
[CHANGELOG](../CHANGELOG.md) entry. Treat it as a pre-release snapshot, not a
supported upgrade source, and give the next release a new version so it is
not confused with it.

## Supported upgrade paths

| From | To | Status | Evidence |
| --- | --- | --- | --- |
| v0.6.0 | current source | Tested | [Upgrade matrix run](lab-results/2026-10-27-upgrade-matrix-40452352170.md): upgrade, an upgrade killed before commit and completed by a rerun, installer rollback of a broken candidate, the documented downgrade back to v0.6.0, and a repeat upgrade, on an Ubuntu 24.04 QEMU guest (Apache) and in a systemd container (Caddy). The release is adapted only as far as it needs to install at all (see below). |
| v0.7.0 | current source | Tested | Same run and checks as v0.6.0. |
| `0.8` | current source | Unsupported | Pre-release snapshot (see above). |
| current source | v0.6.0, v0.7.0 | Tested, by the documented procedure only | Same run: the procedure in [Rollback](#rollback) restored each release, which then served `/readyz` and verified the audit log. A binary never opens a newer schema on its own (see [Downgrades](#downgrades)). |

v0.6.0 and v0.7.0 cannot complete a fresh install as published on a current
Ubuntu 24.04 host: their local database helper runs through `sudo`, which
their own service sandbox refuses, and with Caddy the PHP packages they
install start `apache2` on port 80 first. The upgrade tests therefore drop
only their `STEPANEL_DBCTL` entry and use Apache on the QEMU guest; the
systemd `LoadCredential` credential handoff the upgrade migrates is as
published ([fixture](../deploy/lab/upgrade-fixture.sh)). The upgrade itself
does not depend on either defect.

Upgrades are in place: re-run `install.sh` from the new source tree. The
installer keeps the explicitly listed operator runtime settings in
`/etc/ste-panel.env` and rewrites installer-owned settings
([INSTALLATION.md](INSTALLATION.md)).

## What an upgrade changes

- **Control-plane schema.** On first start, the new binary applies every
  migration newer than the database's recorded version, each in its own
  transaction, under a migration lock shared by the panel and worker. Before
  migrating an already-populated database it writes a snapshot
  `pre-migration-vN-TIMESTAMP.db` into the private (0700) directory
  `STEPANEL_CONTROL_PLANE_DB.snapshots`, keeping the newest three.
- **Hosting accounts (schema 21).** Existing sites stay in `/var/www/sites`
  and keep working. Sites created, imported or restored from cPanel after the
  upgrade are placed in `/home`. Production requires user quotas on the
  filesystem holding `/home` as well as `/var/www`.
- **Privileged helpers.** Helpers and their shared libraries in
  `/usr/local/sbin` and `/usr/local/lib/stepanel` are replaced together; a
  helper refuses to run if a library it sources is missing or unsafe.

## Rollback

There are two cases.

**The upgrade fails before it commits.** The installer restores the files it
registered (binaries, helpers, units, configuration), the control-plane
SQLite file set and the previous state of the StePanel units, as described in
the [installer recovery contract](INSTALLER_RECOVERY.md). Package,
repository, account and SELinux changes are not reversed. Read the install
journal under `/var/lib/stepanel/install-journal/` before restarting
anything.

**The upgrade committed and must be undone.** There is no automatic
downgrade. Before an upgrade changes anything, the installer keeps a copy of
the replaced release's `/etc/ste-panel.env` in
`/var/lib/stepanel/upgrade-rollback/` (root-only; the newest five are kept)
and prints `Kept the replaced release's configuration in …`. An older
installer may refuse the newer environment file (v0.6.0 and v0.7.0 reject the
`RCLONE_CONFIG` entry the current installer writes), so the downgrade restores
that copy. To return to the previous version:

1. Find the copy made when the installed release replaced the previous one:

   ```sh
   grep -lFx "replaced_by_sha256=$(sha256sum /opt/stepanel/stepanel | cut -d' ' -f1)" \
     /var/lib/stepanel/upgrade-rollback/*/upgrade | sort | tail -n 1
   ```

   Its directory holds `ste-panel.env`. After an interrupted upgrade this is
   still the copy of the previous release's file, made by the first attempt.
2. Stop `stepanel`, `stepanel-worker` and `stepanel-root-broker`.
3. Take a copy of the current control-plane database and its `-wal` and
   `-shm` files.
4. Restore the kept `ste-panel.env` to `/etc/ste-panel.env` (owner root,
   mode 0600).
5. Restore the newest `pre-migration-v*` snapshot from
   `STEPANEL_CONTROL_PLANE_DB.snapshots` over the control-plane database and
   remove its `-wal` and `-shm` files. A release from before the SQLite
   control plane (v0.6.0, v0.7.0) ignores the database and reads its own
   state files, which the upgrade leaves in place.
6. If the previous release predates the root broker (v0.6.0, v0.7.0), run
   `systemctl disable stepanel-root-broker.service` so it does not start at
   the next boot.
7. Re-run the previous release's `install.sh`.
8. Check `/readyz` and verify the audit log with the previous release's
   `stepanel verify-audit`.

Control-plane changes made after the upgrade (jobs, accounts, settings,
audit records) are lost. Sites placed in `/home` after the upgrade cannot be
served by a release older than schema 21; delete or migrate them before
downgrading. Site files, databases and backups themselves are not changed by
the downgrade. A host rolled back this way can be upgraded again; if it is
upgraded to the same release, the copy from the first upgrade is reused, so
settings changed under the older release in between must be reapplied.

## Downgrades

A binary refuses to open a control-plane database stamped with a schema
version newer than it knows ("schema version N is newer than this binary
supports"), so an older release cannot silently run against migrated state.
Downgrading always means restoring a pre-migration snapshot as above.

## Open before 1.0

- Run the upgrade matrix on each release candidate, and extend it to
  AlmaLinux and Rocky guests, other interruption points, and a previous
  release with populated sites, databases and backups.
- Decide the supported upgrade window (for example, N-1 minor releases) and
  publish it here with each release.
