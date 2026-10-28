# Installer phases and recovery contract

Every mutating installer run writes an append-only, root-owned journal to
`/var/lib/stepanel/install-journal/<UTC timestamp>-<pid>.tsv` (directory mode
0700, file mode 0600). Each existing path component is checked to be a real,
root-owned directory not writable by group or other users. Records are
tab-separated UTC timestamp, phase, status,
recovery class, and a bounded detail label. They contain no credentials. Keep
the journal with the host's installation and incident records.

After preflight and operator confirmation, the installer acquires the
nonblocking host-wide lock `/run/stepanel-installer/installer.lock` before its
first package or service mutation. The directory is root-owned mode 0700 and
symlink paths are rejected; the lock file is opened without truncation. This
avoids placing a root-opened lock file in the commonly world-writable sticky
`/run/lock` directory. The lock remains held through optional post-install
activation and is released automatically when the installer process exits. A
concurrent install or upgrade exits with status 75 without starting host
mutations.

The journal describes progress; it is not a snapshot of the whole host and it
does not make every host change reversible.

| Phase | What is recorded | Failure/recovery contract |
| --- | --- | --- |
| Inventory / preflight | OS ID/version, selected package manager/web server/database, installed versions of the selected web/database/PHP-FPM packages before and after package installation, detected web-service states, takeover and optional integration choices | Read-only. The journal stores a concise inventory, not a complete package/repository manifest or copies of arbitrary host configuration. |
| Dependency installation | Start and completion of base and optional package installation | Package installs, repository metadata, and enabled distribution modules are retained. The installer does not uninstall packages or reverse repository/module changes. |
| Configuration | ModSecurity, mail/FTP setup, per-file snapshot checkpoints, system account and SELinux configuration | Configuration completed before or during the transaction may remain after failure. Installer-created account/group membership, PostgreSQL initialization, SELinux labels/booleans, recursive ownership changes, and existing-site ACL migration are not reversed. Per-file checkpoints identify exactly which managed files were snapshotted. |
| Activation | Web-service takeover/normalization, database/PHP-FPM, legacy mount retirement, Caddy trust, and StePanel service activation | A handled pre-commit failure restores registered StePanel-owned files, the control-plane SQLite file set, and the captured active/enabled state of recognized web services, optional mail services, and the three StePanel units where restoration succeeds. Failures before the managed-file transaction also attempt to restore captured web/mail service state. A failed service-state restoration is named in the journal and requires operator action. This does not restore arbitrary foreign configuration, package post-install effects, database/PHP-FPM service state, or an unmounted legacy `/var/www` bind. |
| Verification | Core `/readyz` check and optional integration outcome | Core readiness is the commit gate. Before commit, the journal records the planned post-commit actions. An optional action runs only after its `started` record is durable; if that record cannot be written, the action is not attempted and remains pending. Once started, the action records `complete` or `pending`; an interruption between those records means the actual service state must be checked. The final status is printed as `post_install_status`. |
| Commit | Successful core installation | The installer attempts to discard temporary snapshots and the private transaction directory after commit. Cleanup failures are warnings with `post_install_cleanup_status=pending` and the retained paths; they do not misreport a committed installation as rolled back. Post-commit optional integrations have their own retry instructions and are not covered by the core rollback. The pre-commit action plan makes an interruption after commit visible even if no optional action has started yet. |
| Recovery | Failed/partial or completed recovery-contract record | `required/manual` means operators must inspect the host. A journal ending before `commit` is not evidence that all changes were reverted. |

## On failure

1. Read the newest journal and identify the last completed phase and any
   `recovery required` record.
2. Check package-manager output/history and repository/module state. Do not
   blindly remove packages: they may be shared with pre-existing workloads.
3. Check web server, database, PHP-FPM, mail, FTP, security services, systemd
   units, and any existing host configuration mentioned by the phase details.
4. For failures after the file transaction begins, inspect the installer
   transaction diagnostic output and any `*.failed-upgrade-*` database files
   before restarting services. Preserve these artifacts until the previous
   release and database have been verified.
5. Resolve any pending optional service using the retry command printed by the
   installer, then verify `/readyz` and the actual customer-facing services.

This contract covers handled installer errors. It does not claim recovery from
power loss, package-manager interruption, arbitrary external host changes, or
failure of the recovery actions themselves. Such cases require operator
inspection; the journal is intentionally retained for that purpose.

The following host changes are intentionally outside automatic rollback and
are explicitly journaled when their phase is reached:

- APT/DNF package, repository metadata, and PostgreSQL module changes.
- PostgreSQL data-directory initialization.
- Package-triggered service changes outside the recognized web and optional
  mail services' active/enabled states restored by pre-commit recovery.
- Creation or modification of the `stepanel` system account and group membership.
- Recursive application/data ownership changes and ACL repair across existing
  site trees.
- Persistent SELinux file labels and booleans.
- Retirement of the old `stepanel-nosymfollow` unit and possible `/var/www`
  unmount. A failed install does not recreate that legacy mount.
- Caddy local CA installation into the machine trust store.

Do not reverse these mechanically without checking for pre-existing workloads
and data. The journal's `manual` recovery class is an explicit operator-review
boundary, not a claim that the effect is automatically undone.

For a handled pre-commit failure, the installer attempts to restore the
active/enabled state recorded for recognized web and optional mail services.
This restores service availability when possible; it does not reconstruct
arbitrary third-party virtual-host files or package-manager changes. Confirm
the reported state in systemd and verify each existing site's HTTP response
before closing the incident.
