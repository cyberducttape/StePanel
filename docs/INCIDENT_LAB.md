# StePanel incident lab

This is the operational proof plan for StePanel's migration and recovery paths.
Each scenario can be reproduced on an isolated disposable VM with a synthetic
backup. Never test restore failure modes against customer data.

## Scenarios

### 1. Database restore failure

Inject an invalid SQL dump or stop the database service before import.

Expected result: the job transitions to `failed`, the audit log records the
failure, the failed counter increases, and the operator receives a clear
recovery instruction. The original site must remain intact.

### 2. Disk exhaustion during staging

Constrain the import filesystem and upload a backup larger than the available
space.

Expected result: the upload fails safely, temporary files are removed, no
partial archive is extracted into the target site, and the operator can retry
after freeing space.

### 3. Interrupted restore

Terminate the process during extraction and restart it.

Expected result: the incomplete job is visible in logs, the target is marked
for operator review, and a documented snapshot/rollback procedure is used
before retrying. A restore is never described as successful without a health
check.

## Incident workflow

1. Record impact, start time, affected site, and the job ID.
2. Preserve logs and the archive checksum before changing state.
3. Stop repeated retries if the same failure occurs twice.
4. Restore the last known-good snapshot or route traffic to the previous site.
5. Verify HTTP, PHP, database connectivity, permissions, and scheduled jobs.
6. Write a short postmortem with detection, timeline, root cause, and one
   prevention item.

## Evidence template

Completed drills should add a dated record under `docs/lab-results/` using this
format. Do not claim a scenario passed until the result includes the command,
observed state, and recovery evidence.

```text
Scenario:                    DB unavailable during restore
Environment:                 disposable OS/image and StePanel revision
Failure injection:           exact command or controlled fault
Expected result:             failed job, intact source, recovery guidance
Observed result:              concise outcome and relevant log identifiers
Recovery verification:       readiness, audit, database, and filesystem checks
Bug found:                   yes/no; issue or fixing commit if applicable
```

The repository intentionally does not include fabricated pass/fail results.
Capture real disposable-lab evidence before using the results as release or
portfolio claims.

For the repository-level gate, run `make production-gate`. For a disposable
systemd host, set `RECOVERY_MATRIX_FULL=1` when invoking
`deploy/lab/recovery-matrix-smoke.sh` to execute every supported journal kill
boundary instead of the bounded default matrix. The full mode is intentionally
not part of every install smoke because it repeats several multi-minute
restore/backup workflows; its output and host logs belong in the dated lab
evidence record.

Repository-level recovery test evidence is recorded in
[`docs/lab-results/2026-09-06-recovery-unit-drills.md`](lab-results/2026-09-06-recovery-unit-drills.md).
The repeatable capture command is
[`deploy/lab/run-recovery-drills.sh`](../deploy/lab/run-recovery-drills.sh).
