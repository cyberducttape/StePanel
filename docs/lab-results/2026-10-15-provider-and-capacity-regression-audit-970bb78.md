# Provider, job-admission, and backup-capacity regression audit — 2026-10-15

## Scope

Source commit: `970bb786d3c162ab2e1c92b805ea7fc0128c8f58` (`970bb78`).
Toolchain: Go 1.26.7. This was a focused local regression run against
previously reported provider/job/capacity defects, not a full production gate
or live provider test.

## Results

The following focused test groups passed:

- Site operations without an idempotency key remain distinct jobs:
  `TestDifferentSiteOperationsWithoutIdempotencyKeyAreBothQueued`.
- Cloud provider contracts and DNS validation/reconciliation: tests matching
  `Test(Linode|AWS|OpenStack|CloudOperationKeysAreStableAcrossUTCDateBoundary|CloudRebootWaitRequiresObservedTransition|CloudDNSRecordValidation|DNSRecordExists)`.
  Coverage includes Linode backup scoping and snapshot identity, AWS's
  top-level `SnapshotId` and resumable checkpoint, OpenStack snapshot
  completion, typed MX/SRV fields, node address+port encoding, TTL/record
  parameter matching, and strict A/AAAA family validation.
- Caller idempotency conflict/retry behavior:
  `TestJobsIdempotentRestoreReturnsExistingJob`.
- Scheduled database backup reservation and schedule/outbox transition:
  `Test(DatabaseBackupCapacityAccountsForDumpAndArchiveCopies|DueBackupScheduleAndOutboxCommitBeforeJobAdmission|CreateSiteBackupIncludesManagedDatabaseDump)`.
- Root-broker streamed dump admission and capacity extension:
  `TestBrokerDumpsIntoCallerCreatedFile`.

The exact commands used were:

```sh
go test . -run 'Test(DifferentSiteOperationsWithoutIdempotencyKeyAreBothQueued|Linode|AWS|OpenStack|CloudOperationKeysAreStableAcrossUTCDateBoundary|CloudRebootWaitRequiresObservedTransition|CloudDNSRecordValidation|DNSRecordExists|JobsIdempotentRestoreReturnsExistingJob)' -count=1
go test . -run 'Test(DatabaseBackupCapacityAccountsForDumpAndArchiveCopies|DueBackupScheduleAndOutboxCommitBeforeJobAdmission|CreateSiteBackupIncludesManagedDatabaseDump)' -count=1
go test ./internal/rootbroker -run 'TestBrokerDumpsIntoCallerCreatedFile' -count=1
```

All commands passed on the recorded source commit.

## Limitations

The provider tests use fixtures/fakes; no Linode, AWS, or OpenStack account
was mutated. They establish request/response handling and local durable-state
behavior, not provider availability, account permissions, remote convergence,
or live side-effect reconciliation. The full 20-repeat installed-host matrix,
physical-host power-loss, broader ENOSPC, and capacity certification remain
separate open gates.
