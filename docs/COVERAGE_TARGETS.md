# Critical coverage targets

Coverage is enforced as a ratchet, not as a proxy for correctness. The
repository-wide floor is currently 50%, with a 48% floor for the legacy root
package. Critical packages already have higher executable floors in
`scripts/check-coverage-targets.sh`.

The next staged targets are deliberately tied to failure-prone state machines:

| Area | Current enforcement | Target |
| --- | ---: | ---: |
| Root broker / privilege validation | 65% | 90% |
| Backup publication and restore | root package floor | 85% |
| Site create/delete lifecycle | not isolated | 85% |
| Auth/session/account security | 90%+ | 90% |
| Archive extraction | importer 55% | 90% |
| Durable job transitions | jobs 85% | 90% |
| Resource locks/fencing | operations 80% | 90% |

Targets are raised only when the corresponding failure-path tests land. The
real-host recovery matrix is independently repeatable with
`RECOVERY_MATRIX_REPEATS=20` through `50`; coverage numbers do not substitute
for SIGKILL, ENOSPC, database-outage, broker-kill, reboot, or power-loss
evidence.
