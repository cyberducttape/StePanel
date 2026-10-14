# Installer phases and recovery contract

Every mutating installer run writes an append-only, root-owned journal to
`/var/lib/stepanel/install-journal/<UTC timestamp>-<pid>.tsv` (directory mode
0700, file mode 0600). Records are tab-separated UTC timestamp, phase, status,
recovery class, and a bounded detail label. They contain no credentials. Keep
the journal with the host's installation and incident records.

The journal describes progress; it is not a snapshot of the whole host and it
does not make every host change reversible.

| Phase | What is recorded | Failure/recovery contract |
| --- | --- | --- |
| Inventory / preflight | OS ID/version, selected package manager/web server/database, installed versions of the selected web/database/PHP-FPM packages before and after package installation, detected web-service states, takeover and optional integration choices | Read-only. The journal stores a concise inventory, not a complete package/repository manifest or copies of arbitrary host configuration. |
| Dependency installation | Start and completion of base and optional package installation | Package installs, repository metadata, and enabled distribution modules are retained. The installer does not uninstall packages or reverse repository/module changes. |
| Configuration | ModSecurity and start of the registered-file transaction | Configuration completed before the transaction, including ModSecurity, mail/FTP, host takeover, and database/PHP service setup, may remain after a later failure. Components with their own validation rollback report that locally; otherwise inspect and restore them manually. |
| Activation | Core web/database/PHP-FPM and StePanel service activation | A handled failure after the transaction starts restores registered StePanel-owned files, the control-plane SQLite file set, and the prior enabled/active state of the three StePanel units where restoration succeeds. It does not restore every host service or package side effect. |
| Verification | Core `/readyz` check and optional integration outcome | Core readiness is the commit gate. Optional integrations may remain pending after core commit and are printed as `post_install_status`. |
| Commit | Successful core installation | The install transaction is discarded after commit. Post-commit optional integrations have their own retry instructions and are not covered by the core rollback. |
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
