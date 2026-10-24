# StePanel Project Status

**Version:** v0.7.0 (Operator Beta)  
**Last Updated:** 2026-10-24
**Status:** Operator Beta; not approved for v1.0 production release

---

### Latest validation — 2026-10-24 (`47ba2ff`)

- Current source is
  [`47ba2ffe88abb1436da38b28fd5a856494614133`](https://github.com/cyberducttape/StePanel/commit/47ba2ffe88abb1436da38b28fd5a856494614133).
  It fixes a Staticcheck `ST1008` finding in the vsftpd validation test
  helper. The focused test and Staticcheck pass locally. Exact-source CI
  ([run 40119821255](https://github.com/cyberducttape/StePanel/actions/runs/40119821255))
  passed all checks: Go tests and coverage floors, race tests, recovery and
  ENOSPC drills, database restore, static analysis, packaging and container
  smoke, and UI end-to-end. Documentation and CodeQL passed on evidence commit
  `e15a284` ([Documentation run
  40121256032](https://github.com/cyberducttape/StePanel/actions/runs/40121256032),
  [CodeQL run 40121256049](https://github.com/cyberducttape/StePanel/actions/runs/40121256049));
  CodeQL reports no open alerts. The prior integration run on `5e9077b` failed only at Staticcheck
  on this finding ([run 40118239239](https://github.com/cyberducttape/StePanel/actions/runs/40118239239));
  its tests, coverage, race, recovery, ENOSPC, and database restore steps
  preceding static analysis passed.
- A clean-tree Rocky 9 KVM run on `1b2e99d` passed guest boot and verified
  `/var/www` user/project quotas. Native installation and installed-host
  checks passed for cPanel import/re-import, SFTP and FTPS access, database
  locking, interrupted import/backup/file restore/database restore/termination,
  account suspension, Git activation recovery, and app lifecycle. The installed
  WordPress recovery proof passed with archive, database, and application
  checks and a measured 15,524 ms RTO; live database and root-broker outage
  checks also recovered. Its extended recovery-boundary matrix is now running
  (local artifacts: `/var/tmp/stepanel-kvm-cert-20261024-1b2e99d`; checkpoint
  [report](lab-results/2026-10-24-rocky9-installed-host-recovery.md)).
  Integration CI for fixture commit `1b2e99d` was superseded and cancelled
  while race tests were running ([run
  40119585611](https://github.com/cyberducttape/StePanel/actions/runs/40119585611));
  its Go tests and coverage had passed. The current-source integration run
  above includes the static-analysis correction.
- The six-host installed recovery matrix on source `5e9077b` remains active
  ([run 40118708094](https://github.com/cyberducttape/StePanel/actions/runs/40118708094));
  no host result is counted as a pass before the matrix completes.
- Production remains **NO-GO**. The 20–50 repeat cohort, physical power-loss
  recovery, broader ENOSPC/database-outage cases, full supported-runtime
  coverage, capacity certification, live alert outage/retry evidence, upgrade
  policy, and independent security assessment remain open.

### Previous validation — 2026-10-24 (`5e9077b`)

- Application source is
  [`1af5fc05e0f3ab39b0da974651ca4b081966a193`](https://github.com/cyberducttape/StePanel/commit/1af5fc05e0f3ab39b0da974651ca4b081966a193).
  Integration CI ([run 40113614938](https://github.com/cyberducttape/StePanel/actions/runs/40113614938)),
  Documentation ([run 40113614962](https://github.com/cyberducttape/StePanel/actions/runs/40113614962)),
  and CodeQL ([run 40113614960](https://github.com/cyberducttape/StePanel/actions/runs/40113614960))
  passed on this source. CodeQL and Dependabot currently report no open alerts.
- One synthetic 500-site mixed HTTP run passed on Ubuntu 26.04.1 with p95
  296.7 ms across 500 requests and 20 completed backup jobs. The host is
  outside the supported OS matrix and resource use was not monitored; this
  does not certify capacity. See the
  [benchmark record](lab-results/2026-10-24-500-site-mixed-load.md).
- The six-platform installed-host recovery matrix is active on this source
  ([run 40114439549](https://github.com/cyberducttape/StePanel/actions/runs/40114439549));
  no result is counted until the complete matrix succeeds. The current-source
  N-1 upgrade/rollback smoke passed ([run 40115776818](https://github.com/cyberducttape/StePanel/actions/runs/40115776818));
  it uses a compatibility-adjusted v0.6.0 fixture and is not a full supported
  upgrade-path certification. See the
  [upgrade evidence report](lab-results/2026-10-24-n-minus-one-upgrade-40115776818.md).
- The quota-enabled Ubuntu installed-host job ([run 39912326883](https://github.com/cyberducttape/StePanel/actions/runs/39912326883))
  passed its rootless Podman runner permission smoke, including pinned-image
  pull, image inspection, repeat build, failure retention, and artifact cleanup.
  That run used source `67e67d6`; the runner helper, runner smoke, and quota
  workflow are unchanged through current application source `1af5fc0`. This
  closes the previously observed Podman `image inspect` failure for that
  tested runner path, but does not establish a full current-source install or
  cross-distro pass. See the
  [scoped result](lab-results/2026-10-24-rootless-runner-smoke-39912326883.md).
- The separate Rocky 9 KVM full recovery and ENOSPC evidence uses application
  source `b20c609`, not this source; see
  [its report](lab-results/2026-10-24-rocky9-kvm-full-recovery.md).
- Production remains **NO-GO**. The required 20–50 installed-host repetitions,
  physical power-loss recovery, broader ENOSPC/database-outage cases, full
  supported-runtime coverage, certified capacity, live alert outage/retry
  evidence, upgrade policy, and independent security assessment remain open.
  See [V1_PRODUCTION_GATES.md](V1_PRODUCTION_GATES.md).

## Executive Summary

StePanel v0.7.0 is in Operator Beta — suitable for controlled single-host deployments with experienced operators. The product is **functionally complete** for its stated feature set. The remaining work before v1.0 is **proving production durability and failure recovery** rather than building new features.

**Release Approval:** Governed by [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md) — the sole authoritative release-gate document.

### Current validation — 2026-10-24 (`06ae561`)

- The latest application source is
  [`06ae561a59a4a677a98e95efa6edf1cddbc6bfb5`](https://github.com/cyberducttape/StePanel/commit/06ae561a59a4a677a98e95efa6edf1cddbc6bfb5).
  It prevents durable job-list queries from replacing newer in-process worker
  lease snapshots. The regression test, full root-package Go suite, and
  targeted race-enabled tests passed locally.
- Exact-source documentation checks passed (run
  [40084549107](https://github.com/cyberducttape/StePanel/actions/runs/40084549107));
  CodeQL passed (run
  [40084549111](https://github.com/cyberducttape/StePanel/actions/runs/40084549111))
  with zero open code-scanning alerts. Exact-source integration CI
  ([40084549149](https://github.com/cyberducttape/StePanel/actions/runs/40084549149))
  passed in full: Go 1.26.10 tests and coverage thresholds, race tests,
  recovery and real tmpfs ENOSPC drills, DB restore checks, clean Python
  deployment, static analysis, container and deployment validation, mixed
  load, and UI end-to-end/accessibility tests. Documentation checks
  ([40084769418](https://github.com/cyberducttape/StePanel/actions/runs/40084769418))
  and CodeQL ([40084769384](https://github.com/cyberducttape/StePanel/actions/runs/40084769384))
  also passed on evidence commit `d2131f2`; the alert query reports zero open
  alerts.
- The six-host recovery run
  ([40083936175](https://github.com/cyberducttape/StePanel/actions/runs/40083936175))
  is testing source `b448cc9`, before this cache-listing fix. The local Rocky
  Linux 9 KVM matrix uses older source `2dead8a`. Both remain in progress and
  neither counts as exact-source recovery evidence for `06ae561`.
- Production remains **NO-GO**. The release gates for repeated installed-host
  certification, power-loss and broader host ENOSPC, capacity, supported
  platform application recovery, live alert outage/retry evidence,
  upgrade/rollback policy, and independent security assessment remain open.

### Previous completed validation — 2026-10-24 (`b448cc9`)

- Exact-head integration CI passed on
  [`b448cc97c2cf74085d77fde48854822a44c30625`](https://github.com/cyberducttape/StePanel/actions/runs/40082479635): Go 1.26.10 formatting, release checks, vet, full tests and coverage floors, race tests, recovery drills, real tmpfs ENOSPC recovery, build/startup/authenticated API, mixed HTTP load, database restore checks, static analysis, and container build/scan. UI end-to-end and accessibility checks passed in the same workflow.
- Exact-head CodeQL passed ([run 40082479642](https://github.com/cyberducttape/StePanel/actions/runs/40082479642)); the current repository alert query reports zero open code-scanning alerts. Documentation link and source-reference checks passed ([run 40082479599](https://github.com/cyberducttape/StePanel/actions/runs/40082479599)).
- The commit fixes a reproduced race in durable job reads: a delayed SQLite snapshot could replace a newer in-process worker lease. A targeted regression test and 30 repeated race-enabled reproductions passed locally; the full Go and race suites passed locally and in exact-head CI.
- An installed Rocky Linux 9 KVM run is active against the preceding application source `2dead8a`, not `b448cc9`. It has passed the default interruption smokes and is running the opt-in full recovery-boundary matrix; no result from this in-progress run is counted as certification for `b448cc9`.
- Production remains **NO-GO**. The current CI results do not close the release gates for repeated installed-host certification, physical power loss and broader host-level ENOSPC, measured capacity, supported-platform application proof, live notification outage/retry evidence, upgrade/rollback policy, or independent security assessment. See [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md).

### Current release evidence — 2026-10-24 (evidence source `4e15d70`, application source `a48aafc`)

- Evidence update is `4e15d70b2b84b5027a0e14b4185af5226df00fa9`; the latest
  application and installed-host smoke source is `a48aafc2bfed8072e4be47878aaa98e5a835a22b`.
  Integration CI for application source `dc25ddc` passed in full, including Go
  tests and coverage floors, race/recovery/ENOSPC drills, MySQL and PostgreSQL
  restore checks, clean Python deployment, static analysis, container/Helm/
  Terraform validation, vulnerability scanning, and UI E2E
  ([run 40077749788](https://github.com/cyberducttape/StePanel/actions/runs/40077749788)).
  Go application code is unchanged through the installed-host smoke source
  `a48aafc`. Documentation validation passed on evidence commit `4e15d70`
  ([run 40080603311](https://github.com/cyberducttape/StePanel/actions/runs/40080603311));
  CodeQL also passed on that commit ([run 40080603276](https://github.com/cyberducttape/StePanel/actions/runs/40080603276)); the current GitHub code-scanning alert query reports zero open alerts.
  A focused exact-source Rocky Linux 9 KVM run verified the quota-enabled
  `/var/www` mount and passed real ext4 ENOSPC backup and file-restore tests;
  see [the result record](lab-results/2026-10-24-rocky9-kvm-enospc.md).
  The smoke resolves the public root through the installed site-location
  library.
- The six-host run at source `e94f870` ([run 39960010260](https://github.com/cyberducttape/StePanel/actions/runs/39960010260))
  failed at a false-negative source-path assertion: imported sites can live
  under `/home`, while the smoke checked only `/var/www/sites`. Artifacts show
  the import completed and the interrupted staging-site transaction was
  recovered. The corrected root resolution has not yet passed an installed-host
  run. The old-source scheduled run 40076789043 has four host passes and two
  Ubuntu host jobs still in progress. The corrected six-host smoke for
  `a48aafc` (40079073573) has all six host jobs still running the installed-host smoke;
  its CI integration (40079061542) is pending. Neither counts as passing
  evidence.
- Production remains **NO-GO** pending the host recovery matrix and the other
  gates in [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md).

### Prior release evidence — 2026-10-23 07:40 UTC (`64b4a39`)

- Application and smoke-harness source under test is `ea69e24`; current `main`
  at `64b4a39` adds evidence documentation only. Documentation validation
  ([run 39917734607](https://github.com/cyberducttape/StePanel/actions/runs/39917734607))
  and CodeQL ([run 39917734570](https://github.com/cyberducttape/StePanel/actions/runs/39917734570))
  passed on current `main`, with zero open CodeQL alerts.
- A fresh integration CI run at `053ac62` passed in full
  ([run 39917250272](https://github.com/cyberducttape/StePanel/actions/runs/39917250272)),
  including Go tests and coverage floors, race tests, recovery and ENOSPC
  drills, database restore checks, static analysis, container validation, and
  UI E2E. Its application source is `ea69e24`, matching current application
  code; the run used an isolated evidence branch. The earlier `ea69e24` run
  remains workflow-stuck and is not counted.
- The quota-enabled Ubuntu installation job completed successfully
  ([job 120277924484](https://github.com/cyberducttape/StePanel/actions/runs/39912326883/jobs/120277924484))
  and its workflow finalized successfully
  ([run 39912326883](https://github.com/cyberducttape/StePanel/actions/runs/39912326883))
  on older application source `67e67d6`; this does not certify current source.
  The corrected six-host full recovery
  run on `ea69e24` remains active on all six hosts ([run 39915109037](https://github.com/cyberducttape/StePanel/actions/runs/39915109037)).
- Production remains **NO-GO** pending physical host power-loss and broader
  ENOSPC/database-outage evidence, repeated recovery, supported-webserver
  application proof, alert-delivery evidence, capacity limits, upgrade policy,
  and independent security assessment.

### Prior release evidence — 2026-10-23 07:23 UTC (`053ac62`)

- The application and smoke-harness revision under assessment is
  `ea69e24a1b5b4cd5fd0594f016a819954a8a1517`. The only code-bearing change
  since application source `cac6c5a` is a bounded one-time HTTP 401
  retry in the installed-host backup-recovery smoke, using a fresh TOTP
  counter. Documentation CI ([run 39914766323](https://github.com/cyberducttape/StePanel/actions/runs/39914766323))
  and CodeQL ([run 39914766222](https://github.com/cyberducttape/StePanel/actions/runs/39914766222))
  passed on HEAD; CodeQL reports zero open alerts. Integration CI
  ([run 39914766303](https://github.com/cyberducttape/StePanel/actions/runs/39914766303))
  is running race tests after its Go test step; it is not yet complete.
- The six-host full-recovery run against `cac6c5a`
  ([run 39910599138](https://github.com/cyberducttape/StePanel/actions/runs/39910599138))
  remains active; the Rocky Linux 9 + Caddy shard failed with HTTP 401 at
  backup-recovery login after the preceding cPanel smoke passed. Logs show a
  successful login at 06:56:07 and the later 401 at 06:56:32 UTC, but do not
  identify the authentication failure reason. A TOTP counter/replay interaction
  is plausible, not confirmed. Other shards remain in progress. The corrected six-host
  run ([run 39915109037](https://github.com/cyberducttape/StePanel/actions/runs/39915109037))
  is queued and provides no pass evidence yet. Quota-enabled install run
  [39912326883](https://github.com/cyberducttape/StePanel/actions/runs/39912326883)
  remains in progress on older source `67e67d6`.
- Production remains **NO-GO**: physical host power-loss recovery, broader
  installed-host ENOSPC/database-outage evidence, 20-repeat recovery cohort,
  supported-webserver application proof, live alert-delivery outage/retry
  evidence, measured capacity limits, upgrade/rollback policy, and independent
  security review are still open.

### Prior exact-source validation snapshot — 2026-10-23 06:45 UTC (`cac6c5a`)

- Exact-head integration CI passed on
  [`cac6c5adc13acb77a99dda080bfa9b9e7d6ccd35`](https://github.com/cyberducttape/StePanel/actions/runs/39908676803): Go 1.26.10 tests and coverage floors, race suite, repository recovery and tmpfs ENOSPC drills, MySQL/PostgreSQL restore checks, static and vulnerability analysis, container build/scan, deployment validation, and UI E2E all passed. Exact-head CodeQL ([run 39908676811](https://github.com/cyberducttape/StePanel/actions/runs/39908676811)) and documentation validation ([run 39908676870](https://github.com/cyberducttape/StePanel/actions/runs/39908676870)) passed; CodeQL reported zero open alerts. Local `go test ./...`, `go vet ./...`, `make release-check`, shell syntax, and diff checks passed.
- An N-1 upgrade/rollback smoke passed on code ancestor `add8dae` ([run 39907581022](https://github.com/cyberducttape/StePanel/actions/runs/39907581022)); it covered an Ubuntu disposable systemd host and preserved operator-managed runtime settings. It is useful upgrade evidence, not a supported version-path matrix or release rollback certification.
- The required three-process import-stage cleanup regression passed ten additional repetitions under `go test -race` on this application source (12.191s).
- A fresh full installed-host recovery run against exact source `cac6c5a` is pending: [run 39910599138](https://github.com/cyberducttape/StePanel/actions/runs/39910599138), one iteration across Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with Apache and Caddy. It is not a pass until every host job succeeds. Ten earlier two-iteration shards ([01](https://github.com/cyberducttape/StePanel/actions/runs/39903141857), [02](https://github.com/cyberducttape/StePanel/actions/runs/39903141679), [03](https://github.com/cyberducttape/StePanel/actions/runs/39903141803), [04](https://github.com/cyberducttape/StePanel/actions/runs/39903141630), [05](https://github.com/cyberducttape/StePanel/actions/runs/39903141759), [06](https://github.com/cyberducttape/StePanel/actions/runs/39903141819), [07](https://github.com/cyberducttape/StePanel/actions/runs/39903141828), [08](https://github.com/cyberducttape/StePanel/actions/runs/39903141959), [09](https://github.com/cyberducttape/StePanel/actions/runs/39903141732), [10](https://github.com/cyberducttape/StePanel/actions/runs/39903141666)) use older application source `84e4286` and remain queued; they do not certify the current source.
- Production remains **NO-GO**. Physical host power-loss recovery, broader installed-host ENOSPC/database-outage evidence, the required 20-repeat recovery cohort, complete supported-webserver application proof, live alert-delivery outage/retry evidence, measured capacity limits, upgrade/rollback policy, and independent security review remain open.

### Exact-source validation snapshot — 2026-10-23 04:37 UTC (`03422e5`)

- At snapshot time, HEAD was `03422e5b8a254de6079d5281ddccad0f82ecd2b5`,
  containing only release-evidence documentation/changelog changes over the
  application code in `cf079a12dc96c63991f72fa23edd9bc5939cf188`
  (`cf079a1`). Documentation validation passed on that snapshot
  ([run 39901196396](https://github.com/cyberducttape/StePanel/actions/runs/39901196396));
  CodeQL was running at capture but was canceled by the subsequent
  documentation-only push
  ([run 39901196497](https://github.com/cyberducttape/StePanel/actions/runs/39901196497)).
  Focused quota tests, shell syntax checks, `make release-check`, and 20
  race-enabled staging-cleanup repetitions passed locally. The new documentation
  snapshot does not change application code.
- The six-host installed smoke
  ([run 39897561432](https://github.com/cyberducttape/StePanel/actions/runs/39897561432))
  passed on `cf079a1` for Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with
  Apache and Caddy. It is installation-smoke evidence, not repeated full
  recovery certification. Code CI
  ([run 39897457130](https://github.com/cyberducttape/StePanel/actions/runs/39897457130))
  passed on `cf079a1`: Go tests/coverage, race, recovery drills, tmpfs ENOSPC,
  mixed load, database restore checks, static analysis, container smoke, UI E2E,
  and deployment validation all succeeded. The XFS quota rerun
  ([run 39897466684](https://github.com/cyberducttape/StePanel/actions/runs/39897466684))
  remains in progress. The two-repeat full recovery shard
  ([run 39898086088](https://github.com/cyberducttape/StePanel/actions/runs/39898086088))
  and two additional two-repeat shards, E
  ([run 39900804398](https://github.com/cyberducttape/StePanel/actions/runs/39900804398))
  and F
  ([run 39901109050](https://github.com/cyberducttape/StePanel/actions/runs/39901109050)),
  each have one Ubuntu 24.04/Caddy matrix job in progress; their other host
  jobs remain queued. These shards use code-equivalent snapshots of `cf079a1`.
  No shard is a pass until every host job succeeds; the 20–50-repeat gate
  remains open.
- The previous XFS guest run
  ([run 39894302014](https://github.com/cyberducttape/StePanel/actions/runs/39894302014))
  enabled and enforced quotas, then failed because `xfs_quota` status output
  polluted the Git staging helper's numeric project-ID response. `cf079a1`
  fixes that stdout contract and adds regression coverage; only the current
  installed-host rerun can verify the fix.

The following results are from older source commits and are historical context:

- Exact-head integration CI passed on
  `05dc4054306d2fbb8cc54ac634c030f7510427cf`
  ([run 39894297883](https://github.com/cyberducttape/StePanel/actions/runs/39894297883)),
  including formatting, vet, full Go tests and coverage floors, race tests,
  repository recovery drills, real tmpfs ENOSPC, mixed HTTP load, database
  restore checks, vulnerability/static analysis, container/deployment checks,
  and UI E2E. CodeQL ([run 39894297901](https://github.com/cyberducttape/StePanel/actions/runs/39894297901))
  and documentation validation
  ([run 39894297857](https://github.com/cyberducttape/StePanel/actions/runs/39894297857))
  also passed on that exact commit.
- The exact-head six-host installation matrix
  ([run 39889562484](https://github.com/cyberducttape/StePanel/actions/runs/39889562484))
  passed on `899ef70951077302b2c451a782d42218533b95df`; `ffc286a` changes only
  the race-test wait/diagnostic and changelog, so installer/application source
  is identical. All six combinations passed: Ubuntu 24.04, AlmaLinux 9, and
  Rocky Linux 9, each with Apache and Caddy. This is install-smoke evidence,
  not full recovery certification or production support by itself.
- The XFS quota-enabled guest run
  ([run 39894302014](https://github.com/cyberducttape/StePanel/actions/runs/39894302014))
  is running against `05dc405`; its guest install/smoke step has not completed,
  so there is no XFS product result yet. The previous attempt
  ([run 39889565080](https://github.com/cyberducttape/StePanel/actions/runs/39889565080))
  failed before installation because cloud-init used `/bin/sh` with a
  Bash-only conditional; the fixture now uses POSIX syntax and is checked with
  `sh -n`. An older
  ext4 quota-enabled Ubuntu guest run
  ([run 39886014031](https://github.com/cyberducttape/StePanel/actions/runs/39886014031))
  completed successfully on source `7b549a7`: it confirmed active user and
  project quotas on `/var/www`, user quotas on `/home`, exercised a real project
  quota overage, and passed real backup and file-restore ENOSPC tests. It does
  not validate the new shared quota-state helper introduced on `899ef70`, and
  is not current-source evidence.
- Two current-source full-recovery shards, each configured for two iterations
  across all six OS/web-server combinations, are in progress on `05dc405`
  ([shard B](https://github.com/cyberducttape/StePanel/actions/runs/39896093726),
  [shard C](https://github.com/cyberducttape/StePanel/actions/runs/39896095968)).
  They are not passes until every matrix job completes successfully. The older
  two-repeat shard ([run 39886953555](https://github.com/cyberducttape/StePanel/actions/runs/39886953555))
  remains active on `dd89653` and is not current-source evidence.
- Repository CI does not certify host power-loss recovery, the full ENOSPC
  matrix, current-head XFS quotas, repeated installed-host recovery, a measured
  capacity envelope, or the remaining gates. The new full-recovery shards do
  not yet meet the documented 20–50-repeat requirement. Approval remains
  **NO-GO**; see
  [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md).

### Latest code-source repository gate — 2026-10-21 (`d931c6a`)

- The repository production-gate commands passed on exact source
  `d931c6aa9f50194ec8430ac7262853b7900eb1e6`: 57.2% aggregate coverage, all
  package floors, full race suite (root package 480.659 seconds), four recovery
  drills, control-plane benchmarks, mixed HTTP load, and documentation/release
  checks. The first aggregate invocation stopped on a stale staging-method
  documentation reference; after correcting it, the reference check and all
  remaining commands passed. See the
  [exact-source report](lab-results/2026-10-21-repository-gate-d931c6a.md).
- Later workflow/documentation commits do not change Go/application source.
  Exact-head integration CI, CodeQL, and documentation checks all passed on
  `7cd215f8a49863bce4c21fa6f35a8889ade1500c` (runs
  [39541304606](https://github.com/cyberducttape/StePanel/actions/runs/39541304606),
  [39541304596](https://github.com/cyberducttape/StePanel/actions/runs/39541304596),
  and [39541304565](https://github.com/cyberducttape/StePanel/actions/runs/39541304565)).
- The six-combination hosted installation matrix passed on workflow commit
  `c9712da7279533970fe6cd0b5b89b45309e23bb5`, whose application source is
  unchanged from `d931c6a`: Ubuntu 24.04, Rocky Linux 9, and AlmaLinux 9, each
  with Caddy and Apache ([run 39540812270](https://github.com/cyberducttape/StePanel/actions/runs/39540812270)).
  The quota-enabled host run for the same workflow commit also passed its
  `quota-install` job ([run 39540812247](https://github.com/cyberducttape/StePanel/actions/runs/39540812247)).
- A separate disposable Rocky 9 KVM run built from exact source `7cd215f`
  is executing the full recovery matrix once. At 2026-10-21 05:06 UTC, boot
  and the listed cpmove, backup, file/database restore, termination, suspension,
  and deployment worker-interruption cases had passed; the full matrix and
  subsequent power-loss/ENOSPC phases had not completed. This is partial
  evidence, not certification. See the
  [in-progress host evidence record](lab-results/2026-10-21-hosted-smokes-and-kvm-progress.md).
- Production remains **NO-GO** for physical power-loss and repeated installed-host
  recovery, XFS quota behavior, full application restore across supported web
  servers, alert outage/retry, capacity certification, upgrade/rollback policy,
  and independent security review. Repository gate success does not close
  these release gates.

### Previous exact-source repository gate — 2026-10-19 (`c0113dd`)

- `bash scripts/production-gate.sh` passed on exact code source
  `c0113dd4f7da75c3d3624811d813686a6a8589f5`: 57.1% aggregate coverage, every
  package floor, full race suite (root package 472.072 seconds), repository
  recovery drills, benchmarks, mixed HTTP load, and documentation/release
  checks. This commit reserves capacity before off-site archive downloads and
  holds the reservation through staging cleanup. See the
  [exact-source report](lab-results/2026-10-19-repository-production-gate-c0113dd.md).
- At 2026-10-19 05:27 UTC, exact-head [CI](https://github.com/cyberducttape/StePanel/actions/runs/39203702895)
  and [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39203702910)
  were queued; standard [install](https://github.com/cyberducttape/StePanel/actions/runs/39203703063)
  and [quota install](https://github.com/cyberducttape/StePanel/actions/runs/39203702833)
  were pending. These are not passing evidence.
- Production remains **NO-GO** for real-host recovery, quota/XFS, repeated
  installed-host recovery, capacity, live-alert, platform/upgrade, and
  independent-security gates below. Older-source recovery runs do not certify
  this commit.

### Previous exact-source repository gate — 2026-10-19 (`6e8e280`)

The exact-source repository gate passed on `6e8e280`; see its
[run report](lab-results/2026-10-19-repository-production-gate-6e8e280.md).
It is superseded by the `c0113dd` exact-source run above and does not close
hosted or real-host certification gates.

### Previous exact-source repository gate — 2026-10-19 (`6cef641`)

The exact-source repository gate passed on `6cef641`; see its
[run report](lab-results/2026-10-19-repository-production-gate-6cef641.md).
It is superseded by the `6e8e280` exact-source run above and does not close
hosted or real-host certification gates.

### Previous exact-source repository gate — 2026-10-19 (`c291614`)

- `bash scripts/production-gate.sh` passed on verified source
  `c2916141df8553bccd9b181243170639bad59db0`: 57.1% aggregate coverage, all
  package floors, full race suite (root package 473.323 seconds), repository
  recovery drills, benchmarks, mixed HTTP load, and documentation/release
  checks passed. See the
  [exact-source report](lab-results/2026-10-19-repository-production-gate-c291614.md).
- At capture, exact-head CI and CodeQL were queued; standard and quota-enabled
  installation smokes were pending without assigned jobs. None count as
  passing evidence.
- Production remains **NO-GO**: real-host ENOSPC, physical power loss,
  installed-host aggregate quota behavior (including XFS), the 20-repeat
  installed-host recovery cohort, notification outage/retry, capacity
  certification, supported-platform/upgrade policy, and independent security
  review remain open.

### Previous exact-source repository gate — 2026-10-19 (`1ed5e8a`)

- `bash scripts/production-gate.sh` passed on pushed checkout
  `1ed5e8aae4db88173747ad4a5c3c05426b0938e5`: 56.9% aggregate coverage, all
  package floors, full race suite (root package 474.679 seconds), repository
  recovery drills, benchmarks, mixed HTTP load, and documentation/release
  checks passed. See the
  [exact-source report](lab-results/2026-10-19-repository-production-gate-1ed5e8a.md).
- At capture, exact-head CI and CodeQL were queued, while standard and quota-
  enabled installation smokes were pending without assigned jobs. These are
  not passing evidence.
- Production remains **NO-GO** pending installed-host aggregate quota
  validation (including XFS), the 20-repeat recovery cohort, physical power-loss
  and broader ENOSPC evidence, notification outage/retry, capacity
  certification, supported-platform/upgrade policy, and independent security
  review.
- The quota fixture correction passes its repository syntax/feature gate, but
  successful hosted installation and quota enforcement are still unverified.
- The older-source (`633c013`) 20-repeat recovery cohort remains incomplete:
  three of ten six-host shards passed at the latest capture; several shards
  are still active and four remain queued. None of these results certify the
  current source. Repeat-mode shards are now limited to one host combination
  each to prevent future cohorts from flooding the runner pool.

### Previous exact-source repository gate — 2026-10-19 (`91f402d`)

- `bash scripts/production-gate.sh` passed on pushed checkout
  `91f402df34c9393efe50e0c36c63882526819717`: 56.9% aggregate coverage, all
  package floors, full race suite (root package 476.281 seconds), repository
  recovery drills, benchmarks, mixed HTTP load, and documentation/release
  checks passed. See the
  [exact-source report](lab-results/2026-10-19-repository-production-gate-91f402d.md).
- At capture, CodeQL passed, CI was in progress, and standard/quota-enabled
  installation smokes were pending. The last three are not passes.
- Production remains **NO-GO** pending installed-host aggregate quota
  validation (including XFS), the 20-repeat recovery cohort, physical power-loss
  and broader ENOSPC evidence, notification outage/retry, capacity
  certification, supported-platform/upgrade policy, and independent security
  review.
- A prior quota-enabled smoke failed before installation because of a fixture
  `mke2fs` option delimiter. The fixture is corrected, but a successful hosted
  rerun remains necessary; see the
  [failure and correction report](lab-results/2026-10-19-quota-smoke-fixture-failure-39183265054.md).

### Previous exact-source repository gate — 2026-10-19 (`abb4f65`)

- `bash scripts/production-gate.sh` passed on exact checkout
  `abb4f6530038dc98d674d9cd59423a16ce4e26ce`: coverage was 56.9%, all package
  floors passed, and the full race suite passed (root package 474.732 seconds).
  Repository recovery drills, benchmarks, mixed HTTP load, and documentation /
  release checks also passed. See the
  [exact-source report](lab-results/2026-10-19-repository-production-gate-abb4f65.md).
- At capture, exact-head CI and CodeQL were queued; standard and quota-enabled
  installation smokes were pending. These are not passes.
- Production remains **NO-GO** pending installed-host aggregate quota
  validation (including XFS), the 20-repeat recovery cohort, physical power-loss
  and broader ENOSPC evidence, notification outage/retry, capacity
  certification, supported-platform/upgrade policy, and independent security
  review.

### Previous exact-source repository gate — 2026-10-19 (`2ebd28d`)

- `bash scripts/production-gate.sh` passed on exact checkout
  `2ebd28dce1379e9e5a596adfe6ba1c732dde668d`: coverage was 56.9%, all package
  floors passed, and the full race suite passed (root package 475.918 seconds).
  Repository recovery drills, benchmarks, mixed HTTP load, and documentation /
  release checks also passed. See the
  [exact-source report](lab-results/2026-10-19-repository-production-gate-2ebd28d.md).
- At capture, exact-head CI and CodeQL were queued, while standard and quota-
  enabled installation smokes were pending. These are not passes.
- Production remains **NO-GO** pending installed-host aggregate quota
  validation (including XFS), the 20-repeat recovery cohort, physical power-loss
  and broader ENOSPC evidence, notification outage/retry, capacity
  certification, supported-platform/upgrade policy, and independent security
  review.

### Previous exact-source repository gate — 2026-10-19 (`d79e831`)

- `bash scripts/production-gate.sh` passed on exact checkout
  `d79e831e07434d195ad081c7562d02b85b14d382`: coverage was 56.9%, all package
  floors passed, and the full race suite passed (root package 472.031 seconds).
  Repository recovery drills, benchmarks, mixed HTTP load, and documentation /
  release checks also passed. See the
  [exact-source report](lab-results/2026-10-19-repository-production-gate-d79e831.md).
- At capture, exact-head CI was queued, CodeQL and standard installation smoke
  were pending, and quota-enabled installation smoke was pending. These are
  not passes.
- Production remains **NO-GO** pending installed-host aggregate quota
  validation (including XFS), the 20-repeat recovery cohort, physical power-loss
  and broader ENOSPC evidence, notification outage/retry, capacity
  certification, supported-platform/upgrade policy, and independent security
  review.

### Previous exact-source repository gate — 2026-10-19 (`ad9a43a`)

- `bash scripts/production-gate.sh` passed on exact checkout
  `ad9a43aa8761c4d9b52d6132fc46221c791dc004`: coverage was 56.8%, all package
  floors passed, and the full race suite passed (root package 471.926 seconds).
  The four repository recovery drills, benchmarks, mixed HTTP load, and
  documentation/release checks also passed. See the
  [exact-source report](lab-results/2026-10-19-repository-production-gate-ad9a43a.md).
- At the latest query, branch CI was queued and CodeQL was pending; the
  installation-smoke runs for the code-bearing commit were pending without
  jobs. No hosted result is inferred.
- Production remains **NO-GO** pending installed-host aggregate quota
  validation (including XFS), the 20-repeat recovery cohort, physical power-loss
  and broader ENOSPC evidence, notification outage/retry, capacity certification,
  supported-platform/upgrade policy, and independent security review.

### Latest full repository gate — 2026-10-18 (`128386e`)

- `bash scripts/production-gate.sh` passed on exact source checkout
  `128386e8beb40dcd965ba8c662b6da97237be7b6`: vet, full tests and coverage
  floors (56.9%), full race suite (root package 470.765 seconds), repository
  recovery drills, benchmarks, mixed HTTP load, and repository checks. Details
  are in the [exact-source report](lab-results/2026-10-18-repository-production-gate-128386e.md).
- Exact-head hosted runs for `128386e`: [CI](https://github.com/cyberducttape/StePanel/actions/runs/39173202683)
  and [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39173202723)
  are queued; [standard install](https://github.com/cyberducttape/StePanel/actions/runs/39173202711)
  and [quota install](https://github.com/cyberducttape/StePanel/actions/runs/39173202728)
  are pending. These are not passing evidence.
- The 20-repeat installed-host cohort on `633c013` remains incomplete. At the
  latest query, all 18 host jobs in its three active shards remain at “Start
  disposable systemd host”; no iteration counts are recorded. A fresh
  two-repeat shard on `623acbb` is queued.
- Production remains **NO-GO**: Git project-quota host validation (the new
  ext4 aggregate-write smoke is pending; XFS remains unverified),
  physical host power loss, broader ENOSPC, repeated installed-host recovery,
  notification outage/retry, upgrade/rollback, measured capacity, platform
  support, and independent security review remain open.

### Previous full repository gate — 2026-10-18 (`e447ca2`)

- `bash scripts/production-gate.sh` passed on exact source checkout
  `e447ca2a28e352aa23190bc2917699d86cee5fda`: coverage was 56.9%, and the
  root-package race suite completed in 470.922 seconds. See the
  [exact-source report](lab-results/2026-10-18-repository-production-gate-e447ca2.md).

### Previous full repository gate — 2026-10-18 (`e50dab7`)

- `bash scripts/production-gate.sh` passed on source checkout
  `e50dab701423e84fd126a5edcbe55d3b17a34139`: vet, full tests and coverage
  floors, full race suite, repository recovery drills, benchmarks, mixed HTTP
  load, and repository checks. Exact measurements and scope limits are in the
  [gate report](lab-results/2026-10-18-repository-production-gate-e50dab7.md).
- The report-only descendant `338ea32` has no application or installer source
  changes. At capture its CI and CodeQL runs were queued; exact-source standard
  and quota installation smokes for `e50dab7` were pending without jobs. No
  hosted result is inferred from those states.
- The 20-repeat installed-host recovery cohort on `633c013` remains incomplete;
  no repetitions are promoted from active or queued shards. Production remains
  **NO-GO**: physical host power loss, broader ENOSPC, notification
  outage/retry, upgrade/rollback, measured capacity, platform support, and
  independent security review remain open.

---

## Release Gate Status

See [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md) for complete gate requirements and acceptance criteria.

### Previous full repository gate — 2026-10-18 (`0ebbd86`)

- `bash scripts/production-gate.sh` passed on exact checkout
  `0ebbd86a9c56b37081b132bc39fc10355f4bb9a6`. The application code is
  unchanged from `633c013`; hosted CI and CodeQL passed on `0ebbd86`, and the
  exact application source passed six-host install and quota-enabled smoke.
- The gate passed vet, tests and coverage floors (56.9% aggregate), the full
  race suite (root package 475.085 seconds), four repository recovery drills,
  control-plane benchmarks, mixed HTTP load (p95 14.717 ms), docs, dashboard,
  quota fixture, assets, pinned actions, and shell checks.
- The exact-source 20-repeat host cohort was still running at capture: three
  of ten unique two-iteration shards active and seven queued. This does not
  close the repetition gate. Full measurements and limitations are in the
  [repository gate report](lab-results/2026-10-18-repository-production-gate-0ebbd86.md).

### Exact-head hosted validation — 2026-10-18 (`633c013`)

- Exact-head [CI](https://github.com/cyberducttape/StePanel/actions/runs/39147354411),
  [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/39147354339),
  [six-host installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39147354348),
  and [quota-enabled installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/39147354334)
  passed. CI included Go tests, race tests, repository recovery drills,
  MySQL 8 and PostgreSQL restore checks, the clean Python deployment test,
  static analysis, and release/documentation checks.
- All six Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 × Apache/Caddy hosts
  passed the installed recovery smoke. Each restored a populated WordPress
  database and passed its application HTTP probes (measured RTOs 4.269–5.538
  seconds), recovered from a database outage, and passed one interruption
  matrix iteration. The quota-enabled Ubuntu guest also passed. This is
  current-source smoke evidence, not 20-repeat certification or physical-host
  power-loss evidence.
- A new 20-repeat cohort was dispatched on this exact SHA as ten unique
  two-iteration shards (`gate5-20261018T192511-633c013`). At the recorded
  capture, two shards were active and eight queued; no repeats are counted
  until all six host jobs in a shard pass. The earlier `a27becc` cohort ended
  with eight passing and two failed shards (16 successful repetitions per
  host combination), so it does not satisfy the release gate.
- Production remains **NO-GO**. Physical host power loss, additional ENOSPC
  boundaries, live notification outage/retry, certified capacity, broader
  runtime support, upgrade policy, and independent security review remain
  open. Per-host RTOs, run IDs, and exact limitations are captured in the
  [hosted validation report](lab-results/2026-10-18-hosted-validation-633c013.md).

### Latest full repository gate — 2026-10-17 (`bed56d8`)

- Validation source was `bed56d81b9b56bfd102d48c5daca453ad66ab1aa`. The
  [repository production gate](lab-results/2026-10-17-repository-production-gate-bed56d8.md)
  passed: vet, tests, coverage floors, race suite, repository recovery drills,
  benchmarks, synthetic mixed load, docs, dashboard, quota-fixture capacity
  invariant, action pins, assets, and shell checks. This remains repository
  evidence, not host certification.
- At capture on that source, hosted CI was queued, CodeQL and standard/quota
  install smokes were pending. Three of ten installed-host recovery shards
  remained active in their long install/recovery step; seven were queued. No
  host evidence is inferred from these states. Check GitHub for live status.
- The earlier quota smoke failure on `a27becc` remains a failure, not pass
  evidence. The corrected 8 GiB fixture and job diagnostics are in `2c13219`;
  earlier push-triggered workflow runs were canceled by subsequent commits;
  the latest quota run was pending at capture. The
  original failure and unverified fixture diagnosis are recorded in the
  [failure report](lab-results/2026-10-17-quota-smoke-failure-38992616443.md).
- The 20-repeat installed-host recovery cohort on `a27becc` is incomplete:
  eight of ten two-iteration shards passed and two failed, so it contributes
  16 successful repetitions per host combination, not 20. A new exact-head
  cohort is recorded above. Neither cohort establishes physical host power
  loss or closes the remaining ENOSPC, notification, capacity, runtime-matrix,
  upgrade-policy, or independent-security-review gates.

Production approval remains **NO-GO**.

### Quota-smoke fixture and capacity check — 2026-10-17 (`bed56d8`)

This commit increased the quota smoke's `/var/www` filesystem to 8 GiB for the
5 GiB Git staging reservation and 1 GiB free-space floor. It also requires a
durable Git deployment job ID, detects a terminal state before the injected
activation kill, and prints the job status on timeout. Its hosted checks were
canceled by later pushes. That was the fixture state at this historical
commit; the current unverified change expands both local and hosted fixtures to
24 GiB for a 15 GiB project quota plus the installer's 5 GiB free-space floor.
The current quota smoke also tests aggregate writes and requires active ext4
project quotas. No hosted result is claimed until the new smoke completes.

### Prior source validation — 2026-10-17 (`a27becc`)

- The full local [repository production gate](lab-results/2026-10-17-repository-production-gate-a27becc.md)
  passed on exact source `a27beccc9b5be33a7e30d57662d027c74e6a6cbe` with Go
  1.26.7: vet, tests and coverage floors, race suite, recovery drills,
  benchmarks, mixed HTTP load, documentation, dashboard, asset, action-pin,
  and shell checks.
- Exact-source hosted [CI](https://github.com/cyberducttape/StePanel/actions/runs/38992616491),
  [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38992616456),
  and [standard installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/38992616469)
  passed. The exact-source [quota-enabled installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/38992616443)
  failed before the Git deployment kill boundary; the failed run and
  unverified capacity-fixture diagnosis are recorded in the [failure report](lab-results/2026-10-17-quota-smoke-failure-38992616443.md).
  The quota fixture and smoke diagnostics have been corrected for rerun.
- A 20-repeat full installed-host recovery cohort was dispatched as ten
  uniquely keyed two-iteration shards on this exact SHA. All shard links and
  their then-current running/queued states are recorded in the [gate report](lab-results/2026-10-17-repository-production-gate-a27becc.md).
  No repeats are counted until a complete shard passes all six host
  combinations. The cohort does not prove physical host power loss or close
  the remaining ENOSPC, notification, capacity, runtime-matrix, or independent
  security-review gates.
- A focused exact-source Rocky Linux 9 KVM drill passed real ext4 ENOSPC backup
  and file-restore recovery with active user quotas on `/var/www` ([evidence](lab-results/2026-10-17-rocky9-enospc-03af01b.md)).
  Publication, import, database-restore, and physical-host power-loss cases
  remain open.
- The source-level evidence below remains valid for `a27becc`; it is not
  exact-head validation for later commits. Production approval remains
  **NO-GO** pending the open authoritative gates.

The 2026-10-17 local Rocky Linux 9 KVM install attempt on source `6d5b8b1`
passed database ownership, import/restore/termination recovery, WordPress
application proof, and outage checks, then stopped before Git activation because
the 3 GiB guest had only 2.27 GiB available against the enforced 2.5 GiB clone
admission minimum. The harness now defaults to a 4 GiB guest and rejects
undersized install runs before boot. This attempt is incomplete evidence and
closes no release gate; see the [run report](lab-results/2026-10-17-rocky9-kvm-6d5b8b1.md).

The exact pushed source `ee563eb` passed the repository production gate on
2026-10-16, including full race tests, coverage thresholds, repository recovery
drills, mixed HTTP load, and documentation checks. This is repository-level
evidence only; the installed-host, repeated recovery, upgrade, and capacity
gates remain as recorded in the authoritative gate document and the
[run report](lab-results/2026-10-16-repository-production-gate-ee563eb.md).

The 2026-10-11 hosted install smoke passed on both Rocky Linux 9 and
AlmaLinux 9, including one default interruption-matrix iteration per OS.
For `7d0754f4`, hosted [CI](https://github.com/cyberducttape/StePanel/actions/runs/38224814805),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38238903409),
[Rocky/Alma installation smoke](https://github.com/cyberducttape/StePanel/actions/runs/38238726272),
and [quota-enabled installation](https://github.com/cyberducttape/StePanel/actions/runs/38224814910)
passed. On 2026-10-12, the Rocky Linux 9 KVM guest completed the full
recovery matrix twice consecutively, including the extended journal-boundary
cases; the previous 2026-10-11 repeat attempt remains a recorded failure, not
erased evidence. The guest also passed abrupt-loss and ENOSPC checks recorded
in the [KVM evidence](lab-results/2026-10-12-rocky9-kvm-certification.md).
The two-repeat run is useful installed-host evidence but is not the required
20–50 repetitions, did not simulate physical host power loss, and did not
exercise every ENOSPC boundary. Gate 5 remains open; see the
[repeat-matrix record](lab-results/2026-10-12-rocky9-repeated-recovery-matrix.md).

| Gate | Requirement | Status | Blocker |
|------|-------------|--------|---------|
| **Gate 1** | One Lifecycle Authority | ✅ MET (revised 2026-10-02) | None for publication and removal; configuration updates are broker-executed |
| **Gate 2** | Cross-Process Lock Enforcement | ✅ MET (2026-10-02) | None; interrupted conflicting workflows are covered by `workflow_interruption_test.go` |
| **Gate 3** | Capability Reporting | ✅ COMPLETE | None |
| **Gate 4** | Automated Archive Database Restoration | ✅ COMPLETE | None |
| **Gate 5** | Failure Recovery Testing | 🔄 PARTIAL | 20–50× repeated matrix (two consecutive full Rocky KVM runs now recorded), ENOSPC beyond backup/file restore, physical power loss |

On 2026-10-14 the installed-host WordPress recovery proof passed on Apache and
Caddy with MariaDB for Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9. The latest
completed matrix run is 38547265154; it includes database-backed HTTP proofs
and one recovery-matrix iteration per host combination. These remain smoke
results, not repeated recovery evidence, a broader runtime certification, or
production approval.

**Overall:** Operator Beta gates are in place; Gate 5 Phase 7 remains open for production approval.

The 20-repeat hosted recovery attempt [38694634787](https://github.com/cyberducttape/StePanel/actions/runs/38694634787)
ran out of the six-hour hosted job budget after two complete matrix iterations
and the start of iteration 3/20 on each of its six OS/web-server combinations.
Those two iterations are partial evidence only; no 20-repeat cohort passed.
The workflow now bounds each dispatch to one or two iterations per uniquely keyed
shard; see the [failure analysis](lab-results/2026-10-15-hosted-repeat-matrix-timeout-38694634787.md)
and [shard procedure](REAL_HOST_FAILURE_MATRIX.md). Gate 5 remains open.

On 2026-10-16, installed-host run [38828118744](https://github.com/cyberducttape/StePanel/actions/runs/38828118744)
passed all six OS/web-server combinations, including populated WordPress
restore/HTTP proofs, control-plane and WordPress database outage recovery, and
one recovery-matrix iteration per host ([RTOs and job links](lab-results/2026-10-16-hosted-install-smoke-38828118744.md)).
This brings smoke evidence to nine runs/54 proofs, not 20–50 repeated
certification. The later three-shard attempt on source `60f96ee` completed
without a passing six-host cohort: one job per shard stopped at an HTTP 401 in
the cPanel smoke login. See the [shard outcome report](lab-results/2026-10-16-hosted-recovery-shard-outcomes.md).

Quota-enabled guest run [38828118772](https://github.com/cyberducttape/StePanel/actions/runs/38828118772)
also passed on `0fe2138`: `/var/www` user quotas were active; the populated
WordPress proof RTO was 44.724 seconds; abrupt QEMU guest loss recovered; and
real ENOSPC backup and file-restore tests passed. This is not physical-host
power-loss evidence and does not cover publication/import/database-restore
ENOSPC ([details](lab-results/2026-10-16-quota-install-smoke-38828118772.md)).
The later quota-enabled smoke on current source `3be9b99` passed
([run 38843586000](https://github.com/cyberducttape/StePanel/actions/runs/38843586000)).

### Current-head validation — 2026-10-16 (`3be9b99`)

- Exact-head hosted [CI](https://github.com/cyberducttape/StePanel/actions/runs/38843586085),
  [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38843585904),
  [quota-enabled install smoke](https://github.com/cyberducttape/StePanel/actions/runs/38843586000),
  and [N-1 upgrade smoke](https://github.com/cyberducttape/StePanel/actions/runs/38882203108)
  passed. The six-host standard install smoke
  [38871962287](https://github.com/cyberducttape/StePanel/actions/runs/38871962287)
  and an earlier run [38843585907](https://github.com/cyberducttape/StePanel/actions/runs/38843585907)
  remain queued/pending at capture; no result is inferred from them.
- The three completed two-repeat recovery shards on older source `60f96ee`
  ([01](https://github.com/cyberducttape/StePanel/actions/runs/38830877337),
  [02](https://github.com/cyberducttape/StePanel/actions/runs/38830880739),
  [03](https://github.com/cyberducttape/StePanel/actions/runs/38830884355))
  did not pass as a cohort. Fifteen of 18 host jobs passed both iterations;
  the other three each failed once at the cPanel smoke login with HTTP 401,
  before import. Those failures occurred on Ubuntu/Apache, Rocky/Caddy, and
  Ubuntu/Caddy respectively. The current-source smoke harness retries one 401
  with a fresh TOTP counter, but its six-host hosted run is still queued, so
  that mitigation is not yet confirmed by installed-host evidence. No 20-repeat
  certification is claimed. Per-shard details and job IDs are recorded in the
  [outcome report](lab-results/2026-10-16-hosted-recovery-shard-outcomes.md).
- Production readiness remains **not approved**. Physical-host power loss,
  broader ENOSPC boundaries, a complete same-source repeated matrix, live
  notification outage/retry evidence, capacity certification, and independent
  security assessment remain open.

### Historical current-head validation — 2026-10-16 (`64696a2`)

- Local `go test ./...`, `go test -race ./...`, and `go vet ./...` passed with
  Go 1.26.7. The standard root-package suite took 65.436 seconds; the race
  suite took 441.531 seconds. Shell syntax checks passed for `install.sh` and
  all shell scripts under `deploy/` and `scripts/`; documentation link and
  source-reference checks passed for 88 documents.
- Exact-head hosted [CI](https://github.com/cyberducttape/StePanel/actions/runs/38833209036)
  and [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38833209088)
  passed. The GitHub code-scanning API reported zero open alerts at capture.
- The same-SHA 20-iteration installed-host cohort remains in progress:
  shards 01 and 02 are running all six OS/web-server jobs; shards 03–10 are
  queued. No iteration is counted until its complete six-host shard passes. The
  exact-head six-host install and quota runs are pending; the completed smoke
  and quota evidence documented above is on unchanged application code.
- This validation does not close physical-host power-loss, publication/import/
  database-restore ENOSPC, live SMTP/webhook outage, capacity, runtime-matrix,
  upgrade-policy, or independent-security-review gates. No production approval
  is implied. Full results are in the
  [current-head validation report](lab-results/2026-10-16-current-head-validation-64696a2.md).
- A focused KVM run on exact source `aa1d4b4` passed the real ext4 ENOSPC
  backup and file-restore tests in a disposable Rocky Linux 9 guest. The
  backup failed without publishing a partial artifact; file restore preserved
  the existing site on ENOSPC and succeeded after space was freed. This run
  did not install StePanel and does not cover publication, import, database
  restore, physical power loss, or repeated recovery certification; see the
  [run report](lab-results/2026-10-16-kvm-enospc-aa1d4b4.md).
- The exact-source ENOSPC drill was repeated on current source `081f1f6` in a
  fresh Rocky Linux 9 guest; boot, backup, and file-restore ENOSPC phases
  passed ([run report](lab-results/2026-10-16-kvm-enospc-081f1f6.md)). It is
  still a focused disposable-guest test, not installed-host certification, and
  the uncovered ENOSPC boundaries and physical power-loss gate remain open.
- A fresh [repository recovery-drill run](lab-results/2026-10-16-recovery-drills.md)
  on `5732c10` passed partial SQL import cleanup, interrupted transaction
  recovery, configuration rollback, and pending runtime reconciliation. These
  synthetic tests do not substitute for installed-host failure evidence.
- The full [repository production gate](lab-results/2026-10-16-repository-production-gate-3927b38.md)
  passed on exact checkout `3927b38` with Go 1.26.7: tests, coverage floors,
  race suite, repository recovery drills, benchmarks, synthetic mixed HTTP
  load, docs, assets, action pins, and shell syntax. Host recovery repetition,
  physical power loss, broader ENOSPC, live alert outage/retry, runtime matrix,
  and capacity certification remain open.
- The older-source 20-repeat cohort on `60f96ee` is incomplete. One Rocky Linux
  9/Caddy host in shard 02 failed at cPanel-move smoke login with HTTP 401
  before import; the panel logs confirm the failed login but not its cause.
  Shards 01 and 03 and the other shard-02 hosts remain active at capture. The
  cPanel-move smoke now retries one 401 with a fresh TOTP counter; this change
  still needs hosted validation and the cohort does not count as passed.

### Full repository production gate — code source `1e38dfc`

`bash scripts/production-gate.sh` passed on `1e38dfc1d201a244ef07ffb2bfd01ef03f450969`
with Go 1.26.7. It passed full coverage and configured package floors, the race
suite, repository recovery drills, control-plane benchmarks, a 200-site mixed
HTTP workload (180 requests; p95 10.914 ms), documentation/reference checks,
action pinning, asset validation, and shell syntax. The synthetic load and
single-iteration benchmarks are not a certified capacity envelope. The gate
explicitly does not establish host-level ENOSPC, power-loss, distro, or
production-capacity evidence. See the
[captured results](lab-results/2026-10-16-repository-production-gate-1e38dfc.md).

### Exact-head repository production gate — `c628903`

The full `bash scripts/production-gate.sh` passed on this exact checkout with
Go 1.26.7: vet, full coverage and configured package floors, the complete race
suite, repository recovery drills, control-plane benchmarks, synthetic 200-site
mixed HTTP load, documentation/reference checks, action pinning, asset
validation, and shell syntax. Overall coverage was 56.2%; the root race suite
took 448.918 seconds. See the
[captured results](lab-results/2026-10-16-repository-production-gate-c628903.md).
This repository-level result does not close the installed-host recovery cohort,
physical power-loss, broader ENOSPC, live notification, runtime-certification,
or capacity gates.

### Current-head evidence update — 2026-10-15 (`7ca34dc`)

- Hosted [CI](https://github.com/cyberducttape/StePanel/actions/runs/38692216922)
  and [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38692216955)
  passed. CI includes the full Go suite, coverage thresholds, race tests,
  repository recovery drills, real tmpfs ENOSPC, mixed-load testing, production
  container smoke, deployment validation, and UI E2E/accessibility.
- A local full `go test -race -p 1 -timeout 20m ./...` also passed on this
  source; the root package completed in 443.572 seconds. Full results are in
  the [current-head validation report](lab-results/2026-10-15-current-head-checks-7ca34dc.md).
- The exact-head six-host install run [38692216889](https://github.com/cyberducttape/StePanel/actions/runs/38692216889)
  is queued, the quota-enabled run [38692216846](https://github.com/cyberducttape/StePanel/actions/runs/38692216846)
  is pending, and the exact-head 20-repeat full matrix [38692636041](https://github.com/cyberducttape/StePanel/actions/runs/38692636041)
  is pending. The older matrix [38670189826](https://github.com/cyberducttape/StePanel/actions/runs/38670189826)
  remains live on source `1fb3879` and is not counted as current-head evidence.
- Physical power loss, broader ENOSPC, live notification outage/retry,
  capacity certification, full runtime support certification, and independent
  security review remain open. No production approval is implied.

### Current-head evidence update — 2026-10-15 (`7badb0b`)

- Hosted [CI](https://github.com/cyberducttape/StePanel/actions/runs/38690891081)
  passed, including full tests, coverage floors, race suite, repository
  recovery drills, tmpfs ENOSPC, container smoke/restart, deployment checks,
  and UI E2E/accessibility. [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38690891066)
  passed, and the open-alert query returned none.
- The [local current-head report](lab-results/2026-10-15-current-head-checks-7badb0b.md)
  records full Go tests, vet, and the off-site publication failure/retry round
  trip. The off-site change publishes verified objects before its completion
  marker and retries the same immutable artifact after marker-publication
  failure.
- Exact-head installed-host run [38690891035](https://github.com/cyberducttape/StePanel/actions/runs/38690891035)
  was pending; quota-enabled host run [38690891070](https://github.com/cyberducttape/StePanel/actions/runs/38690891070)
  was in progress. The 20-repeat matrix [38670189826](https://github.com/cyberducttape/StePanel/actions/runs/38670189826)
  remains active on older source `1fb3879`, not current-head certification.
- Physical power loss, broader ENOSPC, repeated current-head installed-host
  recovery, live notification outage/retry, capacity certification, and
  unsupported runtime combinations remain open. No production approval is
  implied.

### Current-head evidence update — 2026-10-15 (`a362b29`)

- The exact-head local [repository production gate](lab-results/2026-10-15-repository-production-gate-a362b29.md) passed with Go 1.26.7: full tests, 56.2% coverage and all package floors, race suite, repository recovery drills, benchmarks, synthetic mixed HTTP load, and documentation/asset/shell checks.
- Hosted [CI](https://github.com/cyberducttape/StePanel/actions/runs/38688623120), including UI E2E/accessibility, passed; [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38688623127) passed.
- The [N−1 upgrade and rollback smoke](https://github.com/cyberducttape/StePanel/actions/runs/38688628029) passed from v0.6.0 to the candidate, including rollback after a deliberately broken candidate.
- Six-host installation run [38688623197](https://github.com/cyberducttape/StePanel/actions/runs/38688623197) and quota-enabled run [38688623248](https://github.com/cyberducttape/StePanel/actions/runs/38688623248) were pending at capture. The separate 20-repeat run [38670189826](https://github.com/cyberducttape/StePanel/actions/runs/38670189826) remains in progress on older source `1fb3879`; it is not current-head certification.
- Physical host power loss, broader ENOSPC, repeated current-head installed-host recovery, live alert outage/retry, capacity certification, and the unsupported portions of the runtime matrix remain open. No production approval is implied.

### Current-head evidence update — 2026-10-15 (`7af2e82`)

- Hosted CI passed, including serialized Go tests/coverage, race checks,
  repository recovery drills, real tmpfs ENOSPC, UI E2E/accessibility, and
  deployment/container checks ([run 38662396749](https://github.com/cyberducttape/StePanel/actions/runs/38662396749)).
- CodeQL passed ([run 38662396836](https://github.com/cyberducttape/StePanel/actions/runs/38662396836));
  no open code-scanning alerts were reported.
- All six Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 × Apache/Caddy + MariaDB
  installed-host jobs passed, including explicit control-plane database-outage
  evidence and database-backed WordPress recovery ([run 38662396787](https://github.com/cyberducttape/StePanel/actions/runs/38662396787)).
  The subsequent docs-head install smoke also passed on all six combinations
  ([run 38670211824](https://github.com/cyberducttape/StePanel/actions/runs/38670211824));
  with the `1fb3879` docs-head rerun, seven runs/42 single-run proofs are now
  recorded. This still is not the required 20–50 repetition gate. Latest
  per-host RTOs are in the [run report](lab-results/2026-10-15-hosted-install-smoke-38670211824.md).
- A quota-enabled Ubuntu 24.04 QEMU guest run passed on source commit
  `7af2e82` ([run 38662396783](https://github.com/cyberducttape/StePanel/actions/runs/38662396783)):
  `/var/www` user quota was active, WordPress restore proof RTO was 75.925 s,
  abrupt guest loss with persistent disks recovered, and real backup/file-
  restore ENOSPC tests passed. See the [run report](lab-results/2026-10-15-quota-install-smoke-38662396783.md).
  This does not establish physical host power-loss recovery; publication/import/
  database-restore ENOSPC and 20–50 repetitions remain open. The second quota
  run on `1fb3879` also passed with a 79.412 s WordPress proof RTO; see the
  [quota report](lab-results/2026-10-15-quota-install-smoke-38662396783.md).
  A quota rerun for the docs-only commit `94d5801` remains queued.

### Repository gate update — 2026-10-15 (`3f1445f`)

The full local `scripts/production-gate.sh` passed on `3f1445f` with Go
1.26.7: 56.1% repository coverage and all configured package thresholds,
complete race suite, four recovery drills, control-plane benchmarks,
synthetic 200-site mixed HTTP load, documentation/reference checks, pinned
actions, assets, and shell syntax. The recorded p95 for the short synthetic
load was 11.949 ms; this is not a certified capacity result. See the
[gate report](lab-results/2026-10-15-repository-production-gate-3f1445f.md).
The hosted current-head checks and 20-repeat installed-host matrix remain
separate; no physical power-loss, broader ENOSPC, or production approval is
claimed.

The installed-host smoke on `f335ed5` subsequently passed all six Ubuntu
24.04, AlmaLinux 9, and Rocky Linux 9 × Apache/Caddy + MariaDB combinations
([run 38677968172](https://github.com/cyberducttape/StePanel/actions/runs/38677968172)).
Each completed one default interruption-matrix iteration and a populated,
database-backed WordPress HTTP recovery proof; RTOs were 4.548–8.155 seconds.
This is eight six-host runs/48 individual smoke proofs in total, not 20–50
repetitions. See the [run report](lab-results/2026-10-15-hosted-install-smoke-38677968172.md).
Exact-HEAD CI, CodeQL, install, and quota checks for `276282f` are queued; the
separate 20-repeat matrix remains in progress. No new physical power-loss,
broader ENOSPC, or capacity certification is established.

### Historical evidence update — 2026-10-14

- The latest completed hosted install smoke run (38547265154) passed all six
  Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 × Apache/Caddy + MariaDB
  combinations. Each installed-host job passed the database-backed WordPress
  recovery proof and one default interruption-matrix iteration; measured
  proof RTOs were 5.253–7.251 seconds. Across four completed six-host runs,
  there are 24 single-run proofs. See the
  [latest run evidence](lab-results/2026-10-14-hosted-install-smoke-38547265154.md).
- These are four single-run results per combination, not the required 20–50
  installed-VM recovery repetitions. Physical power loss, broader ENOSPC,
  capacity certification, and repeated recovery certification remain open.
- A quota-enabled Ubuntu 24.04 guest run passed on 2026-10-14 at
  `1b293f8`: production-shaped install, quota-backed `/var/www`, WordPress
  recovery proof (87.688 s in QEMU TCG), worker/panel interruption smokes,
  rootless build/release pipeline, abrupt QEMU guest loss with persistent
  disks, and real backup/file-restore ENOSPC tests. This remains one run, not
  physical host power-loss evidence; publication/import/database-restore
  ENOSPC and 20–50 repetitions remain open. See the
  [latest successful quota-install result](lab-results/2026-10-14-quota-install-smoke-38526303825.md)
  and [earlier quota run](lab-results/2026-10-14-quota-install-smoke.md).
- A later quota-enabled installation smoke ([run 38547265178](https://github.com/cyberducttape/StePanel/actions/runs/38547265178))
  passed, but no detailed diagnostic artifact was retained; it adds no new
  failure-recovery evidence. A subsequent attempt was canceled by a newer push
  under the old workflow concurrency policy. Commit `12b0d70` disables
  cancellation of an active quota run; the replacement run is
  [pending](https://github.com/cyberducttape/StePanel/actions/runs/38572966792).
- Hosted CI, CodeQL, and install-smoke checks passed on `730cbd9`. At capture
  time for source commit `12b0d70`, CodeQL and UI E2E had passed, the Go CI
  job and six-host installation smoke were in progress, and quota-enabled
  install was pending ([CI](https://github.com/cyberducttape/StePanel/actions/runs/38572967532),
  [CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38572967236),
  [install](https://github.com/cyberducttape/StePanel/actions/runs/38572967470),
  [quota install](https://github.com/cyberducttape/StePanel/actions/runs/38572966792)).
- Status refresh after commit `d1a259c`: CodeQL is running; CI and the
  six-host installation matrix are queued. The quota guest run for `c73926e`
  remains active, while the newest quota run for `d1a259c` is pending
  ([CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/38574974617),
  [CI](https://github.com/cyberducttape/StePanel/actions/runs/38574974335),
  [install](https://github.com/cyberducttape/StePanel/actions/runs/38574974411),
  [active quota run](https://github.com/cyberducttape/StePanel/actions/runs/38572742100),
  [pending quota run](https://github.com/cyberducttape/StePanel/actions/runs/38574974320)).
  The intervening quota run for `12b0d70` was pending, then replaced when a
  newer run entered the single pending slot; it did not cancel the active
  `c73926e` run. This is queue replacement, not a test result.

### Changes 2026-10-03 to 2026-10-10 (on `main`, unreleased)

- **Tenant site creation:** an active tenant owner can create a blank PHP
  site with `POST /api/sites` within their plan; ownership is assigned before
  the durable job is queued, plan resources are reconciled afterwards, and a
  terminally failed job releases the reservation. See
  [SHARED_HOSTING.md](SHARED_HOSTING.md).
- **KVM certification run (2026-10-09):** on a Rocky Linux 9 guest with
  SELinux enforcing, the install smoke, the full 105-drill recovery matrix,
  abrupt guest kills (idle and mid-backup), and a real ENOSPC backup on a
  dedicated filesystem passed. The run found and fixed SELinux defects that
  container CI could not see (scheduled tasks, Git deploys after sealing, the
  rootless build runner). See
  [lab-results/2026-10-09-rocky9-kvm-certification.md](lab-results/2026-10-09-rocky9-kvm-certification.md).
- **KVM certification follow-up (2026-10-12):** a fresh Rocky Linux 9 run
  passed the full 108-check install/matrix phase, abrupt loss while idle and
  during backup, and backup plus file-restore ENOSPC. It exposed a runner
  SELinux-labeling regression that was fixed and passed standalone runner and
  release-pipeline retests on that guest. This is one matrix iteration, and
  its first full-run runner phase failed; see
  [the detailed evidence](lab-results/2026-10-12-rocky9-kvm-certification.md).
- **Offsite backups** publish transactionally (temporary remote prefix,
  checked before promotion, completion marker required for restore), with
  size-aware deadlines and constrained restore arguments.
- **Application activation** is journaled: a crash between runtime change
  and manifest publication is rolled back to the prior generation at startup.
- **Caddy** serves each site through a `nosymfollow` bind view, so symlinks
  created after route publication cannot escape the document root.
- **Root-broker journals** live in the root-owned `/var/lib/stepanel/recovery`,
  separate from the panel-owned site snapshot root.

### Changes on 2026-10-02 (on `main`, unreleased)

- **Root broker concurrency:** the socket broker serves connections
  concurrently and admits work through a resource-scoped scheduler (per site,
  database, account, or certificate domain; `-max-concurrent`, default 8).
  Health probes bypass the queue, and stalled peers are disconnected.
- **Durable job cleanup** no longer leaves memory and SQLite out of sync when
  a delete fails; fault-injection tests cover BEGIN, DELETE, COMMIT, and
  `SQLITE_BUSY`.
- **Outbound requests** (imports, task webhooks) share `internal/safehttp`:
  public addresses only, checked on the connected address. Task units deny
  cloud metadata ranges.
- **Domain names** are validated by one shared policy in every Go layer.
- **Installer** prints a host preflight, supports `--dry-run`, and requires
  `--take-over-host` (or, in guided mode, explicit consent) before stopping
  an existing web server. `--guided` and `--config` add a verified guided
  setup.
- **Recovery status:** restore rehearsals are recorded, run automatically
  after scheduled backups, and summarized per site
  (`GET /api/sites/recovery/{site}`), at the archive level.
- **Capacity:** a stated control-plane target (500 sites, session check p99
  ≤ 25 ms) is measured at 6–8.6 ms; see
  [LOAD_BASELINE_2026-10-02.md](LOAD_BASELINE_2026-10-02.md).
- **Gate 1** was redefined to match the implementation; see
  [V1_PRODUCTION_GATES.md](V1_PRODUCTION_GATES.md#gate-1-one-lifecycle-authority).

### Verified hardening before 2026-10-02

- Legacy shared-state blobs remain temporary compatibility storage, but writes
  now use revision CAS with bounded reload/merge retries for independent map
  keys. Conflicting same-key writes refresh the local snapshot and fail closed;
  persistence helpers restore in-memory values on failure. A subprocess test
  exercises a panel/worker-style stale writer against the same SQLite file.
- External-worker mode publishes durable worker identity, host, PID, start and
  last-seen times, supported job kinds, current jobs, and build/version. Both
  readiness and operational health require a fresh compatible worker.
- Archive inspection/import handlers return durable output and report operation
  errors through the job state. Archive scanning is context-bound and bounded
  for redirects, compressed bytes, entries, and decompressed content.
- Privileged operations use typed root-broker requests. The generic helper RPC
  and its caller-controlled helper/action/argv payload have been removed; an
  untyped production helper call now fails closed. Development installs retain
  direct helper execution for local workflows.
- The reported CodeQL path, integer, command-injection, and uncontrolled-path
  alerts are resolved; the hosted CodeQL scan for `7d0754f4` passed with zero
  open alerts. CI for this commit passed formatting, Go tests, vet, race,
  coverage, and configured recovery checks. These checks are release evidence,
  not claims that the remaining production gates are closed.

---

## Key Subsystem Status

### Helper Layer Modernization (typed production protocol complete)

**Status:** The production callsite migration is complete. Root-broker requests
are finite typed operations; no generic helper request type or handler remains.

- ✅ Phase 1: Design & Foundation (100%)
- ✅ Phase 2: Broker foundation and validation (100%)
- ✅ Phase 3: Integration Testing (100%)
- ✅ Phase 4: Callsite Replacement (100%; production mutations and helper-backed reads use typed requests)

**What it is:** Replace 14 shell scripts (1200 lines, high security consequence) with a typed Go root broker. The broker centralizes input validation and fails closed for mutation paths that are not implemented yet; it is not a claim that every helper operation is available.

**Deliverables:** 
- `internal/rootbroker/` — Broker types, validation, supported operations, client, and explicit unsupported-operation responses
- Root-broker tests cover validation and fail-closed mutation behavior
- Integration documentation and migration guide
- `internal/rootbroker/client.go` — direct typed-client integration surface

**Status:** Native production installs route site, app, worker, environment, resource, proxy, vhost, runner, Git, TLS, and database operations through the root-owned Unix-socket broker. The panel and worker have no sudoers grant. The broker accepts concrete request types only; production helper wrappers reject calls without a typed operation. The former `BrokerBridge` wrapper was removed because it had no production callers and exposed unsupported operations through a misleading transitional abstraction.

**Typed protocol evidence (2026-10):** `rootbroker.Request` has no generic
helper field, the broker has no generic helper handler, and production helper
wrappers fail closed instead of serializing arbitrary helper argv. Fixed argv
validation remains internal for resource and runner operations after the broker
constructs arguments from their typed request fields.

**Next Action:** Keep the typed protocol invariant executable in tests and
re-audit every newly added production callsite. Task direct-helper fallback is
limited to non-production development installs.

---

### Gate 5: Failure Injection Testing (partial; Phase 7 remains open)

**Status:** Partial. The 2026-10-10 KVM run passed installation and the full 107-pass recovery matrix once on a SELinux-enforcing Rocky Linux 9 guest ([results](lab-results/2026-10-10-rocky9-kvm-full-matrix.md)); the 2026-10-11 two-iteration attempt completed its first iteration but failed in the second when a worker restart left an empty audit lock ([failure record](lab-results/2026-10-11-rocky9-repeat-matrix-attempt.md)). The zero-byte lock blocked audit-dependent requests. Atomic lock publication and stale-empty-lock recovery now pass unit/race tests. A subsequent 2026-10-12 full extended matrix completed two consecutive iterations on the disposable Rocky guest, including worker and panel interruption at the applicable backup, restore, termination, suspension, cpmove, and Git activation boundaries ([results](lab-results/2026-10-12-rocky9-repeated-recovery-matrix.md)). This is not 20–50 repetitions and does not supersede the prior failure record. The 2026-10-09 run additionally passed abrupt guest kills and a real ENOSPC backup ([results](lab-results/2026-10-09-rocky9-kvm-certification.md)); a focused 2026-10-11 KVM test passed real ext4 ENOSPC during file restore ([results](lab-results/2026-10-11-real-enospc-restore.md)). The full Phase 7 failure matrix remains open: publication/import/database-restore ENOSPC, physical host power loss, and additional repetitions remain unproven.

- ✅ Phase 1: Failure Injection Framework (100%)
- ✅ Phase 2: Durable Checkpoint System (100%)
- ✅ Phase 3: Documentation & Guides (100%)
- ✅ Phase 4: Broker Integration (100%)
- ✅ Phase 5: Operation Journals (100%)
- ✅ Phase 6: Workflow Integration Testing (100%)
- 🔄 Phase 7: VM-Level Failure Testing (partial: two single-iteration full KVM matrix runs; the 2026-10-12 run also passed abrupt guest loss idle and mid-backup plus backup/file-restore ENOSPC. A runner SELinux regression was fixed and retested separately on that guest. The interrupted two-iteration attempt, 20–50 successful repeats, ENOSPC during publication/import/database restore, and physical power loss remain unproven)

**What it is:** Prove StePanel survives and recovers deterministically from real failures (SIGKILL, disk full, database offline) at operation boundaries.

**Deliverables:**
- Durable journals for 6 critical operations (site create, app deploy, DB provision, vhost config, DB restore, git deploy)
- Atomic write patterns (temp + rename) throughout
- 55+ broker operation tests + 6 workflow integration tests
- Provider-neutral VM test harness scaffold that fails closed until real
  disposable-VM provider commands are implemented; it is not itself
  production evidence
- 2000+ lines of integration guides and failure recovery documentation

**Local evidence supports:**
- ✅ Boundary-level rollback and cleanup tests for the listed operations
- ✅ Deterministic journal behavior in repository-level recovery drills
- ✅ Safe retry behavior for the tested journal paths
- ⚠️ Full host-level crash safety remains unproven until the Phase 7 matrix runs

`DefaultManager.Delete` now refuses to remove a site when its operation
context has been cancelled, with a regression test confirming the site remains
intact. This covers the manager cancellation boundary but does not replace
full conflicting-workflow recovery evidence.

Route publication and deletion now share the site/vhost fenced lock set, with
a regression test confirming that deletion cannot enter the vhost boundary
while publication holds it. Full interrupted-workflow recovery acceptance
remains open.

Domain claim and verification now share the site fence with termination.
Proxy deployment/deletion and route reconciliation also check the fenced
operation context before reporting or persisting successful state after helper
work. Full interrupted-workflow recovery acceptance remains open.

Lock-held backup workflows now propagate their operation context through
archive traversal and publication, with cancellation checks preventing a
fenced backup from being committed after lease loss.

Offsite backup transfers in lock-held jobs now inherit the same operation
context, preventing cloud transfer work from continuing after lease loss or
job cancellation.

Application deployment/actions and runner builds now verify the fenced
operation context before recording successful completion after helper work.

SSH access and environment desired-state updates now check the fenced context
before persistence and preserve pending reconciliation state when cancellation
is observed after helper execution.

Scheduled task and resource-profile updates now use the same pending-state
behavior when their mutation lease is lost around helper execution.

PHP and Python runtime/application updates now preserve pending reconciliation
state when helper execution completes after lease cancellation.

Worker lifecycle and desired-state updates now preserve pending reconciliation
state when their helper execution is interrupted by lease loss.

The same lease boundary is now enforced for Node tooling, WordPress, Composer,
`.htaccess`, database, Redis, account suspension, release activation, staging,
and task reconciliation workflows. Route deletion preserves pending desired
state when its helper completes after lease loss. Full interrupted-workflow
recovery acceptance remains open.

Account creation, assignment, deletion, credential recovery, password/MFA
changes, and session revocation now share the account fence; account resource
profile enforcement also acquires the affected site fences before host work.
API-token, backup-schedule, webhook, and Git deployment/rollback mutations now
use the same account/site fencing and completion checks.
Environment reconciliation also leaves host state pending instead of reporting
success after lease loss.
Route, runtime, resource, task, and worker reconciliation now apply the same
post-persistence lease check and pending-state fallback.

Hosted installation smoke run `36529280956` passes on AlmaLinux 9 and Rocky
Linux 9 for the default recovery drills plus alternate kill boundaries across
cpmove import, backup creation/verification, file restore commit, termination,
account suspension, and Git deployment activation. A disposable Rocky Linux
9.8 VM passed the installed-host recovery sequence, then remained healthy after
an abrupt QEMU-process kill/reboot and a MariaDB stop/start. These checks do
not establish real host power-loss or disk-exhaustion safety.

The 2026-10-09 KVM certification run
([results](lab-results/2026-10-09-rocky9-kvm-certification.md)) then killed and
recovered every supported journal boundary of cpmove activation, backup,
restore, termination, and suspension on a SELinux-enforcing Rocky Linux 9
guest (105 drills), survived an abrupt guest kill while idle and during a
backup, and showed that a backup hitting real ENOSPC on a dedicated filesystem
publishes nothing partial. A separate focused test later exercised restore
extraction on a dedicated ext4 filesystem ([results](lab-results/2026-10-11-real-enospc-restore.md)); it does not cover database restore or site publication. These are single runs, and a QEMU `SIGKILL` is not physical power loss.

**Remaining Work:** Complete the Phase 7 failure matrix
([REAL_HOST_FAILURE_MATRIX.md](REAL_HOST_FAILURE_MATRIX.md))
- Repeat the real-host matrix 20–50 times per operation/fault combination
- Real ENOSPC during site publication, import, and database restore (backup and file-restore extraction are covered once)
- Physical power loss / unclean disk-cache loss, beyond killing the QEMU process
- Managed-database outage during an operation and verified recovery
- Real-host drills for the `restore:provision` and `restore:provisioned`
  boundaries (restore-to-staging database path). A focused Rocky Linux 9
  installed-host smoke now exercises `restore:provisioned`: the worker is
  killed after the managed database and staging site are provisioned, recovery
  dead-letters the non-replayable site operation, and the smoke verifies the
  staged files, route, Unix account, and database are removed while the source
  site remains intact. The same run passed the separate backup, file-restore,
  database-restore, and termination interruption checks. This is one focused
  run, not a completed full matrix or repeated certification. The companion
  `restore:provision` checkpoint also passed in an earlier focused guest run
  ([2026-10-21 evidence](lab-results/2026-10-21-rocky9-staging-restore-provisioned.md)).
- Recovery-time measurement (< 5 minute success criterion)

**Next Action:** Run the full recovery matrix on a KVM guest at
`RECOVERY_MATRIX_REPEATS=20` (`local-kvm-certification.sh` currently runs it
once), then extend the harness to ENOSPC beyond backup and managed-database
outages at operation boundaries.

---

## Deployment Classification

**Current:** Operator Beta  
**Target for v1.0:** Single-Host Production Candidate

| Classification | When Ready | Notes |
|----------------|-----------|-------|
| **Development** | ✅ Now | Local development, full feature set |
| **Operator Beta** | ✅ Now (v0.7.0) | Single-host deployment; operator expertise required |
| **Single-Host Production** | After Phase 7 | Requires proof of failure recovery (Gate 5 Phase 7) |
| **Multi-Tenant Production** | v2.0+ | Tenant roles and host resource envelopes exist; requires audit segregation, an HA datastore, and cross-host job routing |

See [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) for deployment capabilities and limitations.

---

## Known Limitations (Operator Beta)

### ⚠️ Single-Host Constraints
- **No active-active HA:** Durable jobs tied to local database; no cross-host failover
- **No backup HA:** Scheduled backups with a required offsite copy, but no automatic failover to a standby host
- **Downtime on deploy:** Updates require restart; no canary/rolling deployment
- **Single point of failure:** All control-plane data on single host

### ⛔️ Not Ready For
- **Multi-tenant SaaS:** Requires audit segregation, an HA datastore, cross-host job routing, and a hostile-workload isolation boundary
- **High-availability:** No stateless API, no shared session store, no cross-region replication
- **Automated recovery:** Restore rehearsals run automatically at the archive level, but restoring a site is an operator action; there is no auto-remediation
- **Enterprise SLAs:** SLA tracking and escalation routing not implemented

---

## Migration Workflows Status

| Workflow | Status | Notes |
|----------|--------|-------|
| **cpmove** | ✅ Implemented & recovery-tested | Most complete; journaled activation |
| **WordPress Import** | ✅ Implemented; pending certification gate | WP-specific optimizations; recovery-tested |
| **Generic Archive** | ✅ Implemented; optional automated DB restore | Manual restore workaround if no credentials |
| **Git Deployment** | ✅ Implemented; git-based rollback | Application-driven; recovery-tested |
| **Backup Restore** | ✅ Implemented; staging + activation | Supports file, database, config restore |

All workflows use **journaled staged activation** — operations are staged in a private directory with recovery journals that enable rollback before production activation.

---

## Recent Completions (This Cycle)

✅ **Security improvements:**
- P1 state persistence error handling (77 lines of safe error helpers)
- 7 locations fixed to no longer silently ignore persistence errors
- Panel and worker systemd units now use filesystem, private-temp, kernel, and
  control-group protections; audited exposure is 6.7 MEDIUM on Rocky 9.8
- Native installs now route all privileged helper calls through the
  root-owned, peer-authorized Unix-socket broker; the panel and worker units
  enable `NoNewPrivileges`, SUID/SGID restrictions, private devices, and
  strict writable-path limits. Older installations must be upgraded to remove
  the transitional sudo policy.

✅ **Helper Layer foundation:**
- Broker foundation (types, validator, operations, and client; the transitional bridge was later removed)
- Fail-closed tests for unsupported mutation paths
- Supported operation paths and remaining gaps documented explicitly

✅ **Gate 5 local recovery evidence:**
- Durable journal pattern proven in production code
- 6 operations with atomic checkpoints
- Workflow integration testing (6 new tests)
- Phase 7 provider-neutral VM scaffold still requires provider commands; separate disposable QEMU VM checks now cover selected recovery paths

✅ **DBLocks (Sep 2026 rewrite):**
- Fixed 5 latent bugs in distributed lock implementation
- Fencing tokens now properly enforced
- Concurrent operation safety proven with regression tests

---

## Roadmap to v1.0

### Immediate (Week 1-2)
1. Repeat the KVM recovery matrix 20–50 times and publish the results
2. Extend VM tests to ENOSPC beyond backup and managed-database outages during operations
3. Complete physical power-loss validation before approving Gate 5 Phase 7

### Short-term
4. Publish the typed privileged-request model and retain the regression test
   proving that generic helper requests are rejected
5. Extend typed broker operations only through concrete request and response
   structures with resource-scoped validation and locking
6. Extend restore rehearsals to import databases and start the site on a
   staging hostname, so the measured recovery time is a true RTO

### Release (Before v1.0.0)
7. Run continuous failure injection in staging
8. Monitor 30 days in staging
9. Publish v1.0.0 Production Release

---

## What's Next?

**For Operators:** See [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) for deployment guidance.

**For Contributors:** See [ROADMAP.md](./ROADMAP.md) for product roadmap and [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md) for release gates.

**For Security Reviews:** See [SECURITY.md](./SECURITY.md) and [THREAT_MODEL.md](./THREAT_MODEL.md).

**For Architecture:** See [ARCHITECTURE.md](./ARCHITECTURE.md) and [docs/adr/](./adr/) for architectural decisions.

---

## Related Documents

**Status & Release:**
- [V1_PRODUCTION_GATES.md](./V1_PRODUCTION_GATES.md) — Authoritative release gates (read this for release approval)
- [PRODUCTION_READINESS.md](./PRODUCTION_READINESS.md) — Deployment capabilities and limitations
- [ROADMAP.md](./ROADMAP.md) — Product roadmap (v0.1 → v2.0)

**Technical Details:**
- [ROOT_BROKER_INTEGRATION.md](./ROOT_BROKER_INTEGRATION.md) — Broker integration guide
- [REAL_HOST_FAILURE_MATRIX.md](./REAL_HOST_FAILURE_MATRIX.md) — Real-host failure matrix and how to run it
- [lab-results/](./lab-results/) — Dated recovery and certification evidence

---

**Questions?** Check the related documents above or open an issue.
