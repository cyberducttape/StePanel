# Filesystem deletion and app-activation regression audit — 2026-10-15

## Scope

Source commit: `bad2c1d849e5d921c1c2d3dfd04f400c40272fc1` (`bad2c1d`).
Toolchain: Go 1.26.7. These are focused repository tests; they do not claim
installed-systemd or physical-host crash certification.

## Results

The following tests passed:

- `TestDeleteRemainsAnchoredWhenSitesPathIsReplaced`: opens the canonical
  sites directory, replaces its pathname with a symlink to an outside
  directory, then proves descriptor-relative deletion removes only the
  intended site under the opened directory and preserves the outside sentinel.
- `TestDeleteRejectsWritableSitesParent`, `TestDeleteRefusesSymlinkedSite`,
  and `TestDeleteHonorsCancelledContext`: verify parent permission checks,
  symlink refusal, and cancellation behavior.
- `TestRecoverAppActivationJournalRestoresRuntimeAndManifest`,
  `TestRecoverAppActivationJournalReconcilesCommittedGeneration`, and
  `TestAppStateActionFailureCompensatesAndCleansJournal`: verify rollback of
  uncommitted state, replay of a committed generation, and compensation on
  returned runtime-action errors.

Commands:

```sh
go test ./internal/sites -run 'Test(DeleteRemainsAnchoredWhenSitesPathIsReplaced|DeleteRejectsWritableSitesParent|DeleteRefusesSymlinkedSite|DeleteHonorsCancelledContext)' -count=1
go test . -run 'Test(RecoverAppActivationJournalRestoresRuntimeAndManifest|RecoverAppActivationJournalReconcilesCommittedGeneration|AppStateActionFailureCompensatesAndCleansJournal)' -count=1
```

Both commands passed.

## Limitations

The deletion test validates path replacement against the open-root API under
the test filesystem; deployment still depends on the documented invariant
that the canonical `sites` parent is not writable by tenants. App journal
tests do not replace crash injection against actual systemd file publication,
daemon reload, restart, and health-check boundaries. Those installed-host
tests remain open in the production gate.
