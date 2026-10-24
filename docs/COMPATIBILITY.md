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
| v0.6.0 | current source | Tested with a modified fixture | [N-1 upgrade run](lab-results/2026-10-24-n-minus-one-upgrade-40115776818.md): install, upgrade, preserved operator settings, and recovery of the prior candidate after a deliberately broken upgrade. The v0.6.0 fixture was adapted to the current credential and service contract, so an unmodified v0.6.0 host is not yet proven. |
| v0.7.0 | current source | Untested | — |
| `0.8` | current source | Unsupported | Pre-release snapshot (see above). |
| current source | an older version | Refused | See [Downgrades](#downgrades). |

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
downgrade. To return to the previous version:

1. Stop `stepanel`, `stepanel-worker` and `stepanel-root-broker`.
2. Take a copy of the current control-plane database and its `-wal` and
   `-shm` files.
3. Restore the newest `pre-migration-v*` snapshot that predates the upgrade
   over the control-plane database, removing its `-wal` and `-shm` files.
4. Re-run the previous release's `install.sh`.
5. Start the services and check `/readyz`.

Control-plane changes made after the upgrade (jobs, accounts, settings,
audit records) are lost. Sites placed in `/home` after the upgrade cannot be
served by a release older than schema 21; delete or migrate them before
downgrading. Site files, databases and backups themselves are not changed by
the downgrade.

## Downgrades

A binary refuses to open a control-plane database stamped with a schema
version newer than it knows ("schema version N is newer than this binary
supports"), so an older release cannot silently run against migrated state.
Downgrading always means restoring a pre-migration snapshot as above.

## Open before 1.0

- Prove an unmodified v0.7.0 → candidate and v0.6.0 → candidate upgrade.
- Repeat upgrade interruption and rollback on each release candidate.
- Decide the supported upgrade window (for example, N-1 minor releases) and
  publish it here with each release.
