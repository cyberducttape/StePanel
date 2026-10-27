# StePanel v1.0.0 Production Readiness Gates

**Status:** CURRENT AUTHORITATIVE RELEASE-GATE DOCUMENT
**Last Updated:** 2026-10-27
**Target:** Ready to run 100+ production WordPress/PHP customer sites  
**Approach:** Complete existing architectural contracts, add robustness testing

All release approval decisions must use this document. `PRODUCTION_READINESS.md`
is a deployment-status summary; `archive/PRODUCTION_SCORECARD.md` (archived) and prior audits are
historical and must not be treated as current approval.

### Current evidence snapshot — 2026-10-27 02:45 UTC (`main` `0cbfed7`; application source `3bb1e6b`)

The current main commit is
[`0cbfed72ab5c2c13e58d04869cd32237e559db75`](https://github.com/cyberducttape/StePanel/commit/0cbfed72ab5c2c13e58d04869cd32237e559db75),
a documentation-only rebase of PR #64; application source remains
[`3bb1e6bf2ff873def292ab2d406d5ceebd7b96f7`](https://github.com/cyberducttape/StePanel/commit/3bb1e6bf2ff873def292ab2d406d5ceebd7b96f7).
PR-head CI, Go analysis, CodeQL, and documentation checks passed on
[`8b25b41`](https://github.com/cyberducttape/StePanel/commit/8b25b41f8706bdf9b2b73d2b4aa5ebfd98bf816d)
before merge ([PR #64](https://github.com/cyberducttape/StePanel/pull/64)).
Main's post-merge Documentation ([40439683192](https://github.com/cyberducttape/StePanel/actions/runs/40439683192))
and CodeQL ([40439683184](https://github.com/cyberducttape/StePanel/actions/runs/40439683184))
passed.

The exact-source AlmaLinux 9/Caddy/MariaDB installed smoke
([40435936293](https://github.com/cyberducttape/StePanel/actions/runs/40435936293))
completed successfully on application source `3bb1e6b`. This is one installed
host combination and one recovery-matrix iteration. The same-source 20-repeat
cohort ([40436448192](https://github.com/cyberducttape/StePanel/actions/runs/40436448192))
is still in progress; its first six-host shard has not completed, so no cohort
repetitions count yet.

Production remains **NO-GO**. Physical host power-loss and broader
ENOSPC/database-outage evidence, quota certification across supported
filesystems, live notification outage/retry proof, measured capacity,
upgrade/rollback policy, and independent security review remain open.

### Previous evidence snapshot — 2026-10-27 02:22 UTC (`main` source `3bb1e6b`)

Current application source is
[`3bb1e6bf2ff873def292ab2d406d5ceebd7b96f7`](https://github.com/cyberducttape/StePanel/commit/3bb1e6bf2ff873def292ab2d406d5ceebd7b96f7),
fast-forwarded from PR #63. Its exact-source CI
([40433445780](https://github.com/cyberducttape/StePanel/actions/runs/40433445780)),
CodeQL/Go analysis
([40433445654](https://github.com/cyberducttape/StePanel/actions/runs/40433445654)),
and Documentation
([40433445610](https://github.com/cyberducttape/StePanel/actions/runs/40433445610))
passed before merge. Push-triggered CodeQL
([40435936389](https://github.com/cyberducttape/StePanel/actions/runs/40435936389))
and Documentation
([40435936306](https://github.com/cyberducttape/StePanel/actions/runs/40435936306))
passed. Push CI integration
([40435936337](https://github.com/cyberducttape/StePanel/actions/runs/40435936337))
also passed. The push-triggered AlmaLinux 9/Caddy install smoke
([40435936293](https://github.com/cyberducttape/StePanel/actions/runs/40435936293))
was still in progress at this snapshot.

The six-host installed recovery smoke
([40433464444](https://github.com/cyberducttape/StePanel/actions/runs/40433464444))
passed on Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with Apache and Caddy
plus MariaDB. Every host completed one recovery-matrix iteration, restored a
populated WordPress site with HTTP proof, and repaired both sites in the
legacy identity collision fixture. Measured recovery RTO ranged from 5.419 to
16.400 seconds. These were hosted disposable systemd containers, not dedicated
VMs or physical power-loss tests. See the
[six-host evidence report](lab-results/2026-10-27-six-host-identity-repair-40433464444.md).

The same-source 20-repeat Gate 5 cohort
([40436448192](https://github.com/cyberducttape/StePanel/actions/runs/40436448192))
was in progress, with its first six-host shard running; no repetitions count
until the full cohort completes and each host result is reviewed. This
six-host smoke is not a substitute for that gate.
Production remains **NO-GO**. Physical host power-loss and broader
ENOSPC/database-outage evidence, quota certification across supported
filesystems, live notification outage/retry proof, measured capacity,
upgrade/rollback policy, and independent security review remain open.

### Previous evidence snapshot — 2026-10-27 (`main` source `04646a2`)

The prior snapshot is superseded for installed identity-repair evidence by the
`3bb1e6b` six-host result above. Earlier source-specific checks and limitations
remain in the historical snapshots below.

### Previous evidence snapshot — 2026-10-26 (`main` source `ebeca0a`)

PR #49 was fast-forwarded to `main` as
[`ebeca0a1622571409e605213cb2662a791cb0275`](https://github.com/cyberducttape/StePanel/commit/ebeca0a1622571409e605213cb2662a791cb0275).
Its exact-main CI integration, CodeQL, and Documentation checks were pending
at the time of the snapshot. The quota install run
([40262294553](https://github.com/cyberducttape/StePanel/actions/runs/40262294553))
was still running then. This older snapshot is retained as history; its pending
checks are superseded by the exact-head results above.

Production remains **NO-GO**. Open gates include legacy site-identity collision
recovery, the required 20–50-repeat installed-host recovery cohort, physical
host power-loss and broader ENOSPC/database-outage evidence, quota certification
across supported filesystems, live notification outage/retry proof, a measured
capacity envelope, upgrade/rollback policy, and independent security review.

### Previous evidence snapshot — 2026-10-26 (main source `c071169`)

Exact-head Integration passed on
[`c071169a0ff4c2f7ce16b412ddd3b60d2fa714fe`](https://github.com/cyberducttape/StePanel/commit/c071169a0ff4c2f7ce16b412ddd3b60d2fa714fe)
([run 40253133749](https://github.com/cyberducttape/StePanel/actions/runs/40253133749));
Documentation ([40253133877](https://github.com/cyberducttape/StePanel/actions/runs/40253133877))
and CodeQL ([40253134087](https://github.com/cyberducttape/StePanel/actions/runs/40253134087))
also passed on that source. The code-scanning API reports zero open alerts.
The exact-source AlmaLinux 9/Caddy install smoke
([40253133851](https://github.com/cyberducttape/StePanel/actions/runs/40253133851))
is still running and is not counted as passed.

The placement-aware account changes have full CI, CodeQL, and documentation
passes on PR head `aba64d1` ([CI](https://github.com/cyberducttape/StePanel/actions/runs/40249722389),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/40249722037),
[docs](https://github.com/cyberducttape/StePanel/actions/runs/40249721919)). A
diagnostic ext4 quota-VM run is active on source `d1e9abf`
([40249633583](https://github.com/cyberducttape/StePanel/actions/runs/40249633583));
it is not exact-source evidence for `c071169` until it completes and is reviewed.

The earlier XFS quota run hit its install-smoke deadline before later checks
([40212633884](https://github.com/cyberducttape/StePanel/actions/runs/40212633884));
the earlier ext4 run failed when `runuser` could not switch to the placed account
([40216152671](https://github.com/cyberducttape/StePanel/actions/runs/40216152671)).
Neither quota run passes certification.

Two full-matrix shards dispatched on `2c6546e` were based on `dd6a65f`, behind
current main by changes to 48 files including the root broker and control plane.
Both were canceled and count as zero current-source repetitions. The recovery
harness requires one exhaustive iteration per unique shard and allows up to
350 minutes within the six-hour hosted job limit. The harness has been rebased
onto `c071169`; new current-source runs must complete before counting any
recovery iterations.

The six-host installed-system smoke on older source `070dce9`
([40176491637](https://github.com/cyberducttape/StePanel/actions/runs/40176491637))
passed Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with Apache and Caddy plus
MariaDB. All six passed installed WordPress recovery proof; measured RTO ranged
from 4.109 to 6.479 seconds. Each completed one iteration in a disposable
systemd container, not a dedicated VM. See
[the run report](lab-results/2026-10-25-six-host-installed-smoke-40176491637.md).

The 20–50-repeat real-host recovery cohort, physical host power-loss and broad
ENOSPC/database-outage tests, certified capacity, live alert outage/retry
evidence, upgrade/rollback policy, and independent security assessment remain
open. Production remains **NO-GO**.

### Previous evidence snapshot — 2026-10-25 (application source `070dce9`; CI run `40206286397`; six-host report `lab-results/2026-10-25-six-host-installed-smoke-40176491637.md`)

The application source under test is
[`070dce9eeee18dd075596c2fe0561112d45db2e5`](https://github.com/cyberducttape/StePanel/commit/070dce9eeee18dd075596c2fe0561112d45db2e5).
The exact-head Integration workflow passed on `e56134e`
([run 40206286397](https://github.com/cyberducttape/StePanel/actions/runs/40206286397)),
including Go 1.26.10 tests/coverage, race tests, recovery and real tmpfs ENOSPC
drills, build/startup/authenticated API checks, mixed HTTP load, MySQL 8 and
PostgreSQL restore checks, clean Python deployment, vulnerability/static
analysis, UI E2E/accessibility, and container/deployment validation.
Documentation and CodeQL passed on evidence commit `94244bd` ([runs
40206913087](https://github.com/cyberducttape/StePanel/actions/runs/40206913087)
and [40206913097](https://github.com/cyberducttape/StePanel/actions/runs/40206913097));
the code-scanning API reports zero open alerts. The six-host installed-system
smoke ([run
40176491637](https://github.com/cyberducttape/StePanel/actions/runs/40176491637))
passed on Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9, each with Apache and
Caddy plus MariaDB. All six hosts passed the WordPress application restore and
HTTP proof; measured RTO ranged from 4.109 to 6.479 seconds. Each host also
completed one recovery-matrix iteration. These were disposable systemd
containers on hosted runners, not dedicated VMs or physical hosts. The ten
older two-iteration runs on `e3a8976` were cancelled after partial execution;
each had one host job succeed and three fail, with the other jobs cancelled.
Those stale-source results are not counted as current-source certification.

The subsequent full Integration run on `c648995`
([40206761607](https://github.com/cyberducttape/StePanel/actions/runs/40206761607))
passed, including the full Go suite, coverage thresholds, race and recovery
drills, real ENOSPC checks, database restores, and UI E2E. Integration on
`1e44993` passed those stages but stopped at the quota-fixture syntax guard,
which parsed raw heredoc text rather than rendered guest shell. The validator
now models heredoc escaping and passes locally. Exact-head Integration on
`73975a3` passed ([40210745823](https://github.com/cyberducttape/StePanel/actions/runs/40210745823)), including Go tests and coverage, race and recovery drills, real ENOSPC, MySQL/PostgreSQL restores, security/static scans, container/deployment validation, and UI E2E. Documentation passed ([40210745830](https://github.com/cyberducttape/StePanel/actions/runs/40210745830)); CodeQL passed ([40210745764](https://github.com/cyberducttape/StePanel/actions/runs/40210745764)) with zero open alerts.


The two-repeat AlmaLinux 9 + Caddy + MariaDB recovery run
([40196578576](https://github.com/cyberducttape/StePanel/actions/runs/40196578576))
remains in progress. The first quota-enabled Ubuntu 24.04 VM run
([40205929003](https://github.com/cyberducttape/StePanel/actions/runs/40205929003))
failed without enough retained output to identify the failing phase. The
instrumented rerun ([40206770167](https://github.com/cyberducttape/StePanel/actions/runs/40206770167))
failed in quota preflight; its guest diagnostics confirm ext4 was mounted with
`usrquota,prjquota`, and both quota types were active. The `quotaon -p`
pipeline treated its nonzero status as failure despite enabled-state output.
Two workflow reruns then exposed script errors: an
unescaped guest variable failed before QEMU started
([40208701493](https://github.com/cyberducttape/StePanel/actions/runs/40208701493)),
and Bash here-string syntax failed under cloud-init's `/bin/sh`
([40208981197](https://github.com/cyberducttape/StePanel/actions/runs/40208981197)).
A rendered-script check then found unescaped quota command substitutions
that the host shell would evaluate. The queued run
([40210073978](https://github.com/cyberducttape/StePanel/actions/runs/40210073978))
was canceled before execution; the substitutions are now escaped and will be
validated by rendering the cloud-init script and running `/bin/sh -n` before
the next guest. No quota-VM attempt in this sequence counts as a pass.

The corrected quota-enabled Ubuntu guest run on `2680808` is active
([40210323828](https://github.com/cyberducttape/StePanel/actions/runs/40210323828)); it has passed cloud-init startup and has not completed installation.

This is current-source evidence across the six listed OS/web-server
combinations. It does not satisfy the 20–50-repeat recovery cohort, physical
host power-loss or broad ENOSPC/database-outage testing, live alert outage/retry
evidence, certified capacity, upgrade/rollback policy, or independent security
assessment. Production remains **NO-GO**.

### Previous completed validation — 2026-10-24 (application `b20c609`, Rocky 9 KVM)

One installed-host recovery-matrix iteration passed on Rocky Linux 9.8 with
enforcing SELinux, quota-enabled ext4, Caddy, PHP-FPM, and MariaDB. Follow-up
QEMU `SIGKILL` scenarios passed for backup, file restore, database restore,
Git activation, and control-plane writes. Backup and file restore also passed
real ENOSPC checks on a dedicated 96 MiB ext4 volume. The complete scope,
environment, timings, run artifacts, and limitations are recorded in
[the 2026-10-24 Rocky KVM evidence report](lab-results/2026-10-24-rocky9-kvm-full-recovery.md).

The Git activation replay exposed that the root broker's systemd mount
namespace can present an ext4 filesystem through a different bind-mount alias.
Quota verification and Git staging limit operations now use the backing
filesystem device. The quota contract test and a targeted broker-namespace
Git interruption/redeployment passed. Exact-head CI for these helper changes
is pending.

This remains one Rocky Linux 9/Caddy/MariaDB repetition. It does not satisfy
the required 20–50-repeat cohort, test physical host power loss, or establish
cross-platform recovery. Production remains **NO-GO**.

### Latest application-source validation — 2026-10-24 (`b20c609`)

The application source under validation is
[`b20c60989cdeabf04b796038eafd0e70ef263392`](https://github.com/cyberducttape/StePanel/commit/b20c60989cdeabf04b796038eafd0e70ef263392).
It fixes PHP import rewriting for environment-backed WordPress credentials and
keeps the installed recovery fixture's generated database username within the
supported 32-character limit. Exact-source Integration CI
([run 40089780650](https://github.com/cyberducttape/StePanel/actions/runs/40089780650)),
Documentation ([run 40089780692](https://github.com/cyberducttape/StePanel/actions/runs/40089780692)),
and CodeQL ([run 40089780673](https://github.com/cyberducttape/StePanel/actions/runs/40089780673))
all succeeded; CodeQL reports zero open alerts.
The current evidence snapshot also passed Documentation and CodeQL
([runs 40100469312](https://github.com/cyberducttape/StePanel/actions/runs/40100469312)
and [40100469296](https://github.com/cyberducttape/StePanel/actions/runs/40100469296)).

The clean exact-source Rocky Linux 9 KVM installation and one full opt-in
recovery-matrix iteration completed. The run also passed installed WordPress
HTTP recovery proof, MariaDB outage recovery, broker-outage readiness behavior,
deploy rollback, abrupt-loss scenarios, and the bounded ENOSPC cases listed in
the evidence report. It is one Rocky Linux 9 iteration and does not establish
the required 20–50 repeats, physical host power-loss behavior, or
cross-platform certification.

Production remains **NO-GO**. Required repeated installed-host recovery,
physical power-loss and broader host ENOSPC/database-outage evidence,
application-level WordPress restore proof across supported web servers,
capacity certification, live alert outage/retry evidence, upgrade/rollback
policy, and independent security assessment remain open.

### Previous pushed-source validation — 2026-10-24 (`215c50c`)

Current `main` is [`215c50ca53ecaca0e26f893c13b4ea4c762ff58e`](https://github.com/cyberducttape/StePanel/commit/215c50ca53ecaca0e26f893c13b4ea4c762ff58e).
The commit increases the installed-host smoke budget after two Ubuntu
installations reached the former 80-minute timeout while provisioning packages,
and adds timestamped installer phase progress. Local focused timeout tests,
the complete root-package test suite, installer and smoke-script syntax checks,
the workflow-bound check, and `git diff --check` passed.

Exact-source integration CI passed in full
([run 40086080662](https://github.com/cyberducttape/StePanel/actions/runs/40086080662)):
Go 1.26.10 tests/coverage, full race suite, recovery and real tmpfs ENOSPC
drills, MySQL and PostgreSQL restore checks, clean Python deployment, static
and vulnerability analysis, container/deployment validation, mixed load, and
UI E2E/accessibility. Documentation ([run 40086080677](https://github.com/cyberducttape/StePanel/actions/runs/40086080677))
and CodeQL ([run 40086080657](https://github.com/cyberducttape/StePanel/actions/runs/40086080657))
passed; the latest evidence commit also passed documentation and CodeQL
([runs 40086920766](https://github.com/cyberducttape/StePanel/actions/runs/40086920766)
and [40086920829](https://github.com/cyberducttape/StePanel/actions/runs/40086920829)).
There are no open CodeQL alerts. The focused Ubuntu 24.04 + Caddy install
smoke ([run 40086450399](https://github.com/cyberducttape/StePanel/actions/runs/40086450399))
has reached its installed-host smoke step and remains in progress.

The six-host full recovery matrix ([run 40085787315](https://github.com/cyberducttape/StePanel/actions/runs/40085787315))
uses application source `06ae561` and is still running the installed-host smoke
on all six OS/webserver combinations. The local Rocky Linux 9 KVM repeated
recovery run uses older source `2dead8a`; it continues to pass observed
interruption points but remains in progress and does not establish exact-source
or power-loss evidence. No in-progress run counts as a pass.

Production remains **NO-GO**. Required repeated installed-host recovery,
physical power-loss and broader host ENOSPC/database-outage evidence,
application-level WordPress restore proof across supported web servers,
capacity certification, live alert outage/retry evidence, upgrade/rollback
policy, and independent security assessment remain open.

### Latest application-source validation — 2026-10-24 (`06ae561`)

The latest application source is
[`06ae561a59a4a677a98e95efa6edf1cddbc6bfb5`](https://github.com/cyberducttape/StePanel/commit/06ae561a59a4a677a98e95efa6edf1cddbc6bfb5).
It prevents global, tenant-scoped, and site-active durable job listings from
replacing newer in-process worker lease snapshots. The full root-package Go
suite and targeted race-enabled regression tests passed locally. Exact-source
Exact-source integration CI passed in full
([run 40084549149](https://github.com/cyberducttape/StePanel/actions/runs/40084549149)):
Go 1.26.10 tests and coverage gates, full race suite, recovery and real tmpfs
ENOSPC drills, database restore checks, clean Python deployment, static
analysis, container and deployment validation, mixed load, and UI
end-to-end/accessibility tests. Documentation ([run 40084769418](https://github.com/cyberducttape/StePanel/actions/runs/40084769418))
and CodeQL ([run 40084769384](https://github.com/cyberducttape/StePanel/actions/runs/40084769384))
passed on evidence commit `d2131f2`; CodeQL reports zero open alerts.

The six-host exhaustive installed recovery run
([run 40083936175](https://github.com/cyberducttape/StePanel/actions/runs/40083936175))
uses source `b448cc9`, before this fix. The local Rocky Linux 9 KVM run uses
older source `2dead8a`. Both are still in progress and neither is exact-source
recovery evidence for `06ae561`.

Production remains **NO-GO**. Repeated installed-host recovery, physical
power-loss and broader host ENOSPC, capacity certification, supported-platform
application recovery proof, live notification outage/retry evidence,
upgrade/rollback policy, and independent security assessment remain open.

### Previous completed exact-head validation — 2026-10-24 (`b448cc9`)

Integration CI passed on
[`b448cc97c2cf74085d77fde48854822a44c30625`](https://github.com/cyberducttape/StePanel/actions/runs/40082479635), including Go 1.26.10 formatting, release checks, vet, tests and coverage gates, race tests, repository recovery and real tmpfs ENOSPC drills, database restore checks, static analysis, container build/scan, and UI end-to-end/accessibility checks. Exact-head CodeQL ([run 40082479642](https://github.com/cyberducttape/StePanel/actions/runs/40082479642)) and documentation validation ([run 40082479599](https://github.com/cyberducttape/StePanel/actions/runs/40082479599)) passed; the current alert query reports zero open CodeQL alerts.

The change under test prevents delayed durable job reads from overwriting a newer local worker lease. A targeted regression test, 30 repeated race-enabled reproductions, full local tests, and the full local race suite passed. The installed-host Rocky Linux 9 KVM full recovery matrix currently running is built from `2dead8a`, before this fix, and is not exact-head evidence for `b448cc9`.

Production remains **NO-GO**. This CI pass does not close the repeated installed-host recovery cohort, physical power-loss and broader host-level ENOSPC gates, capacity certification, full supported-platform application recovery proof, live notification failure/retry evidence, upgrade/rollback contract, or independent security assessment.

### Current release evidence — 2026-10-24 (evidence source `4e15d70`, application source `a48aafc`)

Evidence was updated in
[`4e15d70b2b84b5027a0e14b4185af5226df00fa9`](https://github.com/cyberducttape/StePanel/commit/4e15d70b2b84b5027a0e14b4185af5226df00fa9);
the latest application and installed-host smoke source is
[`a48aafc2bfed8072e4be47878aaa98e5a835a22b`](https://github.com/cyberducttape/StePanel/commit/a48aafc2bfed8072e4be47878aaa98e5a835a22b).
The full local Go suite and race-enabled suite passed with Go 1.26.10 on
`dc25ddc`; the Go application code is unchanged in `a48aafc`. `go vet ./...`
also passed locally on `a48aafc`. Integration CI for unchanged Go source
([run 40077749788](https://github.com/cyberducttape/StePanel/actions/runs/40077749788))
passed in full: formatting, release checks, vet, Go tests and coverage floors,
benchmark, ENOSPC/recovery/race drills, MySQL and PostgreSQL restore checks,
clean Python deployment, static analysis, container/Helm/Terraform validation,
vulnerability scanning, and UI E2E. Documentation checks passed on evidence
commit `4e15d70` ([run 40080603311](https://github.com/cyberducttape/StePanel/actions/runs/40080603311));
CodeQL also passed on that commit ([run 40080603276](https://github.com/cyberducttape/StePanel/actions/runs/40080603276)),
and GitHub reports zero open code-scanning alerts.

A focused disposable Rocky Linux 9 KVM run on the exact source commit passed guest
boot with ext4 user and project quotas active on `/var/www`, plus real ext4
ENOSPC recovery for backup creation and file restore (85-second boot, 31-second
drill). The image hash, host/guest details, raw test output, and explicit scope
limits are in [the 2026-10-24 KVM result](./lab-results/2026-10-24-rocky9-kvm-enospc.md).
This does not test installation, publication/import/database-restore ENOSPC,
power loss, or the repeated full recovery matrix.

The prior exact-source six-host run
([run 39960010260](https://github.com/cyberducttape/StePanel/actions/runs/39960010260),
source `e94f870`) failed on all six hosts at the assertion
`interrupted staging restore disturbed source site`. Its uploaded logs show
that the cPanel import smoke had completed and the recovery journal recovered
the separate staging-site transaction. The assertion checked only the legacy
path, so those failures do not establish source-site data loss. They also do
not prove the corrected assertion or recovery workflow passes. A scheduled
six-host run on `6ac0643` remains active
([run 40076789043](https://github.com/cyberducttape/StePanel/actions/runs/40076789043));
four host jobs have passed and two Ubuntu jobs remain in progress. The
corrected six-host smoke for `a48aafc` has all six host jobs running the installed-host smoke
([run 40079073573](https://github.com/cyberducttape/StePanel/actions/runs/40079073573));
its CI integration is pending
([run 40079061542](https://github.com/cyberducttape/StePanel/actions/runs/40079061542)).
No queued or in-progress run counts as passing evidence.

Production remains **NO-GO**. The outstanding gates below remain open; this
smoke assertion correction does not change the recovery-certification status.

### Prior release evidence — 2026-10-23 13:39 UTC (`e94f87` application source)

Current application and integration source is
[`e94f870e09f51b167d2e637c9a5b481231d66acd`](https://github.com/cyberducttape/StePanel/commit/e94f870e09f51b167d2e637c9a5b481231d66acd).
This revision makes managed-database transaction cleanup idempotent when a
crash occurs before provisioning, and cleans matching `pending-panel` records
left by a killed database helper. `make release-check`, shell syntax checks,
ShellCheck for the helper and real database smoke, and the full Go test suite
passed on the immediately preceding test-only revision `c93f66d`. Exact-source
integration CI passed on `e94f870`, including Go tests, race tests, real tmpfs
ENOSPC recovery, MySQL 8.0 restore, static analysis, container scanning, and UI
E2E ([run 39960000285](https://github.com/cyberducttape/StePanel/actions/runs/39960000285)).
CodeQL passed on current `main` with zero open alerts
([run 39960217734](https://github.com/cyberducttape/StePanel/actions/runs/39960217734));
the current alert query also returns zero open alerts. Documentation checks
passed
([run 39960000287](https://github.com/cyberducttape/StePanel/actions/runs/39960000287)).

The normal six-host installation smoke passed on `c93f66d`
([run 39925374354](https://github.com/cyberducttape/StePanel/actions/runs/39925374354));
it does not exercise every recovery boundary. The exhaustive full recovery
matrix on earlier application source `ea69e24` failed on all six hosts
([run 39915109037](https://github.com/cyberducttape/StePanel/actions/runs/39915109037)).
Its `restore:provision` process-kill case exposed a real defect: the site
transaction journal recorded the intended database before the helper created
it, and restart recovery treated the absent ownership record as a cleanup
failure. The `e94f870` fix adds real-helper smoke coverage for both absent and
partially provisioned database states. An exact-source exhaustive six-host
rerun is running
([run 39960010260](https://github.com/cyberducttape/StePanel/actions/runs/39960010260));
it is not yet passing evidence.

Production remains **NO-GO**. The full recovery rerun, physical host power-loss
recovery, broader installed-host ENOSPC/database-outage evidence, the required
20-repeat recovery cohort, complete supported-webserver application proof,
live alert-delivery outage/retry evidence, measured capacity limits,
upgrade/rollback policy, and independent security assessment remain open.

### Prior release evidence — 2026-10-23 07:23 UTC (`053ac62`)

The application and smoke-harness revision under assessment is
[`ea69e24a1b5b4cd5fd0594f016a819954a8a1517`](https://github.com/cyberducttape/StePanel/commit/ea69e24a1b5b4cd5fd0594f016a819954a8a1517).
The evidence snapshot itself is a documentation-only update on that revision;
application code remains at `cac6c5a`. Documentation CI
([run 39914766323](https://github.com/cyberducttape/StePanel/actions/runs/39914766323))
and CodeQL ([run 39914766222](https://github.com/cyberducttape/StePanel/actions/runs/39914766222))
passed on HEAD, with zero open CodeQL alerts. Integration CI
([run 39914766303](https://github.com/cyberducttape/StePanel/actions/runs/39914766303))
has passed Go tests and is running race tests; it is not yet a complete pass.

The full six-host recovery run on `cac6c5a`
([run 39910599138](https://github.com/cyberducttape/StePanel/actions/runs/39910599138))
is still running. Its Rocky Linux 9 + Caddy shard failed at the backup-recovery
login with HTTP 401 after the preceding cPanel recovery smoke and recovery
checks passed. The logs show a successful login response at 06:56:07 UTC and
the rejected login at 06:56:32 UTC. They do not record the authentication
failure reason. Because the disposable smokes share an administrator and TOTP
secret, a counter-boundary or replay interaction is plausible but unconfirmed.
The harness now retries one 401 using a newly generated TOTP counter;
transport errors and other HTTP statuses remain fatal. A full six-host rerun
against the fix is queued
([run 39915109037](https://github.com/cyberducttape/StePanel/actions/runs/39915109037));
it has not started and supplies no passing evidence yet. The quota-enabled
install run ([run 39912326883](https://github.com/cyberducttape/StePanel/actions/runs/39912326883))
also remains in progress on older source `67e67d6`.

Production remains **NO-GO**. Physical host power-loss recovery, broader
installed-host ENOSPC/database-outage evidence, the required 20-repeat recovery
cohort, complete supported-webserver application proof, live alert-delivery
outage/retry evidence, measured capacity limits, upgrade/rollback policy, and
independent security assessment remain open.

### Prior application-source evidence — 2026-10-23 06:45 UTC (`cac6c5a`)

Exact-head CI passed on
[`cac6c5adc13acb77a99dda080bfa9b9e7d6ccd35`](https://github.com/cyberducttape/StePanel/actions/runs/39908676803),
including Go 1.26.10 tests and coverage floors, race suite, repository recovery
and tmpfs ENOSPC drills, MySQL/PostgreSQL restore checks, static and
vulnerability analysis, container build/scan, deployment validation, and UI
E2E. Exact-head CodeQL ([run 39908676811](https://github.com/cyberducttape/StePanel/actions/runs/39908676811))
and documentation validation ([run 39908676870](https://github.com/cyberducttape/StePanel/actions/runs/39908676870))
passed; CodeQL reported zero open alerts. Local full Go tests, `go vet ./...`,
`make release-check`, shell syntax, and diff checks passed.
The required three-process import-stage cleanup regression also passed ten
additional repetitions under `go test -race` (12.191s).

An N-1 upgrade/rollback smoke passed on application-code ancestor `add8dae`
([run 39907581022](https://github.com/cyberducttape/StePanel/actions/runs/39907581022))
on a disposable Ubuntu systemd host, including preservation of operator-managed
runtime settings. This is useful upgrade evidence, not a supported upgrade-path
matrix or release rollback certification.

A fresh full installed-host recovery run against exact source `cac6c5a` is
pending: [run 39910599138](https://github.com/cyberducttape/StePanel/actions/runs/39910599138),
one iteration across Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with Apache
and Caddy. It is not a pass until all six host jobs succeed. The previous ten
two-iteration shards
([01](https://github.com/cyberducttape/StePanel/actions/runs/39903141857),
[02](https://github.com/cyberducttape/StePanel/actions/runs/39903141679),
[03](https://github.com/cyberducttape/StePanel/actions/runs/39903141803),
[04](https://github.com/cyberducttape/StePanel/actions/runs/39903141630),
[05](https://github.com/cyberducttape/StePanel/actions/runs/39903141759),
[06](https://github.com/cyberducttape/StePanel/actions/runs/39903141819),
[07](https://github.com/cyberducttape/StePanel/actions/runs/39903141828),
[08](https://github.com/cyberducttape/StePanel/actions/runs/39903141959),
[09](https://github.com/cyberducttape/StePanel/actions/runs/39903141732),
[10](https://github.com/cyberducttape/StePanel/actions/runs/39903141666))
test older application source `84e4286` and remain queued. They do not certify
the current source.

Production remains **NO-GO**. Physical host power-loss recovery, broader
installed-host ENOSPC/database-outage evidence, the required 20-repeat recovery
cohort, complete supported-webserver application proof, live alert-delivery
outage/retry evidence, measured capacity limits, upgrade/rollback policy, and
independent security assessment remain open. This evidence update changes
documentation only; the pending recovery run exercises application source
`cac6c5a`.

### Exact-source validation snapshot — 2026-10-23 04:37 UTC (`03422e5`)

At snapshot time, HEAD was
`03422e5b8a254de6079d5281ddccad0f82ecd2b5` (`03422e5`); it changes only
release-evidence documents and the changelog from application code commit
`cf079a12dc96c63991f72fa23edd9bc5939cf188` (`cf079a1`). Documentation
validation passed on that snapshot
([run 39901196396](https://github.com/cyberducttape/StePanel/actions/runs/39901196396));
CodeQL was running at capture and was canceled by the subsequent
documentation-only push
([run 39901196497](https://github.com/cyberducttape/StePanel/actions/runs/39901196497)).
This evidence refresh does not change application code.
The focused quota-state contract test, shell syntax checks,
`make release-check`, and 20 race-enabled site-staging cleanup repetitions
passed locally.

The six-combination installed-host smoke
([run 39897561432](https://github.com/cyberducttape/StePanel/actions/runs/39897561432))
passed on code commit `cf079a1`: Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9,
each with Apache and Caddy. This validates installation smoke only; it is not
the repeated full-recovery certification. Exact-code CI
([run 39897457130](https://github.com/cyberducttape/StePanel/actions/runs/39897457130))
passed, including formatting, vet, all Go tests and coverage floors, race
tests, repository recovery drills, real tmpfs ENOSPC, mixed HTTP load,
database restore checks, static analysis, container smoke, UI E2E, and
deployment validation. Exact-head CodeQL and docs validation passed on
`8550487` (the previous evidence snapshot). The exact-code XFS quota guest
([run 39897466684](https://github.com/cyberducttape/StePanel/actions/runs/39897466684))
is still running. The two-repeat full-recovery shard D
([run 39898086088](https://github.com/cyberducttape/StePanel/actions/runs/39898086088))
and additional shards E
([run 39900804398](https://github.com/cyberducttape/StePanel/actions/runs/39900804398))
and F
([run 39901109050](https://github.com/cyberducttape/StePanel/actions/runs/39901109050))
each have one Ubuntu 24.04/Caddy host job in progress; the other host jobs
remain queued. Their tested application code is equivalent to `cf079a1`.
None count as passes until every matrix job succeeds; the 20–50-repeat gate
remains open.

The XFS quota guest on the preceding implementation
([run 39894302014](https://github.com/cyberducttape/StePanel/actions/runs/39894302014))
did install successfully and confirmed quota accounting and enforcement, but
the smoke failed when `xfs_quota` status text contaminated the Git staging
helper's numeric project-ID output. Commit `cf079a1` redirects that status text
and adds a regression test plus an installed-smoke project-ID assertion. The
current XFS rerun is required to establish whether the defect is resolved.

The following `05dc405` and older results are historical context only.

Exact-head CI integration passed on
`05dc4054306d2fbb8cc54ac634c030f7510427cf`
([run 39894297883](https://github.com/cyberducttape/StePanel/actions/runs/39894297883)),
including full tests and coverage floors, race tests, repository recovery
drills, real tmpfs ENOSPC, mixed HTTP load, database restore checks,
vulnerability/static analysis, container/deployment validation, and UI E2E.
CodeQL ([run 39894297901](https://github.com/cyberducttape/StePanel/actions/runs/39894297901))
and documentation validation
([run 39894297857](https://github.com/cyberducttape/StePanel/actions/runs/39894297857))
also passed on that exact commit.

The six-host installed-host matrix
([run 39889562484](https://github.com/cyberducttape/StePanel/actions/runs/39889562484))
passed on `899ef70951077302b2c451a782d42218533b95df`, the immediately preceding
commit; the only difference in `ffc286a` is a test timeout/diagnostic and
changelog entry. All six Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 Apache
and Caddy combinations passed installation smoke. This does not certify full
recovery or production support.

The prior XFS quota guest run
([run 39894302014](https://github.com/cyberducttape/StePanel/actions/runs/39894302014))
failed during Git staging quota cleanup after quota enforcement had been
confirmed; the helper stdout defect is fixed in code commit `cf079a1`, and its
exact-head quota rerun remains pending above. The earlier run
([run 39889565080](https://github.com/cyberducttape/StePanel/actions/runs/39889565080))
failed in cloud-init before product installation because `/bin/sh` received a
Bash-only conditional. The fixture now uses POSIX syntax and is checked with
`sh -n`. An older ext4 quota-enabled Ubuntu guest
run
([run 39886014031](https://github.com/cyberducttape/StePanel/actions/runs/39886014031))
completed successfully on `7b549a7`: it confirmed user and project quotas on
`/var/www`, user quotas on `/home`, exercised a real project-quota overage, and
passed real backup and file-restore ENOSPC tests. It predates the shared quota
state helper in `899ef70`, so it does not certify that current implementation.
Two current-source full-recovery shards, each configured for two iterations
across all six OS/web-server combinations, are in progress on `05dc405`
([shard B](https://github.com/cyberducttape/StePanel/actions/runs/39896093726),
[shard C](https://github.com/cyberducttape/StePanel/actions/runs/39896095968));
they are not passes until every matrix job completes successfully. A separate
two-repeat matrix ([run 39886953555](https://github.com/cyberducttape/StePanel/actions/runs/39886953555))
remains active on older source `dd89653`. Current-source repeated installed
recovery is therefore still incomplete. These runs do not prove
physical host power-loss recovery, current-head XFS quotas, or all ENOSPC
boundaries. Production remains **NO-GO**.

### Latest complete `scripts/production-gate.sh` run — 2026-10-19 (`c0113dd`)

The exact code source `c0113dd4f7da75c3d3624811d813686a6a8589f5` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-19-repository-production-gate-c0113dd.md):
`go vet`, the complete test suite and coverage floors (57.1% aggregate), the
full race suite (root package 472.072 seconds), repository recovery drills,
benchmarks, mixed HTTP load, documentation, dashboard, quota fixture, assets,
action pins, and shell syntax. The change reserves declared archive capacity
before off-site transfer and retains the reservation for the staged artifact's
lifetime. See the
[exact-source report](./lab-results/2026-10-19-repository-production-gate-c0113dd.md).
At 2026-10-19 05:27 UTC, exact-head [CI](https://github.com/cyberducttape/StePanel/actions/runs/39203702895)
and [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39203702910)
were queued; standard [install](https://github.com/cyberducttape/StePanel/actions/runs/39203703063)
and [quota install](https://github.com/cyberducttape/StePanel/actions/runs/39203702833)
were pending. These do not count as passes. Real-host ENOSPC, physical power
loss, installed-host quota behavior (including XFS), repeated installed-host
recovery, and capacity certification remain open. Production remains **NO-GO**.

### Previous repository production gate — 2026-10-19 (`6e8e280`)

Exact-source evidence is retained in the
[6e8e280 report](./lab-results/2026-10-19-repository-production-gate-6e8e280.md);
it is superseded by the latest gate above and does not close host certification.

### Previous repository production gate — 2026-10-19 (`6cef641`)

Exact-source evidence is retained in the
[6cef641 report](./lab-results/2026-10-19-repository-production-gate-6cef641.md);
it is superseded by the latest gate above and does not close host certification.

### Latest full repository gate — 2026-10-19 (`c291614`)

Verified source `c2916141df8553bccd9b181243170639bad59db0` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-19-repository-production-gate-c291614.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 57.1%; the root-package
race suite passed in 473.323 seconds. Exact-head CI and CodeQL were queued;
standard/quota install smokes were pending without assigned jobs. None are
counted as passes. See the
[exact-source result](./lab-results/2026-10-19-repository-production-gate-c291614.md).
Real-host ENOSPC, physical power loss, installed-host aggregate quota behavior
(including XFS), the 20-repeat installed-host cohort, and capacity
certification remain open. Production remains **NO-GO**.

### Previous full repository gate — 2026-10-19 (`1ed5e8a`)

The pushed source checkout `1ed5e8aae4db88173747ad4a5c3c05426b0938e5` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-19-repository-production-gate-1ed5e8a.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 56.9%; the root-package
race suite passed in 474.679 seconds. Exact-head CI and CodeQL were queued;
standard/quota install smokes were pending without assigned jobs. None are
counted as passes. Real-host ENOSPC, physical power loss, installed-host
aggregate quota behavior (including XFS), the 20-repeat installed-host cohort,
and capacity certification remain open. Production remains **NO-GO**.

### Previous full repository gate — 2026-10-19 (`91f402d`)

The pushed source checkout `91f402df34c9393efe50e0c36c63882526819717` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-19-repository-production-gate-91f402d.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 56.9%; the root-package
race suite passed in 476.281 seconds. Exact-head CodeQL passed; CI was in
progress and standard/quota install smokes were pending at capture. The latter
three are not counted as passes. Real-host ENOSPC, physical power loss,
installed-host aggregate quota behavior (including XFS), the 20-repeat
installed-host cohort, and capacity certification remain open. Production
remains **NO-GO**.

### Quota-smoke fixture failure and correction — 2026-10-19

Quota-enabled run [39183265054](./lab-results/2026-10-19-quota-smoke-fixture-failure-39183265054.md)
failed during disposable-guest filesystem setup: `mke2fs` rejected the
comma-separated `quotatype=usrquota,prjquota` option before StePanel was
installed. Both the GitHub Actions fixture and local KVM harness now use the
documented colon-separated syntax. Local formatter parsing and fixture checks
pass, but this does not establish guest quota activation or StePanel quota
enforcement. The exact-source hosted quota smoke must pass before this gate can
close.

### Previous full repository gate — 2026-10-19 (`abb4f65`)

The exact source checkout `abb4f6530038dc98d674d9cd59423a16ce4e26ce` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-19-repository-production-gate-abb4f65.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 56.9%; the root-package
race suite passed in 474.732 seconds. Exact-head CI and CodeQL were queued;
standard/quota install smokes were pending at capture. None are counted as
passes. Real-host ENOSPC, physical power loss, installed-host aggregate quota
behavior (including XFS), the 20-repeat installed-host cohort, and capacity
certification remain open. Production remains **NO-GO**.

### Previous full repository gate — 2026-10-19 (`2ebd28d`)

The exact source checkout `2ebd28dce1379e9e5a596adfe6ba1c732dde668d` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-19-repository-production-gate-2ebd28d.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 56.9%; the root-package
race suite passed in 475.918 seconds. Exact-head CI and CodeQL were queued;
standard/quota install smokes were pending at capture. None are counted as
passes. Real-host ENOSPC, physical power loss, installed-host aggregate quota
behavior (including XFS), the 20-repeat installed-host cohort, and capacity
certification remain open. Production remains **NO-GO**.

### Previous full repository gate — 2026-10-19 (`d79e831`)

The exact source checkout `d79e831e07434d195ad081c7562d02b85b14d382` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-19-repository-production-gate-d79e831.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 56.9%; the root-package
race suite passed in 472.031 seconds. Exact-head hosted CI was queued; CodeQL
and standard/quota install smokes were pending at capture. None are counted as
passes. Real-host ENOSPC, physical power loss, installed-host aggregate quota
behavior (including XFS), the 20-repeat installed-host cohort, and capacity
certification remain open. Production remains **NO-GO**.

### Previous full repository gate — 2026-10-19 (`ad9a43a`)

The exact source checkout `ad9a43aa8761c4d9b52d6132fc46221c791dc004` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-19-repository-production-gate-ad9a43a.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 56.8%; the root-package
race suite passed in 471.926 seconds. Exact-head hosted CI and CodeQL were
queued, and standard/quota install smokes were pending without running jobs at
capture; none are counted as passes.

Real-host ENOSPC, physical power loss, installed-host aggregate quota behavior
(including XFS), the 20-repeat installed-host cohort, and capacity
certification remain open. Production remains **NO-GO**.

### Previous full repository gate — 2026-10-18 (`6c6e018`)

The exact source checkout `6c6e018adcce396344525d0735e88942e64679e9` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-18-repository-production-gate-6c6e018.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 56.8%; the root-package
race suite passed in 475.424 seconds.

At capture, exact-head CI and CodeQL were queued, and standard/quota install
smokes were pending without jobs. These are not counted as passing evidence.
Real-host ENOSPC, physical power loss, installed-host aggregate quota behavior
(including XFS), the 20-repeat installed-host cohort, and capacity
certification remain open. Production remains **NO-GO**.

### Latest full repository gate — 2026-10-18 (`128386e`)

The exact source checkout `128386e8beb40dcd965ba8c662b6da97237be7b6` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-18-repository-production-gate-128386e.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 56.9%; the root-package
race suite passed in 470.765 seconds. Detailed measurements and limitations
are recorded in the report.

This run does not establish exact-head hosted CI, installed-host certification,
physical power-loss recovery, host-verified aggregate Git staging quotas, or
capacity certification. The 20-repeat installed-host cohort on `633c013` is
still running; six jobs in its first shard remain at “Start disposable systemd
host” and do not count as successful iterations. Production remains
**NO-GO**.

### Previous full repository gate — 2026-10-18 (`e447ca2`)

The exact source checkout `e447ca2a28e352aa23190bc2917699d86cee5fda` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-18-repository-production-gate-e447ca2.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 56.9%; the root-package
race suite passed in 470.922 seconds. The evidence report records exact
measurements and limits.

At capture, exact-head CI and CodeQL were queued, and installation/quota runs
were pending without jobs. These are not counted as passing. The separate
20-repeat installed-host cohort on `633c013` remains incomplete. Repository
gate success does not establish host certification; production remains
**NO-GO**.

### Previous full repository gate — 2026-10-18 (`e50dab7`)

The exact source checkout `e50dab701423e84fd126a5edcbe55d3b17a34139` passed
[`bash scripts/production-gate.sh`](./lab-results/2026-10-18-repository-production-gate-e50dab7.md):
vet, full tests and coverage floors, race detection, repository recovery
drills, benchmarks, mixed HTTP load, documentation, dashboard, quota fixture,
assets, action pins, and shell checks. Coverage was 56.9%; the root-package
race suite passed in 502.760 seconds. The evidence report records exact
measurements and limitations.

At capture, CI and CodeQL for the evidence-only descendant `338ea32` were
queued; the exact-source installation and quota smokes for `e50dab7` were
pending without jobs. These are not counted as passing evidence. The existing
20-repeat installed-host cohort on `633c013` remains incomplete as described
below. Repository-gate success does not establish host certification;
production remains **NO-GO**.

### Exact-head hosted validation — 2026-10-18 (`633c013`)

Exact-head [CI](https://github.com/cyberducttape/StePanel/actions/runs/39147354411),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39147354339),
[six-host installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39147354348),
and [quota-enabled installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39147354334)
passed. The six-host run restored populated WordPress databases and passed
application HTTP probes on Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with
Apache and Caddy (RTO 4.269–5.538 seconds); it also passed database outage
recovery and one interruption-matrix iteration per host. This is useful
installed-host evidence but not repeat certification or physical power-loss
evidence.

A fresh 20-repeat cohort is running on this exact SHA: ten unique two-iteration
shards named `gate5-20261018T192511-633c013-01` through `-10`. At capture,
two were active and eight queued. Count no iterations until each shard's six
host jobs all pass. The older `a27becc` cohort completed with eight shards
passing and two failing, giving 16 successful repetitions per host combination;
it does not satisfy the 20-repeat gate. Per-host RTOs and all run IDs are in the
[hosted validation report](./lab-results/2026-10-18-hosted-validation-633c013.md).
Production remains **NO-GO**.

## Executive Summary

v1.0.0 is NOT about adding new features. It's about making existing architecture bulletproof.

The design is sound. The implementation is 85% there. The remaining work is:
- Finishing what's already designed
- Testing failure paths rigorously
- Making recovery guarantees explicit

### Latest full repository gate — 2026-10-17 (`bed56d8`)

The source `bed56d81b9b56bfd102d48c5daca453ad66ab1aa` passed the exact-source
[repository production gate](./lab-results/2026-10-17-repository-production-gate-bed56d8.md):
tests, coverage floors, race detection, recovery drills, benchmarks, mixed
load, and repository hygiene checks. This does not establish installed-host
or production-capacity certification. At capture, hosted CI was queued,
CodeQL and standard/quota installation smokes were pending, and three of ten
installed-host recovery shards remained in their long install/recovery stage.
No host result is inferred; see [current status](./CURRENT_STATUS.md) for
captured context and query GitHub for live run state.

The immediately prior `2c13219` workflow/harness update enlarged the quota
fixture and improved Git job diagnostics after the preceding quota run failed
before the activation kill boundary. The original failure and unverified
fixture diagnosis are documented in [the failure report](./lab-results/2026-10-17-quota-smoke-failure-38992616443.md).

### Prior-source hosted evidence — 2026-10-17 (`a27becc`)

The full local repository production gate passed on exact source
`a27beccc9b5be33a7e30d57662d027c74e6a6cbe`, including vet, tests, race,
coverage floors, repository recovery drills, benchmarks, mixed HTTP load,
documentation, assets, action pins, and shell checks. Exact-head hosted CI,
CodeQL, and standard installation smoke also passed; the quota-enabled smoke
was still running at capture. A ten-shard, 20-repeat installed-host full
recovery cohort was dispatched on this SHA and remains uncounted until every
required shard completes successfully. A separate focused Rocky Linux 9 KVM
run passed real ext4 ENOSPC backup and file-restore tests with user quotas
enabled ([evidence](./lab-results/2026-10-17-rocky9-enospc-03af01b.md)); it
does not cover publication, import, database restore, or physical host loss.
See the [exact-head repository report](./lab-results/2026-10-17-repository-production-gate-a27becc.md).

This evidence does not close installed-host repetition, physical power loss,
all ENOSPC boundaries, live notification outage/retry, certified capacity,
runtime support, upgrade-policy, or independent security-review gates.
Production approval remains **NO-GO**.

## Finite release contract

The following items are release gates, not general roadmap suggestions. A
release may not be described as **StePanel 1.0 — production-ready for
operator-managed single-host deployments** until every P0 item has executable
evidence recorded in this document or a linked result artifact.

### The 1.0 deployment promise

If every P0 gate below closes, the release may make exactly this promise:

> StePanel 1.0 supports operator-managed, single-host PHP/WordPress hosting
> on the Linux distribution, database, web server, and PHP runtime matrix
> published with the release. High availability, mutually hostile
> multi-tenant hosting, and hands-off disaster recovery are not included.

Docker and Kubernetes packaging remain single-replica control-plane
packaging ([CERTIFIED_ENVELOPE.md](./CERTIFIED_ENVELOPE.md)); they must not be
described as high availability while the control plane uses host-local state
and host-bound lifecycle operations. Site, domain, and account counts may be
published only as a certified envelope, never as targets.

### P0 — required before 1.0

| Requirement | Current status | Evidence / remaining work |
|-------------|----------------|---------------------------|
| Encrypted durable payloads for archive DB credentials | ✅ Implemented | Archive restore rejects missing `STEPANEL_ACCOUNT_KEY`; encrypted-payload tests cover acceptance and rejection. |
| Collision-safe migration of legacy site identities | ⚠️ Installed smoke passed once per supported OS/web-server pair; adversarial and interruption evidence remains open | New site identities are allocated uniquely and persisted. Startup fails closed on unresolved legacy collisions. The journaled repair path snapshots SQLite state, records a root-owned recovery journal, commits all affected mappings atomically, then reconciles ownership/ACL/PHP-FPM, SSH/SFTP policy, resource limits, and quotas. It refuses unsafe preconditions and requires explicit FTPS credential revocation. The exact-source six-host smoke [40433464444](https://github.com/cyberducttape/StePanel/actions/runs/40433464444) repaired the two colliding fixture sites on Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with Apache and Caddy. Remaining: interrupt a real installed repair at database and host checkpoints, validate supported filesystems, and run cross-site read/write denial tests. |
| Fail-closed archive handling | ✅ Implemented; host evidence open | TAR/ZIP imports reject unsafe paths, links, and unsupported objects. WPress records are now parsed before the external extractor runs; absolute/traversal paths, duplicate output paths, malformed or truncated records, excessive entry counts, and expanded-size overflow are rejected before any extracted file is written (`TestInspectWPressArchiveRejectsTraversalBeforeExtraction`, `TestRestoreWPressRejectsTraversalBeforeStartingExtractor`). Independent host ENOSPC and interruption evidence remains part of the broader recovery gate. |
| Preserve restrictive imported file modes | ✅ Implemented | TAR/ZIP extraction preserves ordinary permission bits and strips special bits; mode tests pass. |
| Realistic disk admission and reservations | ✅ Implemented; host evidence open | Known `Content-Length` is admitted at the actual archive size; unknown-length streams reserve progressively ahead of bytes written up to the upload ceiling; local database restore paths hold a shared SQLite reservation against the larger of twice the incoming SQL dump or 1.5 times the live database; WPress restore inspects declared expansion and reserves that amount across the import and site-staging filesystems; off-site restore validates the bounded manifest size, reserves archive bytes in the shared ledger before transfer, enforces the declared size, and holds the reservation through staging cleanup; remote inspection and ZIP-import spools reserve progressively before writing and retain the unused hold until close. These logical reservations still require real-host ENOSPC and competing-writer evidence. |
| Aggregate write-time limits for Git release staging | ⚠️ Implemented; partial host evidence | The root broker assigns a unique project ID and enforces a project hard quota at three times the configured final-tree limit, with a logical capacity reservation at the same bound. Unsupported quota filesystems fail closed; temporary quota removal is required before activation. The earlier-source Ubuntu 24.04/ext4 quota guest run [40307543702](https://github.com/cyberducttape/StePanel/actions/runs/40307543702) (`ab6cde8`, not current source `3bb1e6b`) exercised aggregate release-stage writes and observed the expected project-quota `Disk quota exceeded` result; see the [run report](./lab-results/2026-10-26-ubuntu-ext4-quota-install-40307543702.md). This is one disposable QEMU guest on ext4; it is historical evidence and does not certify quota behavior on current source, XFS, or production mounts. XFS and real production mount behavior still require verification. |
| Atomic managed database restore | ⚠️ Implemented; installed-host evidence open | `stepanel-dbctl restore-dump` loads the dump into a shadow database registered as `pending-restore`, then swaps it in. MySQL/MariaDB move every base table in one `RENAME TABLE` and recreate views, triggers, routines and events from definitions exported before the swap. That DDL is several statements, so it runs behind a login fence: the database's accounts are locked and their sessions ended until the swap is finished and verified, even across a crash. Site clients see refused logins for the swap (a short, real downtime), never a schema without its triggers or views. PostgreSQL renames both databases in one transaction. The dump loads without administrative rights (see [Databases](./DATABASES.md)). Restore admission estimates shadow demand as the larger of twice the incoming SQL dump and 1.5 times the live database, preserves the helper's filesystem reserve, and holds a durable SQLite capacity reservation on the configured local database filesystem for the operation. This coordinates StePanel panel/worker restores; unrelated host writers remain outside that ledger. A failed or killed load leaves the live database unchanged. Once the load succeeds, every step rolls forward: the next `dump`, `restore-dump` or startup `reconcile` of that database finishes an interrupted swap. A retried job reuses the first attempt's safety backup. Exercised locally against MariaDB 11.8 and PostgreSQL 18, and in CI against MySQL 8.0, with SIGKILL before and after each swap DDL step, a concurrent site client checking views, functions and trigger side effects, and a restore racing a running dump. Open: the `abrupt-loss-operations` guest-kill drill has no recorded run. MySQL/MariaDB with binary logging must set `log_bin_trust_function_creators=1` to restore triggers and stored functions. The capacity reservation is an admission estimate, not a kernel-enforced disk quota; other host writes can still exhaust the filesystem, and no ENOSPC fault has been injected into the shadow load, swap or cleanup on a real host. The load fails without touching the live database. See the [disk exhaustion outcome contract](./REAL_HOST_FAILURE_MATRIX.md#disk-exhaustion-outcome-contract). |
| VM crash testing: power loss, ENOSPC, DB outage, broker interruption, worker death | ⚠️ Partial; current source has hosted-container smoke only | Exact-source six-host installed smoke [40433464444](https://github.com/cyberducttape/StePanel/actions/runs/40433464444) passed one recovery-matrix iteration per Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 Apache/Caddy host. This hosted systemd-container run is not VM power-loss evidence. The earlier QEMU/ext4 run [40307543702](https://github.com/cyberducttape/StePanel/actions/runs/40307543702) passed guest interruption and two ENOSPC cases on older source `ab6cde8`; it is useful historical evidence, not proof for `3bb1e6b`. Physical-host power loss and broader ENOSPC/database-restore failures remain unproven. |
| Repeated recovery scenarios | ⚠️ Open; exact-source 20-repeat cohort in progress | The exact-source six-host installed smoke [40433464444](https://github.com/cyberducttape/StePanel/actions/runs/40433464444) completed one recovery-matrix iteration per host combination. The same-source 20-repeat cohort [40436448192](https://github.com/cyberducttape/StePanel/actions/runs/40436448192) targets application source `3bb1e6b` and is in progress; its first six-host shard is running. No repetitions count until all 20 shards and all six hosts in each shard pass and artifacts are reviewed. Older-source cohorts do not count for this source. |
| Cross-process data consistency | ✅ Implemented; needs matrix evidence | The panel and job worker share the control-plane database. Accounts and site assignments are written per row with the observed payload as a compare-and-swap (`TestStaleAccountStorePersistsOnlyItsOwnChange`, `TestStaleAccountStoreConflictingChangeIsRejected`); deployment history appends with a revision check (`TestDBStoresSharingHistoryDoNotLoseRecords`); API tokens are individual rows. A conflicting write fails with a retryable error instead of overwriting. Two-process soak evidence on an installed host remains to be recorded. |
| Authoritative deployment status | ✅ Implemented | Activation is the commit point for Git deploy, pipeline release, and Git rollback. A failure before activation fails the job; after activation the job succeeds with `outcome` `activated` or `committed_reconciliation_pending`, and history records the completed activation (`TestCommittedReleaseIsNeverReportedAsCancelled`). |
| Application runtime/manifest consistency | ✅ Journaled; installed-host evidence open | App deploy, rollback, start, stop, and restart persist the previous and target generations before changing runtime state. Startup compensates uncommitted transitions and re-applies the committed target before retiring its journal. Regression coverage includes `TestRecoverAppActivationJournalRestoresRuntimeAndManifest`, `TestRecoverAppActivationJournalReconcilesCommittedGeneration`, and `TestAppStateActionFailureCompensatesAndCleansJournal`; focused results are recorded in [the filesystem and app-activation audit](./lab-results/2026-10-15-filesystem-and-app-activation-audit-bad2c1d.md). Real systemd crash-point injection remains required before claiming host-level recovery. |
| Application-level WordPress restore proof | ⚠️ Partial; exact-source proof passed once per host combination | The exact-source six-host installed smoke [40433464444](https://github.com/cyberducttape/StePanel/actions/runs/40433464444) restored a populated WordPress filesystem and database, started the installed web stack, and passed an HTTP application probe on Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with Apache/Caddy and MariaDB. RTO ranged from 5.419 to 16.400 seconds. This is one hosted-container run per combination; repeated recovery, physical-host recovery, and all supported runtime combinations remain open. |
| Operational notifications | ⚠️ Implemented; delivery evidence required | Failed/dead-letter transitions enter a SQLite outbox atomically; signed HTTPS webhook and STARTTLS SMTP delivery retry independently, and `/api/health/operational` exposes undelivered and failed counts. Tests cover event creation, HMAC signatures, a verified STARTTLS SMTP session including auth and message delivery, redaction, and retry state. `OPERATIONS.md` now includes redacted incident triage, retry verification, and independent-monitoring guidance. Still required: delivery against configured live SMTP/webhook endpoints and outage/recovery evidence on an installed host. |
| Capacity certification | ❌ Open | [CERTIFIED_ENVELOPE.md](./CERTIFIED_ENVELOPE.md) lists targets (500 sites, 2,000 domains, 100 accounts, 2 concurrent mutations), not a certified envelope. Synthetic HTTP evidence includes the [200-site CI baseline](./HTTP_LOAD_BASELINE_2026-10-02.md) and a [single 500-site experiment](./lab-results/2026-10-24-500-site-mixed-load.md) on an unsupported OS without resource monitoring. High-backlog SQLite claim latency, including the 100k queued/ten stalled-owner case and query-plan comparisons, is in [CAPACITY.md](./CAPACITY.md) and its [benchmark report](./lab-results/2026-10-18-queue-claim-benchmark-a8d0340.md). These measurements omit real PHP/database traffic, sustained load, resource ceilings, and required recovery evidence. A reproducible full-envelope run must certify limits before any site/domain/account count is promised. |
| Upgrade and rollback | ⚠️ Partial | The current-source N-1 smoke ([run 40115776818](https://github.com/cyberducttape/StePanel/actions/runs/40115776818)) passed a v0.6.0-to-candidate install, preserved operator-managed settings, and recovered the prior candidate after a deliberately broken upgrade. Its v0.6.0 fixture is modified for the current credential/service contract; see the [run report](./lab-results/2026-10-24-n-minus-one-upgrade-40115776818.md). Remaining: test unmodified supported source-to-target upgrades, publish the version/schema compatibility matrix and rollback policy, and repeat upgrade interruption/rollback on release candidates. |
| Supported runtime matrix | ⚠️ Partial | The exact-source six-host installed smoke [40433464444](https://github.com/cyberducttape/StePanel/actions/runs/40433464444) passed Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with Apache and Caddy plus MariaDB, once per combination, in disposable systemd containers. This does not certify XFS, dedicated physical hosts, Debian 12, OpenLiteSpeed, other database engines, PHP minor-version combinations, or alternate SELinux/filesystem configurations. `INSTALLATION.md` describes the tested combinations as Operator Beta, not production-certified. |
| Security lifecycle | ⚠️ Partial | [SECURITY.md](../SECURITY.md) defines private reporting, beta limitations, stable-version support windows, and patch-response objectives. Remaining: an independent security review of the release candidate and operational evidence that the advisory, patch, and credential-rotation process is usable. |
| Typed privileged-operation protocol | ✅ Implemented | Production callsites use concrete root-broker request types, including environment/resource operations, database cleanup/reconciliation, and managed route deletion. `Request` no longer contains a generic helper payload, the broker has no generic handler, and production helper wrappers fail closed without a typed route. A regression test rejects the removed request type. |
| Release documentation reconciled with executable evidence | ⚠️ Partial; this snapshot is under review | Application source [`3bb1e6b`](https://github.com/cyberducttape/StePanel/commit/3bb1e6bf2ff873def292ab2d406d5ceebd7b96f7) passed exact-source PR CI, CodeQL/Go analysis, documentation, and push CI integration [40435936337](https://github.com/cyberducttape/StePanel/actions/runs/40435936337). Exact-source six-host recovery and identity-repair smoke passed [40433464444](https://github.com/cyberducttape/StePanel/actions/runs/40433464444). This documentation reconciliation is proposed in [PR #64](https://github.com/cyberducttape/StePanel/pull/64); its required CI test remains pending. The same-source 20-repeat cohort [40436448192](https://github.com/cyberducttape/StePanel/actions/runs/40436448192) and single-host install smoke [40435936293](https://github.com/cyberducttape/StePanel/actions/runs/40435936293) remain in progress. Physical power loss, broader ENOSPC/database outage, filesystem quota certification, live notification outage/retry, measured capacity, upgrade/rollback policy, and independent security review remain open. |

The repository production gate was rerun on the exact pushed commit `ee563eb`
and passed, including coverage thresholds, the complete race suite,
repository recovery drills, mixed HTTP load, and documentation checks. The
[run record](./lab-results/2026-10-16-repository-production-gate-ee563eb.md)
captures its measurements and limitations. It does not close any installed-host
or capacity-certification gate above.

### Historical evidence update — 2026-10-14

The earlier three-run and 18-proof counts in the P0 table above predate
commit `16957b8`. Hosted run [38547265154](./lab-results/2026-10-14-hosted-install-smoke-38547265154.md)
adds six passing installed-host combinations and six database-backed
WordPress recovery proofs, bringing the evidence to four runs and 24
single-run proofs. Each OS/web-server pair has still only been exercised once
in each run; this does not satisfy the repeated recovery gate. Hosted CI,
CodeQL, and installation smoke passed on `730cbd9` ([CI](https://github.com/cyberducttape/StePanel/actions/runs/38565327194),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38565327198),
[install](https://github.com/cyberducttape/StePanel/actions/runs/38565327133));
the local full Go suite also passes on subsequent source commit `c73926e`.
At capture time for source commit `12b0d70`, CodeQL and UI E2E had
passed, the Go CI job and six-host installation smoke were in progress, and
the quota-enabled guest run was pending ([CI](https://github.com/cyberducttape/StePanel/actions/runs/38572967532),
[installation](https://github.com/cyberducttape/StePanel/actions/runs/38572967470),
[quota run](https://github.com/cyberducttape/StePanel/actions/runs/38572966792)).
No new physical power-loss, broader ENOSPC,
SMTP/webhook outage, OpenLiteSpeed, or capacity-certification claim is made.

### Current-head evidence update — 2026-10-15

Commit `7af2e82` passed hosted CI ([run 38662396749](https://github.com/cyberducttape/StePanel/actions/runs/38662396749)),
CodeQL ([run 38662396836](https://github.com/cyberducttape/StePanel/actions/runs/38662396836)),
and all six installed-host combinations ([run 38662396787](https://github.com/cyberducttape/StePanel/actions/runs/38662396787)).
The Go coverage, race, recovery, and real tmpfs ENOSPC checks passed. The
updated control-plane outage drill passed on every installed host, requiring
explicit readiness failure or a matching database startup diagnostic before
accepting the outage result. All six installed WordPress recovery proofs passed
with measured RTOs of 5.157–6.621 seconds. This brings installed-host evidence
to five runs and 30 single-run proofs across the matrix; it does not satisfy
the 20–50 repetitions. See the [run evidence](./lab-results/2026-10-15-hosted-install-smoke-38662396787.md).
The quota-enabled installation/recovery run for the same source commit also
passed ([run 38662396783](https://github.com/cyberducttape/StePanel/actions/runs/38662396783)):
user quotas were enabled on a separate `/var/www`, WordPress recovered in
75.925 seconds under QEMU TCG, abrupt QEMU guest loss with persistent disks was
recovered, and backup/file-restore ENOSPC tests passed. See the [quota-run
evidence](./lab-results/2026-10-15-quota-install-smoke-38662396783.md).
This is not physical host power-loss evidence; publication/import/database-
restore ENOSPC, 20–50 repetitions, OpenLiteSpeed, and certified capacity remain
open. A second quota-enabled guest run on `1fb3879` also passed with a
79.412-second WordPress proof RTO ([run 38667385389](https://github.com/cyberducttape/StePanel/actions/runs/38667385389));
it likewise recovered from abrupt QEMU guest loss and backup/file-restore
ENOSPC. The documentation-head quota rerun on `94d5801` remains pending.

Documentation-head commit `94d5801` passed hosted CI
([run 38670211688](https://github.com/cyberducttape/StePanel/actions/runs/38670211688)),
CodeQL ([run 38670211775](https://github.com/cyberducttape/StePanel/actions/runs/38670211775)),
and another six-host installation smoke ([run 38670211824](https://github.com/cyberducttape/StePanel/actions/runs/38670211824)).
All six WordPress proofs and control-plane outage drills passed; RTOs were
3.997–6.531 seconds. This brings the six-host evidence to seven runs and 42
single-run proofs, not repeated recovery certification. See the [rerun report](./lab-results/2026-10-15-hosted-install-smoke-38670211824.md).
The required 20-repeat matrix is running in workflow
[38670189826](https://github.com/cyberducttape/StePanel/actions/runs/38670189826);
its result is not yet counted. No evidence here establishes physical host
power-loss recovery, database-restore/publication ENOSPC, OpenLiteSpeed support,
or certified capacity.

The local repository production gate passed on the same checkout with Go
1.26.7: full coverage and race suites, repository recovery drills,
control-plane microbenchmarks, synthetic mixed HTTP load, and documentation,
asset, action-pin, and shell checks. See the
[captured results](./lab-results/2026-10-15-repository-production-gate-3f1445f.md).
This does not close the still-running six-host repetition matrix or any
physical-host, broader ENOSPC, or capacity-certification gate.

The subsequent installed-host smoke run on `f335ed5` passed all six host
combinations ([run 38677968172](https://github.com/cyberducttape/StePanel/actions/runs/38677968172));
each completed one default recovery-matrix iteration and a populated,
database-backed WordPress HTTP recovery proof. Measured proof RTOs were
4.548–8.155 seconds. This raises the recorded hosted smoke evidence to eight
six-host runs/48 single-run proofs, but remains one run per host per workflow
execution. See the [run report](./lab-results/2026-10-15-hosted-install-smoke-38677968172.md).
At capture, CodeQL for `970bb78` was in progress and its CI, install, and quota
checks were queued; none is counted as passed. The independent 20-repeat
matrix also remains in progress. No physical-host power-loss,
publication/database-restore ENOSPC, or capacity certification claim is
made.

Focused local regression tests also pass on application source commit
`970bb78` for distinct site-job admission, Linode/AWS/OpenStack provider
contracts, DNS record serialization, per-dump capacity admission, and atomic
schedule/outbox staging ([audit report](./lab-results/2026-10-15-provider-and-capacity-regression-audit-970bb78.md)).
These fixture-backed checks do not replace live-provider or installed-host
evidence.

### Repository production gate — 2026-10-16

The full repository production-gate script passed on exact source commit
`82cfe464757829dbbc15b92e92bbf91051df3811`: full Go tests and coverage
thresholds, race tests, repository crash drills, control-plane benchmarks,
synthetic mixed HTTP load, documentation and asset checks, action pinning, and
shell syntax. See the [captured run results](./lab-results/2026-10-16-repository-production-gate-82cfe46.md).
The local KVM extended recovery matrix was interrupted before completion and
is not counted. This repository-level pass does not close real-host repeated
recovery, physical power-loss, broader ENOSPC, live SMTP/webhook outage, or
capacity-certification gates.

### Current-checkout repository gate — 2026-10-16 (`3927b38`)

The full local repository gate passed on exact checkout `3927b38`: Go vet,
full tests and configured coverage floors, race tests, repository recovery
drills, control-plane benchmarks, synthetic mixed HTTP load, documentation,
assets, action pins, and shell syntax. See the
[captured results](./lab-results/2026-10-16-repository-production-gate-3927b38.md).
This does not close the installed-host repetition cohort, physical power-loss,
broader ENOSPC, live notification outage/retry, runtime-matrix, capacity, or
independent security-review gates.

Three two-repeat shards on older source `60f96ee` have now completed, each with
one host job failing before import at the cPanel-move smoke login (HTTP 401).
The available evidence does not identify whether the cause was a transient
TOTP replay/window issue or another credential rejection. The smoke now retries
once using a fresh TOTP counter; that harness adjustment is not yet validated
by a completed standard six-host hosted run, and none of the three shards is
counted as passing. See the [shard outcome report](./lab-results/2026-10-16-hosted-recovery-shard-outcomes.md).

### Repeated hosted recovery execution update — 2026-10-16

The 20-repeat hosted run [38694634787](https://github.com/cyberducttape/StePanel/actions/runs/38694634787)
on source `31a8341` did not complete within GitHub's six-hour job limit. All
six OS/web-server jobs completed two full iterations and began iteration 3/20
before timeout; the partial third iterations are not counted. See the
[run analysis](./lab-results/2026-10-15-hosted-repeat-matrix-timeout-38694634787.md).
The installed-host workflow now supports independent one- or two-iteration
shards with unique concurrency IDs, and the real-host matrix guide documents
how to aggregate 20–50 iterations on one source SHA. This is a harness
correction, not a closed recovery gate; no 20-repeat cohort has passed.

### Quota-backed guest evidence update — 2026-10-16

Quota-enabled Ubuntu guest run [38828118772](https://github.com/cyberducttape/StePanel/actions/runs/38828118772)
passed on source `0fe2138`: user quotas were active on a dedicated `/var/www`
filesystem, the WordPress proof completed in 44.724 seconds, abrupt QEMU guest
loss recovered with persistent disks, and real ENOSPC backup/file-restore
checks passed. This does not establish physical host power-loss recovery or
ENOSPC safety during publication, import, or database restore.

### Exact-head validation update — 2026-10-16 (`64696a2`)

The full Go tests, race tests, vet, shell syntax, and documentation checks passed
locally; hosted CI and CodeQL also passed, with zero open code-scanning alerts at
capture. See the [validation report](./lab-results/2026-10-16-current-head-validation-64696a2.md).
This does not close the running 20-repeat hosted recovery cohort or the physical-
host, broader ENOSPC, live-notification, capacity, runtime-certification, or
independent-review gates.

### Full repository gate update — 2026-10-16 (`1e38dfc`)

The full local repository gate passed on this code-bearing commit; the
[report](./lab-results/2026-10-16-repository-production-gate-1e38dfc.md)
records the exact coverage, race, recovery-drill, benchmark, and mixed-load
results. This synthetic repository gate does not close the real-host recovery,
physical power-loss, broader ENOSPC, or capacity-certification gates.

### Exact-head repository gate — 2026-10-16 (`c628903`)

The full repository production gate passed on exact checkout `c628903` with Go
1.26.7. Vet, coverage (56.2% overall; all package floors), the race suite
(448.918 seconds for the root package), repository recovery drills, one-shot
control-plane benchmarks, synthetic 200-site mixed HTTP load, documentation,
assets, action pins, and shell syntax all passed. See the
[exact-head report](./lab-results/2026-10-16-repository-production-gate-c628903.md).
This does not close the installed-host repetition cohort, physical power-loss,
broader ENOSPC, live notification delivery/outage, runtime matrix, capacity, or
independent security-review gates.

Status refresh after commit `d1a259c`: CodeQL is running; CI and the
six-host installation matrix are queued. The quota guest run for `c73926e`
remains active, while the newest quota run for `d1a259c` is pending
([CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38574974617),
[CI](https://github.com/cyberducttape/StePanel/actions/runs/38574974335),
[install](https://github.com/cyberducttape/StePanel/actions/runs/38574974411),
[active quota run](https://github.com/cyberducttape/StePanel/actions/runs/38572742100),
[pending quota run](https://github.com/cyberducttape/StePanel/actions/runs/38574974320)).
The quota run for `12b0d70` was pending and then replaced by the newer pending
run; the active `c73926e` run was not canceled. This is queue state, not test
evidence.

Status refresh after `b88c3ad`: current-head hosted CI passed, including the
serialized Go test/coverage suite, race tests, recovery drills, UI E2E, and
container/deployment validation ([CI run 38582152727](https://github.com/cyberducttape/StePanel/actions/runs/38582152727));
CodeQL also passed ([run 38582152612](https://github.com/cyberducttape/StePanel/actions/runs/38582152612)).
The six-host installed-host matrix is still running
([run 38582152769](https://github.com/cyberducttape/StePanel/actions/runs/38582152769))
and is not included in the completed-run or WordPress-proof counts. An earlier
matrix on `5fd7907` passed five combinations but failed Rocky Linux 9/Caddy at
the installed WordPress HTTPS probe; the latest candidate includes a bounded
retry for local certificate/readiness convergence. Its result remains pending.
The new quota-enabled guest run for `b88c3ad` is also pending; the previously
active quota run is separate and must not be attributed to this commit.

The latest completed installed-host values supersede the historical values in the
runtime table below. They remain smoke results, not a certified support
promise.

### Runtime support boundary (current evidence, not certification)

The installer accepts more combinations than CI has validated. Do not infer
support from package-manager detection, selectable web-server options, or a
successful unit test. The installed-host smoke matrix currently exercises:

| Distribution | Web server | Database | Latest WordPress recovery-proof result |
|---|---|---|---|
| Ubuntu 24.04 | Apache | MariaDB | Pass, 5.480 s proof RTO, [job 121118194339](https://github.com/cyberducttape/StePanel/actions/runs/40176491637/job/121118194339) |
| AlmaLinux 9 | Apache | MariaDB | Pass, 5.154 s proof RTO, [job 121118194403](https://github.com/cyberducttape/StePanel/actions/runs/40176491637/job/121118194403) |
| Rocky Linux 9 | Apache | MariaDB | Pass, 4.109 s proof RTO, [job 121118194343](https://github.com/cyberducttape/StePanel/actions/runs/40176491637/job/121118194343) |
| Ubuntu 24.04 | Caddy | MariaDB | Pass, 6.316 s proof RTO, [job 121118194399](https://github.com/cyberducttape/StePanel/actions/runs/40176491637/job/121118194399) |
| AlmaLinux 9 | Caddy | MariaDB | Pass, 5.082 s proof RTO, [job 121118194356](https://github.com/cyberducttape/StePanel/actions/runs/40176491637/job/121118194356) |
| Rocky Linux 9 | Caddy | MariaDB | Pass, 6.479 s proof RTO, [job 121118194342](https://github.com/cyberducttape/StePanel/actions/runs/40176491637/job/121118194342) |

These are one-run smoke results, not production certification or a general
support promise. They do not certify repeated recovery, PHP versions, upgrade
paths, alternate filesystems, SELinux policy variants, or capacity.
OpenLiteSpeed, Debian 12, MySQL/PostgreSQL, and unlisted combinations remain
untested. Release notes must not describe them as supported based solely on
installer code paths.

### Upgrade and rollback contract

The installer transaction rollback covers a candidate that fails its
post-install health check: it restores the previous binary/configuration and
the byte-preserved control-plane database file set. This does not promise
rollback after the candidate has successfully started and accepted writes.
For a completed upgrade, the supported recovery path is to stop panel and
worker, reinstall the prior release, and restore the matching pre-migration
database snapshot as documented in [STATE.md](./STATE.md); writes made after
the snapshot are lost. One-way data or encryption migrations may require this
snapshot and cannot be reversed by reinstalling an older binary alone.

Only explicitly tested source-to-target version paths may be advertised.
Currently the N-1 lab fixture is v0.6.0 to the candidate; it is not evidence
for every intermediate release, a clean-install certification, or a release
candidate until those exact runs are recorded. Each release must record the
source version, target version, schema versions, clean-install result,
interrupted-upgrade behavior, installer rollback result, and snapshot-based
post-upgrade recovery result.

### P1 — strongly recommended for 1.0

These items do not silently become release claims. They are tracked separately:

- Reversible site suspend/resume with reason, audit event, and capability
  reporting.
- First-class blank PHP, WordPress, Git, and Node site creation workflows.
  Blank PHP sites: implemented (`POST /api/sites`, durable `site.create` job,
  `site_creation_test.go`); WordPress, Git and Node templates are open.
- The operator-supplied `STEPANEL_RECOVERY_PROOF_COMMAND` hook remains
  available for non-WordPress applications. It receives `<site> <stage>`,
  and a rehearsal records the `application` level only when the command
  exits successfully. It does not satisfy the P0 WordPress restore proof.

SMTP/webhook notifications and the WordPress application-level restore proof
were moved from P1 to P0 on 2026-10-11.

### Test coverage ratchet

CI enforces a repository-wide statement-coverage floor of **50%** and
package-specific floors for the security boundary, durable jobs, archive
importer, and legacy root workflow handlers. The 50% value is an executable
ratchet from the current 47.5% baseline, not a production-quality claim by
itself. It must increase as root-package handlers are extracted and tested;
the release process must not lower it to accommodate a regression. Coverage is
supplemental evidence and does not replace the failure-recovery matrix below.
The current local repository-gate run is recorded in
[2026-10-14-repository-production-gate-db7cd6d.md](./lab-results/2026-10-14-repository-production-gate-db7cd6d.md);
it passed but does not close the installed-host release gates.

---

## Gate 1: One Lifecycle Authority

**Requirement:** Every site lifecycle operation has exactly one owner per
layer, and every write that publishes or removes a canonical site tree goes
through `SiteManager`'s path-safe primitives.

**Roles (October 2026 revision).** An earlier version of this gate said all
site mutations flow through `SiteManager`. The code does not work that way,
and this gate now describes the implementation as it is:

| Layer | Owner | Responsibility |
|-------|-------|----------------|
| Lifecycle orchestration | Root-package lifecycle handlers and durable jobs (`site_lifecycle.go` `handleSiteTermination`, import/restore/clone/release jobs) | Locks, ordering, audit, recovery, and the decision to create, publish, or remove a site |
| Filesystem primitive | `SiteManager` (`internal/sites/manager.go`) | Staging allocation, atomic activation/replacement/rollback, clone, and descriptor-rooted final deletion. Deletion rejects a writable `sites/` parent and removes relative to an opened `os.Root`; canonical path components must remain trusted and customer accounts must not write `sites/`. |
| Privilege executor | Root broker and helpers (`internal/rootbroker`, `stepanel-sitectl`, `stepanel-appctl`) | Site Unix account, ownership, PHP-FPM pool, SSH access, quotas, services, and the privileged part of site teardown |

**Two domains:**
1. **Staging areas**: temporary workspaces allocated by `SiteManager`.
   - Domain components (importer, restore, git, backup) may mutate staging
     granted to them.
   - Staging is published only by `SiteManager.ActivateStaged*` and discarded
     only by `SiteManager.DiscardStaging()`.
   - Staging paths are never persisted across operation boundaries.
2. **Canonical sites**: `{webRoot}/sites/{siteName}` for legacy sites, and
   for sites placed in a hosting account (ADR-0007) the document root
   `{homeRoot}/{user}/public_html` or `{homeRoot}/{user}/{domain}`. Placed
   sites stage under `{homeRoot}/.stepanel-manager-staging`
   (`SiteManager.CreateSiteStagingLease()`), so activation stays one rename on
   the home filesystem; deleting one removes only its document root and its
   preserved releases, never the shared home.
   - Unprivileged code publishes, replaces, and removes canonical trees only
     through `SiteManager`.
   - The privileged teardown helper (`stepanel-sitectl delete`) also removes
     the site tree, because it must first remove root-owned state inside it.
     `SiteManager.Delete()` then finalizes the removal idempotently. This is
     the one place outside `SiteManager` that deletes a canonical tree.

**How each operation actually flows:**
```
create  → site.create job (site_creation.go): SiteManager.CreateStagingLease() + template
          → broker/stepanel-sitectl prepare (account, PHP-FPM pool)
          → SiteManager.ActivateStaged() → broker seal; on failure (or after an
          unclean shutdown) broker delete + SiteManager.Delete() remove the new site
import  → SiteManager.CreateStagingLease() + domain extract + SiteManager.ActivateStaged()
clone   → SiteManager.CreateStagingLease() + copy (staging.go) + SiteManager.ActivateStaged()
restore → SiteManager.CreateStagingLease() + domain restore + SiteManager.ActivateStaged()
release → SiteManager.CreateReleaseStagingLease() + build + SiteManager.ActivateStagedReplacing()
          (rollback: SiteManager.RollbackStagedActivation())
update  → domain handlers + root broker (PHP runtime, resources, quota); SiteManager is not involved
delete  → lifecycle job: backups, services, tasks, routes, databases
          → broker/stepanel-sitectl delete (account, PHP-FPM, SSH, site tree)
          → SiteManager.Delete() (descriptor-rooted, idempotent finalization)

# Never acceptable:
❌ os.Mkdir/Create/Rename on {webRoot}/sites/* outside SiteManager
❌ Publishing a staged tree without SiteManager.ActivateStaged*
❌ A new privileged path that deletes canonical trees without being listed above
```

**Manager API contract:** `SiteManager` is the site filesystem primitive
interface. Imports, restores, and configuration updates are orchestrated by
durable jobs and the root broker; suspend and resume are not exposed by this
interface. `TestGateOneDocumentMatchesManager` asserts the exact implemented
method set and rejects stale placeholder claims in this section.

**Implemented but unused:** `SiteManager.Create()` and `SiteManager.Clone()`
have no production callers; the flows above use staging instead.

**Current Status: MET for publication and removal; configuration updates are broker-executed**
- ✅ Generic archive, Git (activation and rollback), cPanel, WordPress,
      file-backup restore, staging clone, backup restore-to-staging, and the
      release pipeline all publish through manager-owned staging
- ✅ Failed staging workflows discard trees through `SiteManager.DiscardStaging()`
- ✅ Site termination finalizes deletion through descriptor-rooted
      `SiteManager.Delete()` after
      the privileged helper removes root-owned state
- ✅ Canonical site modifications are protected by distributed locks
- ⚠️ Configuration updates, suspend, and resume have no `SiteManager` entry
      point; updates run through domain handlers and the root broker, and
      suspend/resume are not offered

**Acceptance Criteria:**
- [x] All site creation operations publish through SiteManager staging (Phase 1 audit: importer.go, cpmove.go, staging.go use manager staging)
- [x] Site modification operations are lock-protected (Phase 2 audit: node_tooling.go, apps.go; release_pipeline.go uses manager release staging)
- [x] Site deletion finalizes through descriptor-rooted `SiteManager.Delete()`; the privileged helper's tree removal is the documented exception
- [x] Grep audit: no `os.Mkdir.*sites` outside manager (only in test files)
- [x] Grep audit: no direct file operations on site paths outside manager (all non-test operations are read-only or lock-protected)
- [x] This section matches `SiteManager`'s implemented methods (`TestGateOneDocumentMatchesManager`)

---

## Gate 2: Cross-Process Lock Enforcement

**Requirement:** Panel and worker NEVER independently mutate the same resource

**What this means:**
- Distributed locks (DBLocks) must be acquired BEFORE any site mutation
- Held for the ENTIRE operation duration
- Test suite must include adversarial concurrent scenarios

**Current Status:**
- ✅ DBLocks implementation redesigned and verified (see below)
- ✅ Boot-time reconciliation guarded so only the panel runs it (previous
      behavior let panel and worker concurrently rename recovery journals
      and reconcile host state during startup)
- ✅ All direct mutation lock call sites use the durable fenced-lock wrapper;
      the wrapper retains the process-local mutex as a fast-path and acquires
      `DBLocks` for cross-process fencing.
- ✅ Wired into site termination, Git deployment/rollback, backup/restore,
      route/proxy changes, runtime reconciliation, resources, tasks, workers,
      SSH access, database operations, and account mutations.
- ✅ Resource enforcement acquires both site and owning-account fences, so
      resource updates cannot race account suspension.
- ✅ Root-broker admission enforces the lease-watch safety margin before
      starting a privileged helper; near-expiry leases are rejected rather
      than waiting for the watchdog's first interval-delayed check. The
      regression test exercises that admission boundary. Killing the broker
      during installed-host mutations and proving takeover/reconciliation
      remain separate open Gate 5 evidence.
- ✅ Two independent SQLite connections now exercise all five conflicting
      mutation lock scenarios; an OS-process regression test also proves
      hold/block/reacquire behavior across panel/worker-style processes.
- ✅ Full interrupted-workflow acceptance (2026-10-02): `workflow_interruption_test.go`
      interrupts each of the five real workflows at every instrumented
      boundary, runs the conflicting workflow, and verifies the final state.

**DBLocks fixes (September 2026):**

The prior `internal/operations/db_locks.go` implementation had latent bugs
that meant it could not actually be relied on, so its earlier "✅ implementation"
line above was inaccurate. Rewritten in this cycle:

- `INSERT ... WHERE NOT EXISTS` collided with the resource_key primary key
  on expired-lease takeover; SQLite raised UNIQUE-constraint errors and
  Acquire failed even though the lock was legally re-acquirable. Now uses
  `INSERT ... ON CONFLICT(resource_key) DO UPDATE ... WHERE lease_until <= ?`
  — a single atomic statement with no PK collision.
- `generation` was advertised as a fencing token but was hard-coded to 1
  and never checked by Release/Renew. Now monotonically increments per
  key, is returned to the caller in a `Lease` value, and is required by
  both Release and Renew.
- Release used to `DELETE` the row, which reset generation to 1 on the
  next acquisition and reopened the fencing race. Release now marks
  `lease_until = 0` so the generation counter is preserved across
  release/reacquire cycles.
- `newLeaseUntil` was computed once before the retry loop, so a waiter
  that waited most of the lease duration got an already-near-expired
  lease. `tryOnce` now recomputes both timestamps on each attempt.
- Retry budget was advertised as "5 minutes" but was 300 × 100ms = 30
  seconds. Replaced with `Acquire(ctx, key)` — caller supplies the wait
  deadline; no hidden hard-coded cap.
- Lease deadlines are stored as INTEGER unix nanoseconds. Ordering is now
  the same in SQL as in Go (variable-width RFC3339Nano strings were
  string-ordered, which is not temporal ordering).
- `Hold(ctx, lease)` handles automatic renewal on a `leaseTime/3`
  cadence so callers do not have to hand-roll a renewal goroutine.

Test coverage uses **two independent `*sql.DB` handles pointed at the same
file** (WAL mode) and `TestDBLocksAcrossOSProcesses`, which launches separate
test processes. It does not rely on two goroutines sharing one handle. Eight
regression tests cover: expired takeover, live-lease
protection, fencing on release, waiter freshness, honest context budget,
Hold renewal past the original lease, and takeover fencing.

The dead `internal/operations/distributed_locks.go` (file-based locks with
no production callers) has been removed to avoid future confusion about
which implementation to reach for.

**Adversarial Test Scenarios:**
```
Test 1: restore + delete simultaneously
  → One must wait or one must rollback

Test 2: deploy + restore simultaneously
  → Files must not be mixed

Test 3: route update + termination simultaneously
  → Route state must be consistent

Test 4: resource update + suspension simultaneously
  → No partial application

Test 5: backup + filesystem restore simultaneously
  → Backup must see consistent state
```

**Evidence currently available:**

- `TestAdversarialMutationLockScenariosSerializeAcrossConnections` exercises
  all five lock-key combinations with two independent SQLite connections.
- `TestSiteMutationLockSerializesRealHelperBoundaryAcrossApps` runs two
  independent app instances through the same helper boundary and verifies
  that their critical sections do not overlap.
- `TestSiteMutationLockLossTerminatesInFlightHelper` starts a real long-running
  helper, fences its durable lease from an independent database connection,
  and verifies the helper is terminated before it can record completion.
- `DefaultManager.Delete` now checks its operation context before path
  resolution and immediately before filesystem removal; a cancelled-context
  regression test verifies the canonical site remains intact.
- Route publication and deletion now share the site/vhost fenced lock set;
  `TestRouteMutationLockSetSerializesPublicationAndDeletion` verifies that a
  deletion cannot acquire the vhost fence while publication holds it.
- Domain claim and verification now share the site fence with termination, and
  proxy deployment/deletion check the fenced context before reporting helper
  success. Route reconciliation also refuses to persist applied/deleted state
  after the lease is lost.
- Lock-held backup workflows now call `CreateSiteBackupContext`; cancellation
  is checked during archive traversal, file copying, verification, and commit
  so a fenced lease loss cannot publish a backup after takeover.
- Lock-held offsite backup transfers now inherit the operation context, so
  cloud upload/download work is cancelled when the site lease or job context
  is cancelled.
- App deployment/actions and containerized runner completion now check the
  fenced operation context before durable completion records, preventing a
  lost lease from being reported as a successful mutation.
- SSH access and environment desired-state workflows now reject a fenced
  operation before persistence and retain pending state when cancellation is
  observed after helper execution, allowing reconciliation to repair host
  state instead of recording false success.
- Scheduled task and resource-profile workflows now reject fenced operations
  before desired-state persistence and leave pending state when helper work
  completes after cancellation.
- PHP and Python runtime/application workflows now use the same pending-state
  fallback around helper completion and reject lease loss before durable
  success is recorded.
- Worker lifecycle, creation, and removal workflows now reject lease loss
  before durable transitions and retain pending state for reconciliation.
- Node tooling, WordPress, Composer, and `.htaccess` workflows now check the
  fenced context both before helper execution and before reporting completion.
- Database provisioning, credential rotation, deletion, Redis desired-state
  updates, account suspension, release activation, staging publication, and
  task reconciliation now reject lease loss at their helper-to-state boundary.
- Account creation, plan/site assignment, deletion, password/MFA changes,
  recovery actions, and session revocation now use the account fence; account
  creation also fences site resource-profile enforcement.
- API-token creation/revocation, backup schedule changes, webhook
  configuration, and Git deployment/rollback now use their account/site fence
  and check lease loss before reporting completion.
- Environment reconciliation now retains the item as pending when its helper
  completes after the site lease is lost.
- Route, PHP/Python runtime, resource, task, and worker reconciliation now
  re-check the lease after the final applied-state write and restore pending
  state when cancellation is observed at that boundary.
- Route deletion now leaves the desired route pending with an error when the
  lease is lost after the webserver helper returns, instead of removing the
  durable desired state.

These tests prove lock acquisition, helper serialization, and the delete
cancellation boundary. They do not yet prove that every full workflow remains
consistent after a conflicting operation is interrupted, so the operation-level
acceptance item remains open.

**Acceptance Criteria:**
- [x] Distributed lock acquired before each currently implemented mutation
- [x] Lock held until operation completes or rolls back
- [x] Lock-key serialization tests pass for the 5 scenarios above — site_lock_workflows_test.go
- [x] No race condition bugs after concurrent operations — verified through workflow tests
- [x] Full conflicting workflows remain in a known good state after interruption — `workflow_interruption_test.go` drives the real workflows: backup interrupted at init/archive/verify/commit then file restore; file restore interrupted at verify/extract/activate/commit then site deletion; release activation interrupted then restore; resource update interrupted mid-enforcement then account suspension then reconciliation; and a pending route update with termination interrupted at each of its nine journaled steps then resumed. Each scenario asserts exact site contents, no leftover staging, terminal recovery journals, no partial backups, and continued usability

---

## Gate 3: Capability Reporting is Brutally Accurate

**Requirement:** Capabilities reflect usable workflows, not binary existence

**What this means:**
```
WRONG:
  "database_restore": {
    "available": true,
    "reason": "mysql binary found"
  }

RIGHT:
  "database_restore": {
    "available": true,
    "mode": "available",
    "reason": "Managed DB helper provisions, restores, verifies inventory, and rolls back on staged-import failure when credentials are supplied."
  }
```

**Current Status:**
- ✅ CapabilityMode enum implemented
- ✅ Database restoration reports as available only when the managed DB helper is executable
- ✅ All capabilities updated to report mode
- ✅ Audit: capability probes verify the managed DB helper dependency before reporting availability

**Acceptance Criteria:**
- [x] All capabilities report mode (not just available/unavailable)
- [x] Mode accurately reflects operation readiness
- [x] No "available: true" for operations not yet implemented
- [x] Documentation matches capabilities (capabilities verify their dependencies)

---

## Gate 4: Automated Archive Database Restoration

**Requirement:** Database restoration is transactional end-to-end

**What this means:**
```
1. Create database (rollback point)
2. Restore SQL dump
3. Validate table structure
4. Rewrite configuration with new credentials
5. Health check (verify connectivity)
6. Commit state

Failure at any step:
  - Drop created database
  - Restore previous site files
  - Rollback application configuration
  - Operator sees "restoration failed" not "partial state"
```

**Current Status:**
- ✅ Archive import checks disk space
- ✅ Recovery journal initialized
- ✅ Automatic restoration is transactional within staged import when a valid database password is supplied; the no-credential path remains explicit manual follow-up.

**Implementation Status:**
1. ✅ ArchiveImportRequest accepts opt-in automatic-restore credentials
2. ✅ The control plane injects the existing managed DB helper adapter
3. ✅ Transactional restoration runs before staged site activation
4. ✅ Provisioned database cleanup is retained until activation commits
5. ✅ Capabilities probe the executable DB helper before reporting availability

**Acceptance Criteria:**
- [x] Database restoration is transactional within the staged import
- [x] Failure rolls back the provisioned managed database and staged site
- [x] Config is rewritten with actual credentials for the automatic path
- [x] Managed inventory verification passes before success
- [x] Capability reports availability only when the DB helper dependency exists

---

## Gate 5: Failure Injection Testing

**Requirement:** StePanel survives and recovers from failure at every step

**What this means:**
```
For each critical operation (backup, restore, deploy, terminate):
  Test failure at:
    - Job start
    - 25% through
    - 50% through
    - 75% through
    - After success but before commit
    
  Then restart worker and verify:
    - Operation completes OR rolls back (never leaves half-state)
    - Operator can see what happened
    - Retry works
```

**Test Framework:**
```go
// $STEPANEL_FAIL_AT="restore:commit" injects a deterministic error.
// The current hooks cover backup init/archive/verify/commit, restore
// verification/extraction/activation/database/commit, termination init and
// each journal boundary (backup/database/routes/proxies/tasks/services/
// site-state/ownership), deployment activation, account suspension before
// persistence, and transaction init/commit.
failureInjection("restore", "commit")
```

Subprocess regression drills now cover termination-journal persistence,
filesystem transaction recovery, and release activation recovery: a child
process is killed with `SIGKILL` after a destructive/replacement step starts,
and the parent reloads the state, restores the original site/release, and
completes recovery. These prove cross-process persistence for those recovery
primitives, but do not replace the still-open multi-point host-kill/restart
matrix below.

**Critical Operations to Test:**
1. Backup (boundary tests: init, archive, verify, commit; hosted worker-kill/restart drill passes)
2. Restore (boundary tests: verify, extract, activate, database, commit; hosted worker-kill/restart drill passes for file restore)
3. Deploy (hosted kill/restart at activation passes; clone/build/health-check interruption remains)
4. Terminate (hosted worker kill/restart during site-state removal passes; interruptions during backup and cleanup remain)
5. Account suspension (hosted panel kill/restart after persistence passes; external helper and earlier-stage interruptions remain)

**Evidence currently available:** hosted installation smoke run
[38238726272](https://github.com/cyberducttape/StePanel/actions/runs/38238726272)
passes on both AlmaLinux 9 and Rocky Linux 9 for the default recovery drills
and an expanded alternate kill-boundary matrix covering cpmove import, backup
verification, file-restore commit, termination, account suspension, and Git
deployment activation. The deployment drill verifies that startup recovery
restores the original site content and clears the activation journal. The
repository
recovery-drill harness passes partial SQL import cleanup, interrupted
transaction recovery, configuration
rollback, and pending runtime reconciliation. Its generated results explicitly
exclude power-loss recovery. A disposable Rocky Linux 9.8 VM also passed the
installed-host cpmove, backup, file-restore, termination, suspension, and deploy
recovery drills. Abruptly killing the QEMU process and rebooting the guest left
systemd healthy, the panel and worker units active, and `/readyz` reporting ready;
stopping/restarting MariaDB separately left the control plane healthy. This is
limited VM evidence, not a host power-loss or disk-exhaustion test. Local SIGKILL
regression tests now cover termination journals, filesystem restore
transactions, Git release activation, durable account suspension state, backup
staging cleanup, and managed-database journal cleanup. The installer was also
corrected to handle images that already have full `curl` installed and to
install the Git runtime required for deploys; the hosted recovery smoke passes
with those fixes on both distributions. The five-operation acceptance criteria
remain open for multi-point failure injection, real disk-exhaustion, actual
host power-loss, and broader workload and recovery-time evidence.

The newer [2026-10-11 hosted installation smoke](./lab-results/2026-10-11-hosted-install-smoke.md)
passed the default installed-host recovery matrix once each on Rocky Linux 9
and AlmaLinux 9 at commit `a0c0585`. This is privileged systemd-container
evidence: it covers same-site replacement import, SFTP, backup/file/database
restore, termination, suspension, and deployment interruption, but does not
replace repeated KVM/VM matrix runs, physical power-loss testing, or ENOSPC
outside backup creation.

**KVM certification runs:** a 2026-10-10 Rocky Linux 9 KVM guest passed the
installation and full 107-pass recovery matrix once; this run did not execute
the separate abrupt-loss or ENOSPC phases. See
[lab-results/2026-10-10-rocky9-kvm-full-matrix.md](./lab-results/2026-10-10-rocky9-kvm-full-matrix.md).
The 2026-10-09 Rocky Linux 9 KVM guest with
SELinux enforcing passed the install smoke, the bounded matrix (57 drills),
and the full recovery matrix (105 drills) killing every supported journal
boundary of cpmove activation, backup, restore, termination, and suspension.
Killing the QEMU process left the guest recoverable both idle (`/readyz` ready
in 23 s including boot) and mid-backup (the job completed and the backup
verified, with no staging left behind). A backup hitting real ENOSPC on a
dedicated filesystem failed without publishing anything partial. The run also
found and fixed five defects invisible to container CI. It is one run, a QEMU
kill is not physical power loss, and ENOSPC was exercised only for backup;
the `restore:provision`/`restore:provisioned` real-host drill was added to the
full-matrix harness after this run and has not yet been executed. See
[lab-results/2026-10-09-rocky9-kvm-certification.md](./lab-results/2026-10-09-rocky9-kvm-certification.md).

The 2026-10-12 Rocky Linux 9 KVM run adds one full 108-check install/matrix
pass, idle and mid-backup abrupt guest-loss recovery, and real backup/file-
restore ENOSPC evidence. Its initial runner phase failed because SELinux
relabeling was attempted on a root-owned build-script bind mount; the
site-owned/read-only staging fix then passed runner and release-pipeline
retests on the same guest. Because the original full run records that failure
and the fix retest used the existing guest, this is not a clean all-green
full-run rerun. The 20–50× matrix, physical power loss, and other ENOSPC
boundaries remain open. See
[lab-results/2026-10-12-rocky9-kvm-certification.md](./lab-results/2026-10-12-rocky9-kvm-certification.md).

The installed systemd units now enable a root-owned, peer-authorized Unix
socket broker for host mutations. The panel and worker use
`NoNewPrivileges=true`, `RestrictSUIDSGID=true`, `PrivateDevices=true`,
`ProtectHome=true`, `ProtectProc=invisible`, and strict writable-path limits;
the broker is the only root process and has an explicit host-management
write-path allowlist. The hosted install smoke asserts the enabled protections
and runs the recovery suite under them. Existing installations must be
upgraded to remove the transitional sudo policy.

The transient rootless build service uses a closed device policy with access
only to `/dev/fuse`, required by the unprivileged overlay storage driver. The
panel and worker services retain `PrivateDevices=true`. The privileged broker
uses `DevicePolicy=closed` without `CAP_MKNOD`, `CAP_SYS_RAWIO` or `@raw-io`
system calls instead of a private `/dev`. The quota tools it runs for site and
Git staging quotas need to resolve the block device behind `/var/www`, and the
closed device policy still refuses to open it.

**Acceptance Criteria:**
- [x] Failure injection framework implemented at transaction init/commit
- [x] Boundary-level failure tests cover all 5 operations
- [x] Multi-point process-kill/restart drills cover all 5 operations at repository level — `crash_drill_test.go` kills a child process with SIGKILL at every instrumented boundary of backup (4), file restore (4), termination (9), and account suspension (2), then runs the production startup recovery (`recoverUncleanShutdown`) or resumes the durable job and verifies the final state; deploy activation is covered by `TestReleaseActivationRecoversAfterProcessKill`. Hosted installed-host runs remain the evidence for real services
- [x] No mysterious half-states discovered — the drills found two real leaks, both fixed: restore scratch trees under the import root and manager staging trees were never cleaned after a crash
- [x] Recovery is deterministic at repository level — 114 consecutive SIGKILL drills (6 repetitions of all 19 kill points) recovered to the same verified state
- [ ] Real disk exhaustion (ENOSPC) and host power loss on a disposable VM (partial: backup and file-restore ENOSPC passed on the 2026-10-24 Rocky 9 KVM run; physical host power loss, publication/import/database-restore ENOSPC, and broader host exhaustion remain. See the [2026-10-24 full recovery report](lab-results/2026-10-24-rocky9-kvm-full-recovery.md), [2026-10-12 Rocky run](lab-results/2026-10-12-rocky9-kvm-certification.md), and [2026-10-14 Ubuntu run](lab-results/2026-10-14-quota-install-smoke.md))
- [ ] Repeat the real-host recovery matrix 20–50 times per operation/fault
  combination, with post-boot invariant evidence retained (see
  `docs/REAL_HOST_FAILURE_MATRIX.md`)

---

## v1.0.0 Release Checklist

### Architecture
- [x] Gate 1: One Lifecycle Authority - ALL mutations through SiteManager (VERIFIED: 8/8 CRITICAL + 3/3 HIGH files compliant)
- [x] Gate 2: Cross-Process Locks - Lock layer enforced; interrupted-workflow acceptance covered by `workflow_interruption_test.go`
- [x] Gate 3: Accurate Capabilities - No "available: true" for unimplemented
- [x] Gate 4: Automated DB Restoration - Transactional end-to-end
- [ ] Gate 5: Failure Injection - Survives failure at every step (repository-level kill and failure matrix complete; one Rocky 9 installed-host matrix and targeted abrupt-loss scenarios passed; 20–50 repeats, additional platform cohorts, broader ENOSPC, and physical host power-loss evidence remain)

### Testing
- [x] Adversarial concurrency tests (5 scenarios) — the five conflicting lock pairs across two SQLite connections (`TestAdversarialMutationLockScenariosSerializeAcrossConnections`) and across OS processes (`TestDBLocksAcrossOSProcesses`); the real restore, termination, resource-update, and suspension entry points are proven to wait for a lock held by another process without mutating (`cross_process_workflow_test.go`); deploy and route updates are covered at the lock-key level
- [ ] Failure injection tests (all critical paths)
- [ ] Disposable host integration tests (real OS, full workflow)
- [ ] Production load simulation (concurrent operations)

Initial synthetic SQLite microbenchmarks now cover 10/100/500-site mutation
maps, 10 concurrent mutators, panel/worker connection-pool contention, two
actual concurrent test processes, 50-job backup/restore batches, and
audit-heavy writes ([LOAD_BASELINE_2026-09-29.md](./LOAD_BASELINE_2026-09-29.md)).
A mixed HTTP workload now runs in CI on every push: a real `stepanel` process
with 200 sites, 8 concurrent readers across 12 endpoints, and 20 real backup
jobs running at the same time, failing on any error, unfinished job, or p95
above 500 ms ([HTTP_LOAD_BASELINE_2026-10-02.md](./HTTP_LOAD_BASELINE_2026-10-02.md)).
It found and fixed a quadratic site-overview scan and an uncoordinated
service-status refresh. This still does not satisfy the production load
gate: it is not representative production hardware, real web servers and
databases, or more than a few hundred sites.

### Documentation
- [ ] v1.0.0 Production Gates (this doc)
- [ ] Architecture diagrams updated
- [ ] Capability documentation accurate
- [ ] Recovery procedures documented

### Quality
- [x] No TODO comments in critical paths (`TestNoDeferredWorkMarkersInCriticalPaths` guards production Go code, helpers, and the installer)
- [x] Error categorization deployed (Persistence/Corruption/Temporary/Cleanup): durable job persistence failures (temporary when SQLite is busy), quarantined recovery journals, and periodic cleanup failures feed `stepanel_state_errors_total`
- [x] Audit trail verified at every mutation (`TestEveryMutatingRouteIsAudited`: every mutating route is behind the fail-closed `Auth.Require` audit or is a reviewed self-auditing route; webhook deploys now audit before mutating)
- [ ] Operational visibility complete

---

## Success Criteria

After v1.0.0 is released, the following should be true:

1. **Can run 100+ production sites** - Proven by integration tests on multiple OS versions
2. **No mysterious failures** - Every failure path tested and recoverable
3. **Operators trust it** - Features work as documented; capabilities are accurate
4. **Incident response is clear** - Failed operations leave auditable state
5. **Concurrent safety proven** - Adversarial tests pass consistently
6. **Recovery is fast** - Can restore from any failure in <5 minutes

---

## Timeline Estimate

| Component | Effort | Dependency |
|-----------|--------|------------|
| Gate 1 (Lifecycle Authority) | 2-3 days | None |
| Gate 2 (Distributed Locks) | 3-4 days | Gate 1 |
| Gate 3 (Capability Accuracy) | 1 day | None (mostly done) |
| Gate 4 (DB Restoration) | 2-3 days | None |
| Gate 5 (Failure Injection) | 3-4 days | Gate 1 + Gate 2 |
| Integration Tests | 2-3 days | All gates |
| **Total** | **14-18 days** | **Sequential** |

---

## What v1.0.0 is NOT

- ❌ Not adding 50 new features
- ❌ Not replacing the UI
- ❌ Not implementing multi-tenant SaaS
- ❌ Not high availability, multi-host, or multi-region operation
- ❌ Not SLA monitoring/escalation, automated incident response, or enterprise support tiers

Those belong to the 2.0 shared-hosting platform in [ROADMAP.md](./ROADMAP.md).
Adding them to the 1.0 scope requires changing this document and the roadmap
together.
- ❌ Not competing with cPanel on breadth

## What v1.0.0 IS

- ✅ Complete architectural contracts
- ✅ Bulletproof failure handling
- ✅ Provable cross-process safety
- ✅ Production-hardened hosting control panel
