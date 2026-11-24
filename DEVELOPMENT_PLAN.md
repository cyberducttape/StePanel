# StePanel production-readiness plan

**Status:** In progress; production remains **NO-GO**.  
**Release authority:** [`docs/V1_PRODUCTION_GATES.md`](docs/V1_PRODUCTION_GATES.md).  
**Operating promise under consideration:** operator-managed, single-host
PHP/WordPress only. HA and hostile multi-tenant isolation are out of scope.

This plan orders unfinished work by release risk. A milestone is complete only
when its acceptance evidence is attached to the exact release-candidate source
and the authoritative gate document is updated. Passing unit tests or a
successful one-off smoke does not close a host-certification gate.

## Milestones

### M0az — Make the site workspace lighter on first load (P2)

**Status:** Implemented in commit `8e54ebb4` and locally verified. The
lifecycle and overview views use their recovery summary for the latest backup
instead of scanning hundreds of backup records. Recent web-server and PHP-FPM
error samples in the lifecycle view are loaded only when an operator asks to
inspect them. `make check`
passed, including `go vet ./...` and the full Go suite. The seeded lifecycle
browser journey and full browser suite passed against a freshly built panel
binary with embedded assets; frontend unit tests passed (19/19). Hosted CI for
this source is tracked in `PROGRESS.md`.

**Acceptance criteria**

- Opening the overview and lifecycle views does not issue an additional backup
  inventory request; the summary uses the recovery endpoint’s latest-backup
  evidence.
- Opening the overview and lifecycle views does not read log files; requesting
  recent evidence loads bounded web-server, PHP-FPM, and access-log samples.
- The overview explicitly says when logs have not been sampled; HTTP 503 evidence
  appears only after the access-log sample is loaded. Failed queries remain
  distinct from an empty log sample or a clean result.
- Frontend unit tests, focused overview and lifecycle journeys, the full browser
  E2E suite, and `make check` pass against the current frontend assets.

### M0au — Gate customer WordPress credentials on verified ownership (P1)

**Status:** Implemented and verified on exact source `07726007`; the hosted
integration and customer UI E2E jobs passed in
[run 45108095198](https://github.com/cyberducttape/StePanel/actions/runs/45108095198).
The seeded browser flow covers customer-side DNS proof before WordPress
credentials and site creation.
Customers first claim and verify a DNS TXT record; only then are the WordPress
title and credentials shown. Copy controls are available for the DNS record,
and editing the site or domain invalidates the proof. The backend continues to
enforce the claim independently. The installer cancellation message now
accurately distinguishes securing saved settings from changing host services.

**Acceptance criteria**

- Site creation is never submitted before a customer proves domain control.
- WordPress credentials remain hidden until the current site/domain claim is
  verified; changing either requires a fresh proof.
- Customers can copy the exact TXT name and value and see clear verification
  status.
- Browser E2E covers the above flow and installer tests reject the inaccurate
  cancellation claim. Exact-source UI and integration CI pass.

### M0av — Reauthenticate customer credential mutations (P1)

**Status:** Implemented and locally verified. `make check`, full browser E2E
(33 passed, one expected skip), API/release/documentation checks pass. The
exact-source Go and customer UI E2E jobs passed in
[run 45108095198](https://github.com/cyberducttape/StePanel/actions/runs/45108095198).
Password changes and MFA
enrollment/replacement require the current password and, if an authenticator
is already configured, a fresh current-factor code. A per-account/client
limiter bounds guesses; the existing TOTP replay store prevents reusing a
sign-in code. Audit actions distinguish enrollment from rotation. Lost-factor
recovery remains a separate recovery flow.

**Acceptance criteria**

- A session without the current password cannot change a password or replace
  MFA; an MFA-protected account also needs a valid, unused current-factor code.
- Successful changes revoke existing sessions and are durably audited.
- Tests cover denied session-only changes, incorrect credentials, valid
  reauthentication, TOTP replay protection, and the browser forms.
- Full Go checks and exact-source hosted CI pass.

### M0aw — Keep guided installer binaries from diverging (P2)

**Status:** The original two-binary journal is extended to cover the full
three-executable set under M0ay. On exact code source `237d1d16` the hosted
[integration workflow](https://github.com/cyberducttape/StePanel/actions/runs/45103029684)
passed, including shell contracts, Go tests, race tests, ENOSPC drills, and
database restore checks. The [seeded browser job](https://github.com/cyberducttape/StePanel/actions/runs/45103029684)
also passed. The cancellation message and saved-settings contract remain
covered by the shell suite. The installed-host evidence for the three-binary
install is recorded under M0ay.

**Acceptance criteria**

- The installer is never invoked after only one new executable has been
  published.
- A failed publication restores the previous complete executable set (or
  restores the original absence of files).
- After forced process stops between publications, the next run restores the
  old set before building and publishes matching new artifacts.
- Shell syntax, contract tests, full local checks, and exact-source hosted CI
  pass.

### M0ax — Report first-install blockers together (P1)

**Status:** Implemented and locally verified on `788882f3`; aggregation,
ordering, shell syntax, ShellCheck, installer-contract tests, and full `make
check` pass. The read-only host preflight now runs all
independent OS/package-manager, systemd, quota, and offsite-tool checks, prints
each blocker with a fix, then gives an exact recheck command. Quota and remote
backup requirements remain required; this does not modify mount settings or
configure a remote. Exact-source integration, docs, CodeQL, seeded browser E2E,
and installed-host smokes passed on `07726007`: Ubuntu 24.04 + Caddy, AlmaLinux
9 + Apache, and Rocky Linux 9 + Apache. The smoke reports record one
recovery-matrix iteration per host and the missing project-quota enforcement
in the disposable containers; this is not repeated or physical-host
certification.

**Acceptance criteria**

- The report lists every independent failed precondition in one invocation,
  with a documented repair and recheck command.
- All passing checks are visible even when another check fails; any blocker
  still prevents the guided build from continuing.
- If quota tools are missing but mount options are present, label enforcement
  as deferred and unverified rather than as a satisfied requirement; the
  guided pre-question and full installer reports must both use that label,
  and the post-package check must still block before hosted-site provisioning.
- No privileged host setting is changed by the doctor. The installer repeats
  prerequisite checks before mutation. A non-root invocation prints a
  copyable, shell-quoted sudo command for the read-only preflight.
- Aggregation tests, shell syntax, ShellCheck, documentation checks, and
  exact-source hosted CI pass.

### M0ay — Ship the documented API CLI with native installs (P1)

**Status:** Verified on code source `237d1d16`. Hosted [integration](https://github.com/cyberducttape/StePanel/actions/runs/45103029684),
[documentation](https://github.com/cyberducttape/StePanel/actions/runs/45103898528),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/45103898618),
and [release evidence](https://github.com/cyberducttape/StePanel/actions/runs/45103945286)
passed. The packaged Ubuntu host smoke passed on code-identical commit
`df838fa9`; its WordPress recovery measurement and scope are in the
[smoke report](docs/lab-results/2026-11-23-ubuntu-packaged-first-host-smoke-df838fa.md).
On clean source archive `c4870e6f`, the guided installer downloaded and
SHA-256-verified the pinned Go 1.26.11 toolchain, built all three binaries,
ran the actual CLI help command, and reached a mocked privileged handoff.
This validates source bootstrap and build behavior, not a second installed-host
run.
The OpenAPI-backed `stepanelctl` is built by source and release workflows,
included in release archives and the container, and installed as a root-owned
`/usr/local/bin/stepanelctl`. Guided publication uses a durable rollback
journal for the entire executable set and restores it before rebuilding after
an interrupted rename. Installer cancellation distinguishes secured,
reusable setup settings from system-service changes.

**Acceptance criteria**

- Linux amd64/arm64 release archives contain a runnable CLI and the release
  verifier executes its help command. Verified locally with GoReleaser
  v2.17.1 snapshot archives.
- `make build` and the guided source installer build all three binaries;
  interrupted publication restores the complete prior set before rebuilding.
- From a clean source archive with `GO` unavailable, guided setup downloads
  only the pinned compiler to temporary storage, verifies its checksum, builds
  all three executables as the invoking user, and completes the host handoff.
- Native install and upgrade snapshot the CLI target for rollback, install it
  with root ownership, and verify its executable help command in the packaged
  installed-host smoke. The Ubuntu 24.04 + Caddy + MariaDB run passed on
  `df838fa9`; WordPress HTTPS/database restore measured 4,435 ms and one
  recovery-matrix iteration passed. See the
  [scoped smoke report](docs/lab-results/2026-11-23-ubuntu-packaged-first-host-smoke-df838fa.md).
- README and API/installation guides state how the CLI is obtained; API
  generation and shell-completion artifacts remain checked.
- Focused installer and release checks and full local `make check` pass.
  Exact-source hosted integration and browser E2E passed on code-equivalent
  `237d1d16`; the host installation smoke passed on `df838fa9`.

### Release publication safety — verify artifacts before publishing

**Status:** Implemented and verified on `01d4a2d5`. The complete integration
workflow [45030061090](https://github.com/cyberducttape/StePanel/actions/runs/45030061090), documentation [45030061075](https://github.com/cyberducttape/StePanel/actions/runs/45030061075), CodeQL [45030061024](https://github.com/cyberducttape/StePanel/actions/runs/45030061024), and release-evidence workflows passed.

The tag workflow now builds a nonpublishing GoReleaser distribution, generates
the exact-source evidence and SBOM, and runs the release archive verifier
before GoReleaser, container, provenance, or release-attachment publication.
It repeats artifact verification against the final distribution after the
publish build. `make release-check` enforces this ordering. This prevents
known packaging and evidence failures from first appearing after a release is
already public; it does not certify the host installation or recovery gates.

**Acceptance criteria**

- A tag build produces GoReleaser archives with `release --skip=publish`.
- Exact-source evidence, SBOM generation, and artifact verification complete
  before the first publishing action.
- The ordinary release build still verifies the final distribution and
  attaches evidence and SBOM.
- `make release-check` fails if the pre-publication ordering is removed or
  reordered.
- Exact-source release workflow CI passes. No release is published as part of
  this change.

The pinned GoReleaser v2.17.1 command was also run in an isolated temporary
checkout with a local-only test tag. `release --skip=publish --clean` built
both Linux architecture archives and `SHA256SUMS` without publishing. The
local tag was not pushed and no release was created.

### M0aa — Make first-time evaluation and support tiers clear (P1)

**Status:** Partially implemented. The installation guide now opens with a
three-way choice (local preview, disposable/staging Beta, or stop before
production) and states the real prerequisites and release limitation before
the platform matrix. The API workflow guide also clarifies that restore-to-
staging returns a queued job and that the operator must wait for completion.
These documentation changes reduce ambiguity but do not create a supported
fresh-install archive.
The local preview's primary command is now `bash scripts/evaluate-local.sh`,
so a newcomer does not need GNU Make. `make evaluate` remains a convenience
alias. README and installation guidance list Bash and distinguish optional
Make; preflight help points to the direct command. Shell contracts exercise
the Bash path with a deliberately failing `make` shim and still exercise the
Make target separately. Focused contracts, the complete shell-contract suite,
documentation links/references, and `make check` pass locally.
The native source wrapper also accepts `--config FILE` and `--dry-run` and
passes those arguments, preserving path boundaries, to `install.sh`. A saved
setup file can now use the same pinned build and preflight path without a
manual binary build or switching installer commands. Regression coverage
includes a config path containing spaces, relative paths from outside the
checkout, and rejection of missing files, symlinks, and duplicate config
input. File ownership, mode, and link count are checked before toolchain
download; contract tests run with the CI `GOTOOLCHAIN=local` default to catch
environment-sensitive bootstrap behavior. `stepanel setup` recommends this
wrapper for source checkouts and shows direct `install.sh` commands only when
matching binaries have already been built. The installation guide shows this
workflow. Installer publication uses a durable rollback journal around each
binary rename; injected rename failures and process kills between publication
steps restore the previous complete executable set before a later build.
After saving settings, the CLI detects whether it is running from a source
checkout or an extracted release directory and prints only that context's
matching installer commands. Each command changes into the detected absolute
directory in a subshell, so it remains copyable from any later working
directory. Tests cover both layouts and quote paths containing spaces and
apostrophes.
The `stepanelctl doctor` command now presents readiness failures as a readable
checklist by default, includes the diagnostic and suggested next step, retains
JSON output for automation, and exits nonzero for critical/not-ready or
unrecognized status.
Its control-plane check uses the bounded readiness query rather than running
SQLite `quick_check` inline. The report now shows the latest background
integrity result and timestamp, warns when no scan has completed since startup,
warns when a successful scan is over 30 minutes old (the maintenance cadence is
15 minutes), flags a timestamp ahead of the host clock, and gives recovery
guidance on failure. `make check` and the focused integrity-doctor race test
pass locally; exact-source hosted CI is pending.
The integrated guided installer also secures a previously saved setup file
before reusing it, so `./stepanel setup` followed by
`sudo ./install.sh --guided` no longer offers a reuse path that fails root
ownership validation. The promotion rejects symlinks, unexpected owners,
nonprivate permissions, and multiply linked files. A shell contract test
executes those rejection rules and ensures both reuse and regeneration pass
through the same promotion step.
Local evaluation guidance and guided-setup
support labels, pre-mutation platform classification, and release archive
documentation requirements are in place. The one-command source installer
now runs a read-only OS/systemd/quota/rclone-prerequisite check before
downloading Go or compiling; the privileged native installer repeats its host
checks before changes. A newcomer can run a loopback-only
evaluation or use the source bootstrap to build as an unprivileged user and
then start the guided native installer. Both entry points share a Go
bootstrap: when Go is unavailable, too old, unidentified, or
`GOTOOLCHAIN=local`, it downloads the pinned compiler into a temporary
directory and verifies its SHA-256 before extraction. It does not install host
packages. Contract tests cover missing/old Go, local mode, build before
privilege elevation, checksum rejection, and the `make evaluate` entry point.
Exact-source CI on `a3cbb37a` passed the full Go suite, race suite, release
checks, and browser workflow
([integration run 45012500672](https://github.com/cyberducttape/StePanel/actions/runs/45012500672));
Documentation and CodeQL also passed. That integration workflow did not run
the installer/bootstrap contract scripts. The current workflow now runs
`make shell-contract-check`, which includes those regressions. Exact-source CI
on `a6dfa99a` passed the full integration workflow, including shell contracts,
Go vet/tests, race tests, recovery drills, real ENOSPC checks, MySQL and
PostgreSQL restore tests, container checks, and deployment-manifest validation
([run 45027692263](https://github.com/cyberducttape/StePanel/actions/runs/45027692263));
the browser suite also passed. Documentation and CodeQL passed on the
code-equivalent documentation-only descendants. The setup guidance and installer
preflight identify Ubuntu 24.04 + Caddy + MariaDB as the recommended Beta
profile. The exact-current-source installed-host smoke on `6af93e88` passed on
Ubuntu 24.04 + Caddy + MariaDB ([host run
45037318148](https://github.com/cyberducttape/StePanel/actions/runs/45037318148)).
The GoReleaser snapshot was installed on a disposable systemd container,
`post_install_status=complete`, and the WordPress HTTPS/database recovery proof
measured 4.259 seconds; one recovery-matrix iteration passed. Without project
quotas, Git deployment was refused and the live site remained unchanged; this
proves fail-closed behavior, not quota enforcement. The report records scope
and limitations in
[`2026-11-22-ubuntu-packaged-first-host-smoke-6af93e8.md`](docs/lab-results/2026-11-22-ubuntu-packaged-first-host-smoke-6af93e8.md).
The later exact-source run on `e5fae4ad` also completed installation and its
one-iteration recovery matrix; the installed WordPress HTTPS/database proof
measured 4.539 seconds ([run 45040904045](https://github.com/cyberducttape/StePanel/actions/runs/45040904045),
[report](docs/lab-results/2026-11-22-ubuntu-packaged-first-host-smoke-e5fae4a.md)).
That artifact revealed a noisy missing-`/var/www/sites` inventory warning on
fresh hosts. The current installer counts a missing directory as zero and
adds a shell regression. Exact-source host run `d2695cd0` now verifies that
the warning is gone; installation completed and the WordPress HTTPS/database
recovery proof measured 4.512 seconds ([run 45060415981](https://github.com/cyberducttape/StePanel/actions/runs/45060415981),
[report](docs/lab-results/2026-11-22-ubuntu-packaged-first-host-smoke-d2695cd.md)).
The subsequent packaged install/recovery smoke on `a309055d` also passed:
installation completed, the WordPress HTTPS/database recovery proof measured
6.996 seconds, and one recovery-matrix iteration passed ([run
45069529493](https://github.com/cyberducttape/StePanel/actions/runs/45069529493),
[report](docs/lab-results/2026-11-22-ubuntu-packaged-first-host-smoke-a309055d.md)).
The disposable host lacked project quotas, so Git deployment was refused and
the live site stayed unchanged; this proves fail-closed behavior, not quota
enforcement. This remains a single disposable-host iteration, not host
certification.
The administrator's first-run dashboard also keeps the long unavailable-
capability inventory collapsed behind a disclosure that reports its current
counts. Infrastructure navigation expands it directly; service inventory
and security checks remain available. The browser regression covers the
collapsed default and navigation expansion. Exact-source CI on `665f3df4`
passed the full integration suite and browser accessibility suite
([run 45049477424](https://github.com/cyberducttape/StePanel/actions/runs/45049477424));
documentation passed on the code-equivalent parent, and CodeQL passed on
`665f3df4`.
Although this host smoke postdates the source bootstrap changes in `a3cbb37a`,
the GoReleaser archive does not contain or execute `scripts/install-guided.sh`;
it is not runtime evidence for that separate bootstrap path. A clean temporary checkout at
`7e1d88df` subsequently downloaded and SHA-256-verified the real Go 1.26.11
archive, built both binaries, removed the temporary toolchain, and reached a
mocked `sudo` handoff. The bootstrap now reads the version directly from
`go.mod`; release checks ensure both supported architectures have a checksum
entry, and its contract test rejects a newly pinned version until that entry
exists. The fallback shows compiler download progress. A clean temporary
checkout ran `make evaluate` with Go pointed at a nonexistent path: the
verified toolchain built the preview, it served HTTP 200 on `127.0.0.1:8080`,
Ctrl-C stopped it, and its temporary compiler/build directory was removed.
This verifies local evaluation and compiler/build behavior, not the native
installer on an installed host.
This is one disposable-container pass, not repeated host certification. A
clean-host smoke of the official tagged release archive also remains open:
no currently published stable archive supports a fresh Ubuntu 24.04 install.
These release and repetition gates keep M0aa partial; the source-based staging
path is usable, but it is not a production-ready release.

The six-host run on `0cadd0ab` exposed a current EL 9 first-install blocker:
Rocky and AlmaLinux 9.9 repositories do not provide a package named
`php-curl`; their `php-common` package supplies and enables `curl.so`. The
installer now selects `php-curl` when DNF repositories provide it, otherwise
falls back to `php-common`, and checks that the installed PHP runtime actually
loads curl. The same run also showed that matching the
literal version string `9` misclassified real `9.9` hosts as outside the
matrix. EL support classification now matches the major series while telling
operators to verify the exact minor release evidence. Four EL jobs failed on
the old source before this correction. Exact-source integration, documentation,
CodeQL, and release-evidence checks pass on `07726007`. Exact-source
installed-host reruns passed on Ubuntu 24.04 + Caddy + MariaDB, AlmaLinux 9 +
Apache + MariaDB, and Rocky Linux 9 + Apache + MariaDB. The two EL 9 smoke
reports record the successful `php-common` fallback and installed WordPress
recovery proof. Each profile passed one disposable systemd-container recovery
iteration; this does not close repeated certification, project quota, physical
power-loss, or tagged-release installation criteria. See the [Ubuntu
report](docs/lab-results/2026-11-23-ubuntu-packaged-first-host-smoke-07726007.md)
and [EL 9 report](docs/lab-results/2026-11-23-el9-first-install-phpcurl-fallback-07726007.md).
The later packaged smoke on `2e9e06e9` also passed on Ubuntu 24.04 + Caddy +
MariaDB: installation and cleanup completed, one recovery-matrix iteration
passed, and application-level WordPress recovery measured 4,398 ms. Its
disposable-host scope and missing project-quota limitation are recorded in
[`the report`](docs/lab-results/2026-11-23-ubuntu-packaged-first-host-smoke-2e9e06e.md)
([run 45129211602](https://github.com/cyberducttape/StePanel/actions/runs/45129211602)).
The exact-current-source, non-publishing versioned `v0.9.0` candidate
([run 45317837019](https://github.com/cyberducttape/StePanel/actions/runs/45317837019),
source `93c60aa`) also built and installed successfully on the recommended
Ubuntu profile. Cleanup completed, one recovery iteration passed, and the
WordPress HTTPS/database proof measured 4,367 ms. Both architecture archives,
checksums, exact-source evidence, and SPDX SBOM passed; no release was
published. The container lacked project quotas, so Git deploy correctly
refused to proceed. See
[`the current candidate report`](docs/lab-results/2026-11-24-v0.9.0-ubuntu-install-45317837019.md).
This improves current-source install evidence but does not make a published
stable archive available or close repeated/physical-host certification.

**Acceptance criteria**

- A newcomer can start a loopback-only, no-host-mutation UI evaluation without
  configuring offsite storage using `bash scripts/evaluate-local.sh`; `make
  evaluate` is an optional alias. The launcher fixes the
  listener to loopback, clears inherited StePanel settings, and the
  instructions and native setup greeting clearly prohibit network exposure
  and real customer data in development mode. Setup users are shown the
  evaluation escape route before storage prerequisites are checked. README's
  quick-start link lands directly on this path and states the compiler and
  first-download requirements there. A clean current-source checkout without
  an installed Go command also ran `make evaluate`, served HTTP 200 on loopback
  only, and removed its temporary compiler after Ctrl-C. A temporary checkout
  at `c5a2765e`
  completed `make evaluate`, served the dashboard with HTTP 200, and was
  observed listening only on `127.0.0.1:8080`; Ctrl-C stopped it. The exact
  command was repeated from a clean source archive at `5e23f579`, isolated
  from the checkout's existing data; it built successfully, returned HTTP
  200, and listened on `127.0.0.1:8080` before clean shutdown. This is UI
  evaluation evidence only, not a native installation test.
- On exact source `853b65dc`, a clean temporary checkout ran the documented
  `bash scripts/evaluate-local.sh` command, returned HTTP 200, and listened
  only on `127.0.0.1:8080`. The temporary process, build directory, and
  worktree were removed afterward. This confirms the preview path, not native
  installation or production readiness.
- `bash scripts/evaluate-local.sh` and the guided native installer can
  bootstrap the pinned Go toolchain without installing a system Go package.
  Full development commands
  (`make check`, `go run`) state that an installed Go 1.21+ with automatic
  toolchain selection is required. Contract tests cover missing/old Go, local
  mode, checksum failure before extraction/build/privilege elevation, and
  unsupported options.
  `scripts/install-guided.sh` builds all three executables as the normal user,
  then invokes the guided host installer through `sudo`. Source-based installation
  remains clearly distinct from a published release archive. The manual
  fallback `make build && sudo ./install.sh --guided` also builds
  `stepanel`, `stepanel-root`, and `stepanelctl`; local evaluation builds only
  the panel binary. `make evaluate` remains an optional alias for the Bash
  evaluation command.
  Before fetching the compiler, the source bootstrap checks for an existing
  installation and routes it to the ordinary upgrade path; fresh hosts must
  pass the read-only OS/systemd/quota/rclone preflight before any toolchain
  download or build. A regression verifies that a failed preflight launches
  no build.
- Guided setup recommends Ubuntu 24.04 + Caddy + MariaDB while identifying
  which alternatives are outside the current installed-host smoke matrix;
  interactive and unattended installer defaults select that same profile.
  Pushes that change the installer, privileged integration, or installed-host
  smoke paths run the recommended-profile installed-host workflow; the workflow
  uses path filters, so documentation-only and unrelated source pushes do not
  trigger it. Manual runs still default to the complete six-combination host
  matrix. The host workflow tests the packaged snapshot, not
  `scripts/install-guided.sh`; a real installed-host run of that source
  bootstrap remains unverified.
  The installer labels the recommended profile as Beta and explicitly marks
  the other smoke-tested tuples as one-iteration Beta with repeat acceptance
  still pending, before the user commits to host configuration.
  `install.sh --guided` identifies the host OS and prints its recommended
  profile evidence before it asks for remote-storage or administrator secrets.
  After the operator selects a web server and database, the wizard runs the
  same support-tier classifier as the installer and displays the exact OS and
  stack evidence. It requires acknowledgement when the tuple is outside the
  host-smoke matrix, before checking storage, collecting credentials, or
  writing settings; the full pre-mutation preflight repeats the classification.
- Guided setup checks for rclone, a configured remote, and its config path
  before asking for hostname or credentials; it tests the selected destination
  before collecting administrator passwords/MFA and before saving installer
  settings.
  The installation guide places the Ubuntu rclone install/config commands
  before the source build, preventing a predictable build-then-abort restart;
  the written probe step now matches the wizard's Step 3.
  Integrated `install.sh --guided` now checks for systemd and enforced user
  quotas before invoking the answer wizard, then repeats those checks during
  the full pre-mutation preflight. This avoids collecting credentials on a
  host that cannot run the native installation.
  Quota failures link directly to a new safe preparation guide with mount
  inspection commands, separate ext4/XFS option guidance, and explicit
  warnings against applying filesystem-format or feature commands blindly.
- The first-login browser journey presents health findings, opens the
  WordPress form from the primary next action, verifies template fields, and
  updates the next task to site backup and restore proof. The website area is
  labeled for customers, and its create action says “Create your first
  website” when the active inventory is empty and “Create another website”
  after a site exists. The current complete disposable Chromium suite exercises
  both the administrator first-run journey and a freshly provisioned account
  owner; both passed in the latest run. If site inventory is unavailable, the
  website list explains that sites may still exist and offers an in-place
  retry; the retry path is browser-tested. Optional hosting-account and
  website-placement settings start collapsed so the first-site form emphasizes
  the site name, domain, and starting point; both account roles are covered.
  The operator site workspace keeps Overview, Guided workflow, Domains, and
  Backups in the primary tab row; specialist Runtime, Deployments, Databases,
  Logs, Workers, Tasks, Security, and Settings remain available under
  Advanced administration. Direct links open the matching advanced tab.
- The install guide reports current-code evidence separately from historical
  runs and never presents smoke coverage as production certification.
- The next release candidate archive contains the matching guided setup,
  installer, helpers, and assets. The artifact verifier requires the support-
  tier, systemd-, quota-, package-, database-version-, change-plan-, and
  env-file helpers.
  The artifact verifier checks archive contents and executes the packaged
  setup command on the native CI architecture. The installed-host workflow now
  builds an exact-source GoReleaser snapshot, validates its packaged setup and
  runtime assets, and runs the disposable host smoke from the extracted
  archive instead of the checkout. The first hosted run of that path
  [44988484953](https://github.com/cyberducttape/StePanel/actions/runs/44988484953)
  built and extracted the archive and passed installation plus lifecycle
  interruption drills, but failed when the disposable host timed out fetching
  WordPress/WP-CLI fixtures. The workflow now stages and verifies those pinned
  fixtures on the runner before host mutation and mounts them read-only for
  recovery. On exact source `e04235f8`, full integration CI passed
  ([44990700232](https://github.com/cyberducttape/StePanel/actions/runs/44990700232));
  host run [44990700250](https://github.com/cyberducttape/StePanel/actions/runs/44990700250)
  passed installation, WordPress HTTPS/database restore proof (4.351 s),
  database/broker outage recovery, and one recovery-matrix iteration. Matching
  integration CI [44990700232](https://github.com/cyberducttape/StePanel/actions/runs/44990700232)
  passed on the same source. This snapshot workflow does not replace a
  clean-host test of the official tagged archive or repeated certification.
- Installer preflight reports OS, web-server, and database support tier and
  actionable failed prerequisites (including availability of required base,
  PHP, database, web-server, and selected optional-feature packages in
  repository metadata, requested
  database versions/streams, a live systemd manager, user-quota mount settings
  on `/var/www` and `/home`) before host mutation;
  it verifies active enforcement immediately if tools are available, otherwise
  reports the deferral and fails before site services if enforcement is absent.
  Before confirmation, the change plan lists selected optional integrations,
  their host/configuration effects, post-commit activation, and services that
  will intentionally remain stopped. It distinguishes package installation
  from web-route configuration when the selected server does not support that
  integration.
  First-site creation,
  backup, and restore proof are verified on the selected supported profile.
- After successful installation, the console prints the exact HTTPS URL and
  stack-specific first-login steps. It explains DNS/firewall prerequisites,
  Caddy automatic certificate issuance, the Apache Certbot command when
  installed, and the TLS setup required for Apache without Certbot or
  OpenLiteSpeed. It points administrators to the live first-run checklist and
  the services to inspect when startup is unavailable.
- Generated/manual installer settings are parsed by a separately tested
  loader that rejects truncated quotes, trailing data, unsupported escapes,
  non-root ownership, unsafe permissions, and symlinks before accepting any
  setting. The exact parser helper is required in release archives.
- When rclone is installed but has no remote, setup now offers to launch its
  interactive configuration on the same terminal after the operator reviews
  the exact host/stack support tier. It re-reads the remote list and verifies
  the selected destination with the existing write/read/delete probe. The
  source preflight accepts this unconfigured state while still failing before
  build if rclone is absent or an existing configuration is malformed. The
  wizard and README document both paths. Focused setup tests, installer
  preflight contracts, and local `make check` passed; exact-source hosted CI
  has not yet run for this change.
- Customer WordPress provisioning now requests a DNS TXT claim and verifies it
  in the create form before sending the site-creation request. The verified
  state is bound to the current site name/domain pair and is invalidated when
  either changes. The server continues to recheck ownership; browser state is
  only an admission aid. Customer E2E covers the rejected-until-verified path,
  proof display, successful verification, and invalidation after editing.

The release artifact and host certification criteria require a tagged
candidate and disposable hosts; this milestone does not authorize publishing.

### M0ab — Stream customer file downloads within explicit resource limits (P1)

**Status:** Implemented and verified on a disposable installed host. Customer downloads now use a
descriptor-bound stream through the local store or typed root-broker protocol,
so files larger than the 2 MiB text-editor limit do not become JSON payloads
or whole-file panel allocations. Downloads are capped at 40 GiB, limited to
two concurrent transfers per tenant and eight per host, and receive a
size-aware overall deadline plus a per-write stall deadline. The customer file
manager and workflow guide now distinguish editor, upload, and download limits
before a transfer begins. The full local Chromium suite passes. The installed-
host file-manager smoke passed in run
[45017173718](https://github.com/cyberducttape/StePanel/actions/runs/45017173718)
on source `55c1421a`; the file manager, broker, privileged helper, and smoke
script are unchanged through current `main` (`d9456c58`). This is one
disposable-host pass, not repeated certification.

**Acceptance criteria**

- A customer can download a regular file larger than 2 MiB with exact bytes,
  `Content-Length`, attachment disposition, and browser-safe response headers.
- The editor's JSON read limit remains 2 MiB; download size and concurrency
  limits are enforced in the panel and privileged helper.
- The production helper opens the file beneath the site root without following
  symlinks and streams from that descriptor; the broker bounds metadata and
  transfer duration without buffering file contents.
- Tests cover large HTTP downloads, broker socket framing, bounded concurrency,
  and helper smoke verification. The installed helper smoke and full local
  Chromium suite pass. Exact-source CI for current `main` is tracked separately
  in `PROGRESS.md`; host smoke evidence is tied to the unchanged file-transfer
  source, not represented as an exact-current-commit full installation run.

### M0ac — Keep first-install instructions aligned with viable releases (P1)

**Status:** Corrected locally. The README and installation guide no longer
recommend a fresh install from v0.7.0, which the compatibility record says
cannot complete on current Ubuntu 24.04. They identify `0.8` as an unsupported
pre-release, direct fresh users to local evaluation or current-source Beta
staging, and link older archives only to tested upgrade procedures. The
README also links each stack choice to its evidence tier rather than describing
all implemented adapters as equally supported. The release contract now checks
that these first-install warnings remain present and that the legacy v0.7
download command is not restored.

**Acceptance criteria**

- The first-install entry points distinguish no-host-change local evaluation,
  current-source staging, tested legacy upgrade paths, and the absence of a
  supported tagged fresh-install archive.
- The first-screen stack summary states the recommended profile and links to
  the exact Beta, experimental, and unsupported platform combinations.
- README and installation guide do not provide a v0.7.0 fresh-install command
  or imply that the v0.8 prerelease is supported.
- The documentation/release contract fails if the known-broken v0.7.0 fresh
  install recipe is reintroduced. Internal links and documentation checks
  pass.

### M0ad — Match the optional SpamAssassin service to supported packages (P2)

**Status:** Implemented and verified on exact source `94fbf293`. The Ubuntu
24.04 installed-host run [45002811271](https://github.com/cyberducttape/StePanel/actions/runs/45002811271)
installed the `spamd` package, disabled `spamd.service`, and recorded
`post_install_status=complete`. The full integration run
[45002811283](https://github.com/cyberducttape/StePanel/actions/runs/45002811283)
passed, including race tests; local `make check` and the package-preflight
contract passed. The packaged first-install result is recorded in the
[smoke report](docs/lab-results/2026-11-22-ubuntu-spamd-service-smoke-94fbf293.md).

**Acceptance criteria**

- Debian/Ubuntu optional-mail preflight and installation include the `spamd`
  package, and service state capture, restoration, activation, and deactivation
  use `spamd.service`.
- The supported-profile installed-host smoke that installs optional mail
  without activation completes without a false pending SpamAssassin action.
- The focused package contract, full local checks, and exact-source hosted CI
  pass. The smoke result is recorded with its source commit and limits.

### M0z — Prevent stale customer file edits from overwriting newer content (P1)

**Status:** Implemented and verified locally. Exact-source CI
[44697055872](https://github.com/cyberducttape/StePanel/actions/runs/44697055872),
CodeQL [44697055748](https://github.com/cyberducttape/StePanel/actions/runs/44697055748),
documentation [44697055795](https://github.com/cyberducttape/StePanel/actions/runs/44697055795),
and release evidence [44697340919](https://github.com/cyberducttape/StePanel/actions/runs/44697340919)
passed on commit `0afc3c6e`. The installed-host file-manager/recovery smoke
[44697055839](https://github.com/cyberducttape/StePanel/actions/runs/44697055839)
was cancelled before completing. The later six-host smoke
[44729127788](https://github.com/cyberducttape/StePanel/actions/runs/44729127788)
passed the installed file-manager checks on all six supported OS/web-server
combinations on code-equivalent source; see the [reviewed result](docs/lab-results/2026-11-20-six-host-install-smoke-059ba72.md).

**Acceptance criteria**

- File content reads return a strong revision derived from the exact bytes.
- The editor binds the revision to its immutable site/path context and sends
  it with saves; stale writes return HTTP 409 without changing file contents.
- The privileged file helper enforces the same comparison for production
  hosting-account files, while unconditional uploads remain explicit and
  supported.
- The customer editor offers a deliberate reload after a conflict.
- Go tests cover local and API stale writes, helper contract tests cover the
  production helper, and browser E2E covers conflict/reload behavior. Full
  `make check`, JavaScript unit tests, ShellCheck, and exact-source CI pass.

### M0y — Keep customer site and file workflows bound to their UI context (P1)

**Status:** Verified locally on current `main` (`c2f976ec`) with a fresh
binary and the seeded account-owner browser journey (1/1 passed). Hosted
`ui-e2e` and accessibility checks passed in run
[44741756748](https://github.com/cyberducttape/StePanel/actions/runs/44741756748)
on exact source `de6f96f7`. The journey checks WordPress creation controls, no
uncaught browser errors, failed-inventory retry, delayed file-read invalidation
after a site switch, stale rows during folder navigation, and no cross-site
write. Local frontend unit tests passed 17/17. Exact-source CI on `de6f96f7`
passed the full Go suite and race tests, and the dedicated PR-CI run passed as
well. This closes the listed customer UI regressions; it does not close
separate host-certification gates.

**Acceptance criteria**

- Customer WordPress creation controls initialize without uncaught JavaScript
  errors and expose the same supported template fields as the operator flow.
- File reads, listings, and mutations stay bound to the site/path that created
  the action; navigation clears stale entries and invalidates outstanding
  reads before their response can populate the editor.
- Failed site/domain inventory requests remain visibly unavailable, preserve
  prior successful data, and offer retry rather than reporting an empty list.
- Customer navigation has one route/hash authority and does not use the
  operator dashboard's scroll spy.
- Seeded-browser regressions cover account-owner template initialization,
  delayed reads across site switching, stale rows during folder navigation, and failed
  inventory with successful retry. These passed in the local disposable
  browser run and hosted `ui-e2e` job.
- Documentation describes only implemented customer workflows and the actual
  single-host network prerequisite.

### M0x — Bind cPanel import to a destination plan and report recovery evidence (P1)

**Status:** Implemented and verified by exact-source CI integration
[44665400225](https://github.com/cyberducttape/StePanel/actions/runs/44665400225),
including the browser migration preflight/report journey. This connects archive inventory,
destination admission, import, and per-site recovery evidence; it does not yet
provide an isolated customer-visible staging hostname or verify the final
public DNS/TLS route before activation.

**Acceptance criteria**

- cPanel uploads have an independent configurable limit up to 40 GiB while
  expanded-tree, entry, capacity, and database-storage admission stay bounded.
- The operator can build a destination-specific plan showing source mappings,
  target account, current route/name conflicts, database support, capacity,
  and rollback/cutover guidance. Import requires a short-lived signed token
  bound to that plan and the selected target/database options.
- The worker repeats capacity and destination conflict checks after taking the
  site mutation lock. Dashboard and CLI use the same plan/import API contract.
- Migration evidence reports the site's verified backup, measured rehearsal
  duration, observed backup age, rehearsal cadence, and whether application
  proof is configured. RPO age and RTO duration are explicitly measurements,
  not guarantees.
- Focused regression tests, generated API artifacts, JavaScript syntax checks,
  and `make check` pass. Installed-host 40 GiB capacity and application/TLS
  cutover drills remain separate release evidence.

### M0w — Correlate site HTTP 503s with host evidence (P1)

**Status:** Implemented and verified by exact-source CI integration
[44665400225](https://github.com/cyberducttape/StePanel/actions/runs/44665400225),
including seeded browser E2E. Installed-host checks remain open. This diagnostic correlates bounded snapshots and does
not claim causality across unsynchronized sample times.

**Acceptance criteria**

- The site resource probe reports systemd slice active/substate/result and,
  when cgroup v2 exposes it, OOM and OOM-kill counters. It validates the
  systemd-provided cgroup path before reading kernel counters.
- A 503-focused incident panel summarizes request count/sample time and
  connects PHP-FPM queue/saturation, systemd/cgroup state, database probes,
  disk/quota headroom, web-server error evidence, certificate state, and the
  latest deployment without dumping full logs or asserting unproven causes.
- Cumulative OOM counts, unavailable signals, and point-in-time “healthy”
  samples are labeled so they cannot be mistaken for request-specific proof.
- Regression coverage verifies the status evidence contract and the
  authenticated browser journey. Full repository checks pass; production host
  behavior remains to be verified on supported cgroup/systemd combinations.

### M0v — Make risky configuration changes reviewable and verifiable (P1)

**Status:** Implemented for PHP runtime, site resource, managed domain route,
TLS issuance, Caddy `.htaccess` translation, WordPress staging promotion, and
Git release rollback previews. Exact-source CI integration and browser E2E
passed in [44665400225](https://github.com/cyberducttape/StePanel/actions/runs/44665400225).
Preview payloads show the exact
structured before/proposed state and identify manual versus automated checks.

**Acceptance criteria**

- Before confirmation, preview displays the exact structured before/after
  payload, effects, validation commands/checks, recoverability, and expected
  outcomes at interruption boundaries. Stale or expired previews are rejected.
- The confirmation dialog renders this plan as text without interpreting
  configuration content as HTML and bounds displayed configuration size.
- Successful mutation responses distinguish applied state from queued or
  pending state and identify the verification evidence. Certificate issuance
  is not successful until the active leaf certificate matches the requested
  hostname and is unexpired.
- Tests cover plan completeness, stale preview rejection, certificate
  hostname verification, and post-change proof fields. Full `make check`
  passes; authenticated browser E2E and exact-source hosted CI pass before
  release.
- This standard applies to the enumerated preview workflows. Other destructive
  workflows without preview-token enforcement remain separate open work; the
  plan is not a claim that every mutation in the product is now preview-gated.

### M0t — Report evidence-based site recovery confidence (P1)

**Status:** Implemented and verified by exact-source CI integration and
browser E2E in [44665400225](https://github.com/cyberducttape/StePanel/actions/runs/44665400225).
The product status summarizes stored evidence; it does not certify
installed-host recovery.

**Acceptance criteria**

- Each site's recovery response and workspace identify the confidence level
  and show separate archive, database, application, synthetic-transaction,
  and offsite-restore checks.
- Every passing check identifies its backup and evidence timestamp; measured
  rehearsal duration is shown with the exact successful recovery point.
- Archive-only validation never implies database or application recovery.
- An offsite upload never implies that an offsite restore succeeded; an
  offsite restore claim is tied to the selected backup identity.
- Built-in WordPress proof writes and reads a unique canary through the
  disposable restored database and removes it with the disposable database.
- Focused recovery/broker tests, race tests, `make check`, UI tests, and
  exact-source CI pass. Full installed-host recovery certification remains a
  separate release gate.

### M0r — Isolate recovery-proof execution and reject incompatible workers (P1)

**Status:** Implemented and verified by exact-source CI integration
[44665400225](https://github.com/cyberducttape/StePanel/actions/runs/44665400225).
The bounded recovery-proof environment, worker protocol compatibility checks,
and full-table login-limiter admission are covered by the integrated Go and
race suites. Typed executors for all site-operation routes are tracked under
M0s and are also complete.

**Acceptance criteria**

- Recovery proof children receive only an explicit environment allowlist,
  execute only under the unprivileged panel identity, and never persist
  arbitrary child output in job/rehearsal errors.
- A regression gives the parent known test secrets and verifies the child
  cannot read them; failure output is not retained.
- Worker heartbeats include a durable job-protocol version. Readiness and
  external-worker admission reject mismatched release/build/protocol values.
- Login limiter memory remains bounded and a full table does not reject a new
  identity; active recent buckets retain their limits and eviction is O(1).
- Focused tests, full repository checks, and exact-source CI pass.

### M0s — Remove HTTP-handler replay from durable site operations (P1)

**Status:** Implemented 2026-11-18; exact-source CI integration (including race
tests and the UI suite) passed on `8556e9fe` in run
[44665400225](https://github.com/cyberducttape/StePanel/actions/runs/44665400225).
All
ten site-operation routes have typed validators and executors. The release
pipeline, Git deploy, staging create/promote, and local/offsite
restore-to-staging handlers are now thin adapters over shared services that
take a `siteRequester` (HTTP request or durable principal). The worker no
longer synthesizes HTTP requests, and the replay fallback, its response
translation, and the replayed-identity context that `Auth` trusted are
removed. Malformed queued bodies are now refused at admission (422).

**Acceptance criteria**

- All site-operation route entries have typed executors; HTTP handlers only
  parse/authenticate/authorize and enqueue, while workers invoke domain
  services directly.
- Webhook deployments retain their signed-site binding and revalidate current
  webhook configuration before execution.
- API-token ownership and scopes are revalidated at execution; session jobs
  use immutable actor/site identity and re-check current ownership.
- Regression tests cover enqueue, retry, cancellation, authorization revocation,
  and error serialization for the converted operations.

### M0q — Require independent signing key for production audit checkpoints (P1)

**Status:** Implemented; focused configuration and checkpoint tests and full
`make check` pass. Exact-source CI integration passed in run
[44665400225](https://github.com/cyberducttape/StePanel/actions/runs/44665400225).

**Acceptance criteria**

- Production configuration with an audit-checkpoint target fails validation
  when `STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY` is absent.
- A dedicated key must satisfy existing size, character, and key-independence
  checks; valid production configuration passes this specific guard.
- Nonproduction fallback behavior remains covered for compatibility.
- Documentation distinguishes the nonproduction fallback from production
  requirements; full checks and exact-source CI pass.

### M0p — Detect altered audit history before appending (P1)

**Status:** Implemented. The latest code change detects a future-dated audit
verification checkpoint after a wall-clock rollback. Audit tests, the audit
race test, and full `make check` pass locally. Current-code-equivalent hosted
CI integration [44724016547](https://github.com/cyberducttape/StePanel/actions/runs/44724016547)
passed the full suite and race tests; its code tree matches current `main`.
Current-source CodeQL [44730175604](https://github.com/cyberducttape/StePanel/actions/runs/44730175604)
also passed.

**Acceptance criteria**

- Signed append-cache metadata binds log size and modification time; any
  mismatch forces verification of every event HMAC and chain link before the
  append proceeds.
- Full-chain validation repeats at least every 15 minutes during active writes;
  a future-dated checkpoint after a wall-clock rollback forces validation
  rather than postponing the scan.
- A same-size middle-record edit with changed file metadata is rejected on the
  next append. If an edit preserves both size and mtime, it is rejected by the
  next due full-chain scan (at most 15 minutes during active writes); the
  append leaves the altered log unchanged. Regression tests cover both cases.
- Existing signed state/cache formats safely fall back to full validation.
- Audit package tests, full repository checks, and exact-source CI pass. This
  does not replace independently administered off-host audit checkpoints.

### M0o — Bound customer file-manager directory enumeration (P1)

**Status:** Implemented; installed-host behavior passed on code-equivalent
source. Six-host run
[44729127788](https://github.com/cyberducttape/StePanel/actions/runs/44729127788)
created 10,000 entries, enforced the 5,001-record/2 MiB/10-second bounds, and
interposed symlink swaps before read, write, mkdir, rename, delete, and list on
Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9 with Apache and Caddy. All six
hosts passed; see the [reviewed report](docs/lab-results/2026-11-20-six-host-install-smoke-059ba72.md).
The non-Markdown source tree matched current `main` before the current
limiter-only change. `stepanel-filectl` streams entries as the account user,
and operations resolve beneath an opened document-root descriptor. A new
nonblocking limiter admits at most four file reads/listings per tenant and 32
per panel process; excess requests receive HTTP 429 with `Retry-After: 5`.
Focused tests cover per-tenant fairness, the host cap, slot release, and the
retry response. `make check` and focused race tests passed locally. Exact-source
CI integration [44741756748](https://github.com/cyberducttape/StePanel/actions/runs/44741756748)
passed the customer `ui-e2e` job and Go race tests. The exact-source
AlmaLinux 9/Caddy/MariaDB install smoke [44741756889](https://github.com/cyberducttape/StePanel/actions/runs/44741756889)
also passed. This does not establish hostile multi-tenant isolation.

**Acceptance criteria**

- A real installed-host directory with 10,000 entries returns at most 5,001
  helper records, a truncated result, and completes within 10 seconds.
- No full directory listing is materialized in a root-owned temporary file.
- Reads and mutations remain rooted at opened directory descriptors even
  when a tenant swaps a checked path component for a symlink.
- Concurrent reads/listings across sites belonging to one tenant share the
  same four-operation budget; other tenants retain independent admission, and
  aggregate panel work remains capped at 32 operations.
- Focused Go tests, ShellCheck, Bash syntax, adversarial installed-host smoke,
  and exact-source CI pass.

### M0n — Remove psql client-command execution from PostgreSQL restores (P0)

**Status:** Implemented and verified by current-code-equivalent CI integration
[44724016547](https://github.com/cyberducttape/StePanel/actions/runs/44724016547)
on source `6e80a189` (identical non-Markdown tree to current `main`). The real
PostgreSQL smoke restored a valid custom-format archive, confirmed concurrent
restores serialize shared loader-role grants so tenant B cannot `SET ROLE`
during tenant A's restore, and rejected plain SQL plus `\!`, `\copy`, and
`\o` without changing the live database. The MySQL 8 client-command smoke
passed as part of the same integration run. Earlier malformed fixtures with
two leading backslashes are not counted; the current smoke asserts literal
fixture bytes before execution. This validates the tested CI database versions
and does not replace host-level recovery certification.

**Acceptance criteria**

- `bash -n`, ShellCheck, Go tests, and static analysis pass on the change.
- The PostgreSQL restore smoke successfully restores a valid custom archive.
- The smoke rejects each psql meta-command fixture without executing it or
  changing the live database.
- A second restore process cannot assume another tenant's database-owner role
  while the first restore's membership is active.
- CI evidence is tied to the exact source commit and the corrected real-server
  fixtures pass. Existing plain-SQL PostgreSQL backup compatibility must
  remain explicitly documented.

### M0 — Restore green exact-source CI

**Priority:** Immediate release hygiene  
**Latest verified status:** CI integration
[45094688816](https://github.com/cyberducttape/StePanel/actions/runs/45094688816)
passed on source `64bb74a4`, including formatting, release and installer
contracts, vet, the full Go suite, coverage floors, race tests, recovery and
ENOSPC drills, database integration, deployment validation, and browser E2E.
Current `main` (`c4397643`) differs from that tested source only in
documentation. Documentation [45098230459](https://github.com/cyberducttape/StePanel/actions/runs/45098230459),
CodeQL [45098230480](https://github.com/cyberducttape/StePanel/actions/runs/45098230480),
and release evidence [45098464802](https://github.com/cyberducttape/StePanel/actions/runs/45098464802)
passed on `c4397643`. Production remains **NO-GO** until the separate
installed-host certification gates pass.

**Historical status:** Green on current code source `1e6db18` with documentation commit
`b2be13f`: CI integration passed on the identical code tree, and CodeQL,
documentation, release evidence, and the exact-code six-host install/recovery
matrix passed. The matrix is one iteration per OS/web-server pair; it is not a
recovery certification cohort.
CI integration covers Go tests and race tests, recovery
drills, tmpfs ENOSPC, database integration, static analysis, deployment
validation, and browser E2E. Earlier exact-source evidence on `711948d` is
retained in the prior progress record. The six-host full recovery shard
[`gate-1ff3233`](https://github.com/cyberducttape/StePanel/actions/runs/41066287752)
and the N-1 upgrade smoke
[41066237188](https://github.com/cyberducttape/StePanel/actions/runs/41066237188)
passed on `1ff3233`, which differs from `6ff12b6` only in documentation, an
alert expression, and a code comment. CI integration had failed from `05565dc`
until `7a85b2c` (overlapping CPU alerts), and the scheduled N-1 smoke from
`b6ce902` until `1ff3233` (installer preflight); see [PROGRESS.md](PROGRESS.md).
The earlier `c91a88b` evidence is recorded in
[`docs/lab-results/2026-10-30-current-source-almalinux-install-79e1de4.md`](docs/lab-results/2026-10-30-current-source-almalinux-install-79e1de4.md).
This closes M0 exact-source validation only; production recovery gates remain
open and are tracked in [`docs/V1_PRODUCTION_GATES.md`](docs/V1_PRODUCTION_GATES.md).

Current code source `1e6db185e5fc77e034280e9182d30e47c562c190` passed CI
integration [41187605734](https://github.com/cyberducttape/StePanel/actions/runs/41187605734)
and the six-host installed recovery matrix
[41189484718](https://github.com/cyberducttape/StePanel/actions/runs/41189484718).
Documentation [41189593476](https://github.com/cyberducttape/StePanel/actions/runs/41189593476)
and CodeQL [41189593384](https://github.com/cyberducttape/StePanel/actions/runs/41189593384)
passed on documentation commit `b2be13f`, whose non-Markdown tree matches the
tested code source. The release-evidence job also passed; a refreshed report
will be generated after this evidence update. Integration includes coverage
floors, the full Go suite and race tests, recovery drills, tmpfs ENOSPC,
database integration, deployment validation, and UI E2E. These software and
single-iteration host checks do not close repeated host-failure certification.

- [x] Correct the Playwright quota-headroom expectation to match the UI's
  account-scoped upper-bound wording; exact-source browser E2E passed in
  [41157044343](https://github.com/cyberducttape/StePanel/actions/runs/41157044343).
- [x] Keep stateful customer-panel fixtures unique across Playwright workers
  and repeat attempts; the exact-source UI E2E job passed in run 41157044343.

**Acceptance criteria**

- `node --check tests/e2e/critical-navigation.spec.js` passes.
- `CI=true ./scripts/e2e-ui-test.sh ./stepanel` passes from a freshly built
  binary.
- The stateful customer-panel spec passes twice concurrently with two workers,
  without account collisions.
- On the exact source commit, CI integration, CodeQL, documentation, and
  release-evidence checks complete successfully. Any failed job is diagnosed
  from its log/trace and fixed before starting another milestone.

### M0e — Bounded cPanel archive resource admission (P1)

**Status:** Implemented; focused regressions, `go vet ./...`, the full Go test
suite, and `make check` pass. Exact-source hosted CI remains pending.

**Acceptance criteria**

- cPanel inspection and extraction independently reject more than 50,000 tar
  headers, 64 MiB of retained path bookkeeping, or paths deeper than 128
  components.
- Admission reserves expanded payload bytes plus 4 KiB per unique filesystem
  object; malformed or overflowing estimates fail closed.
- Tests cover compressed empty-file floods, independent extraction limits,
  path depth, metadata limits, and capacity estimates.
- Focused and repository Go checks pass; host quota/inode certification is
  tracked separately and is not inferred from unit tests.

### M0l — Durable job fencing and independently verifiable audit evidence (P0)

**Status:** Durable per-claim job fencing and the signed checkpoint format are
implemented. Daily publication is now an opt-in durable job that persists the
checkpoint before remote I/O and verifies the remote copy. Independent
checkpoint signing keys and protected historical-key verification are now
supported. The exact source
`1e6db185e5fc77e034280e9182d30e47c562c190` passed CI integration
([41187605734](https://github.com/cyberducttape/StePanel/actions/runs/41187605734)),
CodeQL ([41187605838](https://github.com/cyberducttape/StePanel/actions/runs/41187605838)),
documentation ([41187605775](https://github.com/cyberducttape/StePanel/actions/runs/41187605775)),
and an installed-host smoke ([41187605802](https://github.com/cyberducttape/StePanel/actions/runs/41187605802)).
The smoke passed one AlmaLinux 9/Caddy/MariaDB recovery iteration and a
database-backed WordPress restore proof; details are in
[`2026-10-31-almalinux9-install-1e6db18.md`](docs/lab-results/2026-10-31-almalinux9-install-1e6db18.md).
It does not configure an audit-checkpoint remote or exercise remote
publication. The earlier installer smoke on `e8a1889` was canceled after a
newer source was pushed and is not counted.

Durable fencing prevents stale workers from committing SQLite job state, but
does not undo an external mutation performed after lease loss. The broker's
legacy site-creation journal also derives its journal ID from the site name
(`site-create-<site>`) rather than receiving the durable job ID. Treat this as
a known identity gap; it is not evidence that the other job workflows are
unsafe, but it prevents claiming a consistent operation-ID contract.

Operation-specific idempotency, explicit committing/reconciliation-required
states, prepare/apply/verify/recover journals at irreversible host boundaries,
and repeatable SIGKILL recovery evidence remain open. Before certification,
inventory every mutating durable job kind, broker action, and provider action
in a side-effect matrix. For each externally visible effect, record the
operation ID, idempotency/reconciliation rule, journal transition before and
after the effect, recovery owner, and injected failure points. On lease loss,
the successor must inspect actual host/provider state and either safely adopt,
compensate, or mark operator intervention; it must never infer that fencing
rolled an external effect back. Require tests that kill the worker immediately
before and after each effect and prove the final filesystem, database,
service/provider state, journal, and durable job agree.

Configure `STEPANEL_AUDIT_CHECKPOINT_TARGET` to send checkpoints to a distinct
rclone destination. Use a separately administered storage account and
append/read-only policy where the provider supports it. The implementation
reconciles ambiguous upload results by reading back the same immutable object,
checking its exact bytes, and verifying its signature. Configure
`STEPANEL_AUDIT_CHECKPOINT_SIGNING_KEY` separately from the audit-chain key and
retain retired checkpoint keys in the protected keyring before rotating. This
does not support rotation of the local audit-chain key. The capability is
optional; remote policy immutability, provider outage/recovery evidence, full
destructive-operation completeness review, and repeat host certification remain
open.

**Acceptance criteria**

- Crash/retry tests prove the durable job reuses the exact checkpoint bytes
  after an ambiguous remote result and never overwrites a different object.
- Remote read-back is size-bounded, byte-matched, signature-verified, and
  recorded as an audit outcome only after verification.
- Checkpoint-key rotation uses a protected historical verification keyring;
  tests prove old and current checkpoint signatures remain verifiable after
  rotation. A disposable remote-storage test must still prove provider append
  restrictions and retention behavior.
- Destructive operations require stable, operation-specific idempotency;
  distinct operations are never silently coalesced.
- Every mutating durable job, root-broker action, and provider action is
  inventoried with a durable operation ID and a documented idempotency or
  reconciliation rule for each external effect. No journal uses a site/resource
  name as a substitute for the operation ID.
- Each destructive workflow has a durable state machine and restart-safe
  journal, with operator-intervention state for ambiguous external effects.
- Crash drills kill workers immediately before and after every externally
  visible effect; the successor reconciles actual state and proves stale
  workers cannot commit or repeat unverified side effects.
- The installed-host failure matrix verifies filesystem, database,
  service/provider state, journal, and durable job convergence, with evidence
  retained at the exact release-candidate source.
- Exact-source CI, including race tests, coverage, release evidence, and
  installed-host validation, passes before this implementation is certified.

### M0u — Measure durable queue scale and contention (P2)

**Status:** Current-source local queue-depth and two-worker contention
measurements are recorded in
[`2026-10-31-queue-claim-benchmark-ec88c85.md`](docs/lab-results/2026-10-31-queue-claim-benchmark-ec88c85.md).
No query/index change is justified by those results. This is not a certified
capacity envelope.

**Acceptance criteria**

- Preserve `EXPLAIN QUERY PLAN` output for `ClaimNext` at 1k, 10k, and 100k
  queued/history rows, including owner FIFO and retry-delay scenarios.
- Record repeated p50/p95/p99 claim latency, WAL size/checkpoint behavior,
  SQLite busy/write errors, and queue throughput with one, two, and the
  supported maximum number of workers.
- Repeat under representative backup, restore, migration, and dashboard load
  on each supported host/filesystem profile. Include a sustained run and
  post-kill/restart queue reconciliation.
- Make no index/query optimization without benchmark evidence and regression
  tests proving owner ordering, lease fencing, retry scheduling, and atomic
  claims are unchanged.
- Publish the measured operating envelope and limitations with exact-source
  release evidence; do not infer site capacity from synthetic queue results.

### M0a — Bound long-running restore and API-token behavior (P1)

**Status:** Implemented and locally verified; exact-source CI, CodeQL,
documentation, and browser E2E passed on `79e1de4`. Installed-host smoke
`41019722358` passed one AlmaLinux 9/Caddy/MariaDB install and recovery-matrix
iteration; it is not a repeat cohort or power-loss certification.

Use operation-specific async deadlines that cover the corresponding helper
timeout and cleanup, renew durable leases throughout long work, and propagate
cancellation into offsite transfer. Bound API-token, account, and trusted-source
rate-limit state with LRU eviction, distinct initial bursts and sustained
rates, and no capacity-based rejection of otherwise-valid tokens.

**Acceptance criteria**

- Restore timeout classes exceed the full helper budget plus an explicit
  compensation reserve; offsite restore includes its configured transfer
  deadline.
- A restore test spans multiple short lease periods, completes without
  cancellation, and proves the durable lease was renewed. Canceled staging
  restore tests preserve the existing recovery/compensation contract.
- Limiter tests prove burst and refill behavior, account/source aggregation,
  bounded cache size, LRU eviction, and successful concurrent admission while
  full. Race-enabled targeted tests are clean.
- `make check` passes on the exact local source and the changelog/progress
  record the evidence and any remaining host-certification limits.

### M0b — Report effective security posture and extract typed operation paths (P1/P2)

**Status:** Security reporting and typed services for Node tooling and Python
deployment are implemented. Every site-operation route now has a typed
service (M0s).

Latest Python extraction verification: full `make check`, focused Python
authorization/deployment tests, and the focused race test pass. This closes
the local regression gate for this slice only; it does not close host
certification or the remaining typed-operation work.

Do not infer effective Apache authorization from isolated directive text.
Report managed configuration discovery separately from runtime enforcement.
For durable site operations, progressively move validation, authorization,
mutation locking, and host changes behind typed commands and shared services;
the queue worker should dispatch a typed command rather than synthesize an HTTP
request.

**Acceptance criteria**

- Security API and operator dashboard show `configuration_status` separately
  from `enforcement_status`; a present `Require ip` directive never produces
  a pass absent an effective runtime access test.
- Typed operations validate the same command schema at admission and worker
  execution, rechecks current ownership and token/role scope, and executes
  without HTTP request/response replay. Tests prove the worker does not call
  the route handler. Node tooling and Python deployment currently satisfy this
  criterion individually.
- Migrate each remaining route in focused changes, preserving audit, lock,
  validation, retry, and recovery contracts. Node tooling, Python deployment,
  Composer install, and sandboxed runner builds now use typed worker services.
  All registered routes have tested typed services and the fallback is
  removed (M0s).
- Installed Apache runtime tests from allowed and denied sources are required
  before reporting effective IP enforcement as verified.

### M0c — Exportable cPanel migration compatibility evidence (P1)

**Status:** Core preview/import report is implemented. Bounded source-certificate
metadata and durable rollback checkpoint reporting passed exact-source CI and
the seeded browser E2E in
[44665400225](https://github.com/cyberducttape/StePanel/actions/runs/44665400225),
including the downloadable compatibility report journey. Destination DNS
records are not queried by cPanel import, so the report explicitly marks DNS
comparison as unknown and requires operator review rather than claiming a
record diff.

The cpmove preflight reports bounded domain/document-root mappings, SQL dump
version evidence, Maildir structure, public source certificate metadata, and
explicit DNS/mail/TLS cutover work. An optional read-only SSH scan compares
source PHP extensions and host inventory. The import result records observed
post-migration checks and durable transaction checkpoints. The UI exports
both preview and final evidence as JSON and Markdown. DNS zone record contents
and destination TLS are not independently verified by the import workflow.

**Acceptance criteria**

- Archive tests cover parsed domain maps, unknown/major-mismatch database
  versions, complete/incomplete Maildir structure, and post-import failure
  states.
- Missing source/runtime data is reported as unknown or warning; PHP CLI,
  staged mail, and archive metadata are not represented as PHP-FPM, delivery,
  or DNS verification.
- Dashboard preview exposes domain mappings, database checks, mailbox
  validation, and a DNS cutover checklist; optional SSH analysis uses the
  existing administrator-only, read-only job workflow.
- Preview and completed import evidence download as valid JSON and readable
  Markdown, without including SSH credentials.
- Certificate reports expose only bounded public metadata and never PEM or
  private-key contents. Import reports preserve the recovery journal identity
  and the final rollback/compensation state, including failed imports.
- Focused Go tests, JavaScript syntax/unit/E2E checks, full `make check`, and
  exact-source CI pass. This milestone does not close installed-host recovery
  or production certification gates.

### M0d — Evidence-backed site incident diagnosis (P1)

**Status:** Implemented. Prior exact-source CI integration
([41026353462](https://github.com/cyberducttape/StePanel/actions/runs/41026353462)),
CodeQL ([41026353439](https://github.com/cyberducttape/StePanel/actions/runs/41026353439)),
documentation ([41026353453](https://github.com/cyberducttape/StePanel/actions/runs/41026353453)),
release evidence ([41028522602](https://github.com/cyberducttape/StePanel/actions/runs/41028522602)),
and browser E2E passed on `c91a88b`. The earlier hosted installed-host smoke
`41019722358` passed one AlmaLinux 9/Caddy/MariaDB recovery-matrix iteration
and measured a 7.309-second WordPress recovery proof on `79e1de4`; it is not
current-source host evidence. The repeated recovery gate remains open.
The diagnostics follow-up passes `make check`, focused Go tests, JavaScript
syntax checks, the focused disposable Chromium journey (1/1), and exact-source
hosted CI run [41033987875](https://github.com/cyberducttape/StePanel/actions/runs/41033987875)
(Go coverage, race, recovery/ENOSPC, database restore, container, and UI E2E).
CodeQL [41033987807](https://github.com/cyberducttape/StePanel/actions/runs/41033987807)
and documentation [41033988021](https://github.com/cyberducttape/StePanel/actions/runs/41033988021)
also passed. The exact-source installed-host smoke
[41033987854](https://github.com/cyberducttape/StePanel/actions/runs/41033987854)
is still running; production-host certification remains open.

The current diagnostics refinement distinguishes low free bytes from low free
inodes in filesystem evidence, and classifies database probe failures into
bounded causes such as connection refusal, authentication rejection, socket
unavailability, and timeout. Raw helper output remains hidden. Focused Go tests
and the modified browser journey pass locally, as do `make check` and the
focused disposable Chromium journey. Hosted exact-source CI remains the next
verification step.

Prometheus alert descriptions now report their evaluated values and triggering
thresholds/windows, point to relevant job, readiness, database, storage, or
outbox evidence, and suggest a safe next action. Rule syntax and rendered
annotation tests pass with promtool. The site incident view combines PHP-FPM
worker-memory/saturation evidence, database probes, byte/inode pressure,
certificate renewal history, backup freshness, and resource enforcement. These
site incident checks include timestamped diagnostic snapshots; the separate
Prometheus background sampler now continuously exports per-site PHP-FPM and
resource-limit samples, and durable certificate renewal jobs have per-domain
alert series.
Prometheus now continuously samples free/total bytes and inodes for configured
site, backup, database, and control-plane storage roots. Low-capacity and failed
probe alerts include role, measured ratio or probe result, threshold, and a safe
next action. Host paths are not emitted as labels. A bounded background sampler
also exports PHP-FPM pool counters and per-site systemd cgroup memory/task
usage; it reports sample freshness and unavailable probes without blocking
scrapes. CPU quota utilization is derived from interval deltas of systemd's
cumulative CPU-time counter and the enforced quota. Durable certificate
issuance status is indexed by domain for alerts.

Correlate PHP-FPM saturation and historical pressure, systemd memory/task
limits, host byte/inode capacity, database-engine connectivity, TLS metadata
and failed certificate issuance jobs, scheduled-backup failures/staleness,
recent errors, and recovery evidence. Each displayed finding must state its
observed evidence and a safe next action. Probes must report unavailable or
unverified where they cannot establish application-level behavior.

**Acceptance criteria**

- Site diagnostics report timestamped byte and inode measurements for the site,
  backup, and configured database filesystems, and explicitly identify which
  resource has low headroom or unavailable probes. A zero-sized filesystem
  measurement is not divided or presented as a percentage.
- Database connectivity is based on a completed restricted engine query and is
  clearly distinguished from a per-site WP-CLI check using the site's
  configured credentials; the latter runs as that site identity and is bounded
  and briefly cached. Failed engine probes include a bounded failure class and
  a safe diagnostic detail without exposing raw helper output.
- Failed certificate issuance jobs and enabled backup schedules with repeated
  failure or a recovery point older than twice the interval appear as findings
  with timestamps and measured evidence.
- A newly enabled schedule is not reported stale before its first due run; a
  schedule with no successful backup becomes stale only after its initial run
  has been attempted or its first due window has expired by the documented
  grace interval.
- Prometheus exposes per-site enabled-schedule success presence, consecutive
  failures, and latest-attempt age. Alerts identify the site and include the
  failing count or explicit missing-success evidence; enabled schedules that
  have never succeeded are alertable without mislabeling disabled schedules.
- The overview reports cgroup memory/task usage against enforced limits and
  CPU time against the enforced quota and preserves existing PHP-FPM saturation
  evidence. Prometheus samples these in the background, alerts on saturation,
  unavailable samples, and limit pressure, and does not perform privileged
  probes during a scrape. CPU rates are omitted after counter resets, stale or
  unavailable samples, and for units without a finite quota.
- Site-tree byte and inode usage at 90% or more of a configured account
  allowance is promoted to a warning; at or above the allowance it is
  critical. Evidence states that this is a tree measurement and an upper-bound
  headroom estimate for a potentially shared account quota.
- PHP-FPM saturation evidence includes active/max workers, queue depth, slow
  request counters, and the sample timestamp. Failed certificate alerts name
  the domain, durable job state, and job time; the authorized job history
  provides the associated helper error.
- Certificate job evidence is queried as the latest durable job per domain;
  activity on one domain cannot hide another domain's latest result. If the
  durable job store is unavailable, diagnostics report renewal history as
  unknown rather than implying a clean renewal state.
- Focused Go tests, JavaScript syntax/unit checks, and `make check` pass; each
  rendered alert includes a diagnosis, supporting evidence, and safe action.
- A browser E2E regression verifies the incident view renders measured storage
  pressure, per-site WordPress DB authentication failure, stale/failed backup
  evidence, and a failed certificate job; the seeded full UI suite passes.

### M0e — Continue typed durable operation extraction (P1)

**Status:** Implemented and verified on code-equivalent source `47386470` in
hosted integration [45313583538](https://github.com/cyberducttape/StePanel/actions/runs/45313583538).
Both integration jobs passed, including the Go suite/race tests and seeded
browser E2E. Current `main` has the same non-Markdown source tree; the later
commits only update evidence documentation.
All ten `site.operation` routes have typed executors, and the worker invokes
those services directly. The remaining routes were completed under M0s;
Node tooling, Python deployment, Composer install, sandboxed runner builds, and
Node deployment also use typed worker services. Node deployment revalidates
the queuing token, checkpoints recovery state, and refuses compensation after
lease loss. Static inspection confirms no worker synthesizes an HTTP request or
invokes a route handler. The focused executor/token/lease tests and full local
`make check` passed on `b633dbea`; hosted validation now passes on the
code-equivalent current source. Host-level recovery certification remains a
separate gate.

**Acceptance criteria**

- Every migrated route uses the same domain service for HTTP and worker entry,
  repeats current authorization at execution, and validates the typed command
  at admission and execution.
- Worker code does not synthesize HTTP requests for migrated routes; tests
  verify lock, audit, state-persistence, cancellation, and stale-token behavior
  remain correct. Node deployment checkpoints its previous files and runtime
  stage, revalidates the queuing API token before execution, and refuses
  compensation after losing its durable job/site fencing ownership.
- The handler-replay fallback is removed only once all registered routes have
  equivalent services and regression coverage.
- Focused Go tests and `make check` pass for each extraction; host-level
  certification remains a separate gate.

### M0f — Preserve session-store failure semantics (P1)

**Status:** Implemented and verified on exact source `6ff12b6`. CI integration
([41076494928](https://github.com/cyberducttape/StePanel/actions/runs/41076494928))
passed the complete Go test suite and UI E2E on that source; the commit contains
this fix and its regression tests.

**Acceptance criteria**

- A missing, expired, or revoked session remains an authentication failure;
  a database read failure is reported as service unavailable.
- Existing boolean session checks retain their compatibility behavior for
  non-HTTP callers, while request authentication uses the error-aware API.
- Registry and HTTP regression tests pass, including a closed-database case;
  `make check` and exact-source CI pass.

### M0g — Complete app activation compensation after cancellation (P1)

**Status:** Implemented and verified on exact source `6ff12b6`. CI integration
([41076494928](https://github.com/cyberducttape/StePanel/actions/runs/41076494928))
passed the complete Go test suite and UI E2E on that source; the commit contains
this fix and its cancellation/recovery regression test.

When app activation fails or is canceled after host mutation may have begun,
compensation uses a separate bounded context while preserving the mutation
fencing values. This lets the prior service generation be restored when its
lease is still valid; the broker refuses compensation if fencing has been
lost. Failed compensation leaves the durable activation journal for startup
recovery.

**Acceptance criteria**

- A canceled activation still attempts prior-runtime restoration under a
  bounded context, restores the prior current and rollback manifests, and
  removes its journal only after compensation succeeds.
- If the fencing lease has been lost or compensation fails, the journal
  remains available for deterministic recovery and the operation does not
  report successful rollback.
- Focused cancellation/recovery tests and `make check` pass. This is software
  regression evidence, not host-power-loss certification.

### M0h — Fail closed on missing authentication and reconcile audit outcomes (P1)

**Status:** Implemented; local full-suite and focused race verification passed.
The pinned Prometheus 3.5.0 `promtool` also reports all 44 alert rules valid
and passes `observability/alerts_test.yml` on current source. Exact-source
hosted integration on code-equivalent source `993a4079` is running in [run
45179420668](https://github.com/cyberducttape/StePanel/actions/runs/45179420668);
the current `main` differs only in documentation. Keep this item open until
the run completes.

Production authentication is validated from trusted startup configuration
before route construction. Disabled authentication requires an explicit
development environment and a loopback listener. Security-audit initiation is
stored with a durable operation ID; outcome state and its terminal outbox event
commit atomically. Failed outcome persistence leaves an actionable pending
record and must never trigger mutation replay.

**Acceptance criteria**

- Startup auth tests reject production without credentials and reject
  auth-disabled non-loopback or non-development configurations.
- Audit failure-injection tests prove terminal-write errors are returned,
  completed mutations are not replayed, pending operation state survives, and
  evidence-based administrator reconciliation closes it without mutation.
- Operational health and Prometheus expose pending outcomes; OpenAPI documents
  the admin API and rendered alert tests pass in CI.
- `make check`, focused race tests, exact-source CI, and CodeQL pass. This does
  not close the independent security assessment or host certification gates.

### M0i — Require tenant capabilities at file access and contain package code (P1)

**Status:** Partially implemented. File-store capability checks and
package-command cgroup routing are in place. Site-operation, Node-deployment,
backup creation and restore, backup rehearsal, cPanel import, WPress restore,
and site-termination jobs now persist the tenant that owned the site at
admission and recheck that binding before execution; mutation paths repeat the
check after acquiring the site lock. Legacy customer job payloads without the
binding fail closed. The durable
job protocol version is advanced to reject older workers. Regression coverage
includes site reassignment during queued backup restore, termination, cPanel
import, WPress restore, and site creation, plus crash recovery after
termination removes ownership. `make check`, focused crash and reassignment
tests, and documentation link/reference checks pass locally. Exact-source
AlmaLinux 9/Caddy/MariaDB smoke [44686046786](https://github.com/cyberducttape/StePanel/actions/runs/44686046786)
passed the probe: a detached child remained in the transient package unit and
site slice, respected task/runtime/memory limits, and stopped with the unit.
The six-host systemd-container run
[44729127788](https://github.com/cyberducttape/StePanel/actions/runs/44729127788)
passed the detached-child containment probe on all six supported
OS/web-server combinations on code-equivalent source; see the [reviewed
result](docs/lab-results/2026-11-20-six-host-install-smoke-059ba72.md). This
does not establish behavior on dedicated production hosts or suitability for
mutually hostile tenants. The root broker independently checks customer file
requests against the durable site-to-tenant table before resolving the placed
Unix account or invoking the privileged file helper.

The site file-store API now accepts a `SiteAccess` capability rather than a
site-name string. The customer route passes the capability returned by the
ownership resolver through reads, uploads, and mutations. Customer-controlled
Node lifecycle/build commands, Composer operations, and Python package build
backends now execute in transient systemd services within the site's resource
slice, with cgroup-wide kill mode, a task ceiling, and a 15-minute runtime
bound. Rootless container builds retain their existing transient-unit and
container limits. This does not make shared-kernel hosting safe for mutually
hostile tenants.

An installed-host probe is now wired into `deploy/lab/install-smoke.sh`. It
runs the real Composer entry point with a harmless fake executable that starts
a detached session, verifies the child shares the transient unit's cgroup and
site resource slice, checks effective task/runtime/memory limits, stops the
unit, and verifies the descendant exits. Local syntax, ShellCheck, and shell
contract checks pass. The initial attempt failed at Composer lookup because
the smoke inherited a restricted `PATH`; after setting it explicitly, the
source-specific installed-host smoke
[44686046786](https://github.com/cyberducttape/StePanel/actions/runs/44686046786)
passed on AlmaLinux 9/Caddy/MariaDB at `e1195ccc`. Current `main` has later
production file-manager changes, so this is historical evidence for that
source. Other combinations are in flight on `f35e4b71` and `a7594a45`; their
runtime code matches the current tree, but they are not exact-source evidence
and are not counted until all jobs and artifacts are reviewed.

**Acceptance criteria**

- Store methods cannot be called with raw site-name strings; missing or empty
  authorization capabilities fail closed, and cross-tenant route tests remain
  denied before storage access.
- The root broker rejects missing or mismatched tenant claims for file
  operations using its own durable `tenant_sites` read, before helper
  execution; a regression proves a cross-tenant request cannot reach
  `stepanel-filectl`. The broker startup schema contract requires this table.
- Durable jobs persist immutable tenant and site identifiers, and execution
  revalidates that the actor remains authorized for that same tenant/site pair.
- Every package/build command that can execute customer-provided scripts uses
  the cgroup-bound runner; shell contract tests guard the unit properties and
  all call sites.
- Helper cancellation gives the wrapper a bounded SIGTERM window to stop its
  transient unit before SIGKILL escalation; tests prove the signal handler ran
  and the ordinary process-group descendants were reaped.
- On each supported installed OS, a deliberately detached descendant remains
  in the transient unit cgroup, is bounded by task/time/resource limits, and
  disappears when the unit is stopped or its runtime limit expires.
- Shell contract tests, focused Go tests, race tests, and `make check` pass.
  Host systemd behavior must be recorded as installed-host evidence.

### M0j — Close authentication, audit-key, and import-admission findings (P1)

**Status:** Partially implemented locally. Database-backed TOTP first-use
handling, independent audit-key selection, and direct cPanel-copy admission
are fixed with focused regressions. A customer-token contract test now checks
role downgrade, suspension, and revocation. Historical audit key IDs/keyring
and token reissuance evidence remain open.

The persisted TOTP path now treats `sql.ErrNoRows` as a fresh counter, rejects
invalid negative persisted counters without unsigned conversion, and commits
the accepted counter in the same transaction. Direct cPanel restore copies
apply `Config.MaxUpload` even when the supplied multipart header understates
the actual stream, check cancellation during reads, and reserve import-volume
capacity progressively while bytes are written. Audit signing no longer
falls back to the session secret. The existing fixed legacy-token hard cutoff
remains in effect; it must not be extended without a compatibility review.

**Acceptance criteria**

- First persisted TOTP use succeeds; duplicate use is refused across auth
  instances/restarts; concurrent first use accepts at most once; a newer
  adjacent-window code is accepted; corrupt negative counters fail closed.
- Direct cPanel restore rejects an oversized declared or actual stream,
  cancels promptly, removes partial staging files, and releases its capacity
  reservation on every return path.
- Audit signing requires a dedicated key. A later rotation implementation
  defines key IDs, a protected historical verifier keyring, backward reading
  for existing records, and a tested key-retirement procedure before rotation
  is advertised.
- Legacy unscoped tokens continue only until the documented host-wide cutoff;
  tests cover missing policy, pre/post-cutoff behavior, member role downgrade,
  suspension, revocation, and scoped-token least privilege. Token rotation and
  replacement must also be covered before removing the compatibility path.
- Focused tests, race tests, `make check`, and exact-source hosted CI pass.

### M0k — Fence every durable job claim through commit (P0)

**Status:** Complete on exact source `d5fe183` (2026-10-31). `make check`,
three repeated full-suite runs, audit/root-broker/operations race tests, the
full hosted race suite, and hosted CI/CodeQL passed. The startup-reconciliation
CAS refinement is included in that exact-source validation.

Each claim receives a fresh random lease token distinct from the diagnostic
worker ID. Progress updates, payload checkpoints, lease renewal, success and
failure transitions compare-and-swap on that token and require the lease to
remain unexpired at the SQLite update boundary. This prevents an expired or
replaced worker—including a retry using the same worker ID—from committing
durable job state. Keep the token out of API serialization.

**Acceptance criteria**

- A stale claim cannot commit after expiry, before or after reclamation by a
  worker reusing the same worker ID; current claim can still commit.
- Every worker-owned transition, including legacy completion helpers, uses the
  same token and unexpired-lease compare-and-swap.
- Existing databases migrate to a nullable claim-token column without losing
  queued or running job state. Recovered jobs receive a new token.
- `make check`, focused root-package race tests, and `go test -race` for
  `internal/rootbroker` and `internal/operations` pass.
- Exact-source CI and CodeQL pass before the milestone is closed.

### M1 — Complete installed-host failure recovery (P0)

**Status:** Open. Six-host installed smoke
[44729127788](https://github.com/cyberducttape/StePanel/actions/runs/44729127788)
passed one recovery iteration per Ubuntu 24.04, AlmaLinux 9, and Rocky Linux 9
Apache/Caddy combination on code-equivalent source, with WordPress HTTP proof
(4.588–6.866 s measured RTO). See the [reviewed result](docs/lab-results/2026-11-20-six-host-install-smoke-059ba72.md).
This is one systemd-container iteration per combination, not repeated or
physical-host certification. Current-code AlmaLinux 9/Caddy/MariaDB smoke
[44724016501](https://github.com/cyberducttape/StePanel/actions/runs/44724016501)
passed one additional installed-host recovery iteration and WordPress HTTP
proof (measured RTO 5.022 s).
On 2026-11-23, Rocky Linux 9 KVM drill [45150861029](https://github.com/cyberducttape/StePanel/actions/runs/45150861029)
passed six guest-loss operation scenarios plus database ENOSPC and alert
delivery/retry on source `edd11d47`; the detailed [reviewed report](docs/lab-results/2026-11-23-rocky-kvm-abrupt-loss-edd11d47.md)
links the retained artifact and records that it is one guest-loss pass, not
physical power-loss certification. The source-current-at-run-time KVM
follow-up [45158813904](https://github.com/cyberducttape/StePanel/actions/runs/45158813904)
passed; see the [reviewed report](docs/lab-results/2026-11-23-rocky-kvm-abrupt-loss-4e9ca58.md).
Product source `5da789bb` later changed setup, installer, and quota-preflight
paths, so current-main KVM run
[45305278135](https://github.com/cyberducttape/StePanel/actions/runs/45305278135)
is underway. The release gate remains open pending current-source results,
repeated recovery iterations, physical-host/power-loss coverage, and remaining
failure classes.
**Current evidence update (2026-11-21):** The cache-disabled KVM abrupt-loss
run [44735170833](https://github.com/cyberducttape/StePanel/actions/runs/44735170833)
passed all five operation scenarios once on source `c3e37ebc`; this is guest
loss with QEMU cache bypass, not physical-host power loss. The six-host repeat
cohort [44725816245](https://github.com/cyberducttape/StePanel/actions/runs/44725816245)
was canceled on stale source `f464f79d` after nine full six-host iterations;
the tenth was interrupted, so it contributes no current-source repeats.
Install smoke [44897989372](https://github.com/cyberducttape/StePanel/actions/runs/44897989372)
passed its normal recovery matrix but failed the optional identity-repair
checkpoint drill. Artifact review traced this to the drill killing a wrapper
shell while the helper child could continue; the harness now targets the
helper PID and waits for the pre-commit snapshot. A fresh installed-host
smoke must pass before the corrected drill is accepted. Physical power loss,
operation-specific ENOSPC, and the repeated current-source matrix remain open.
Current-source KVM abrupt-loss operation drills
[44731756198](https://github.com/cyberducttape/StePanel/actions/runs/44731756198)
completed one pass on `94b90f5e` for file restore, database restore, Git
deployment, retention, and control-plane writes; all five passed. A QEMU guest
kill is not physical host power-loss evidence, and that run used QEMU
`writeback`, so the host page cache can preserve acknowledged guest writes.
The abrupt-loss workflow now defaults future runs to `cache=none` to remove
that cushion; this still does not simulate loss of the physical host or storage
controller. The
follow-up run [44735170833](https://github.com/cyberducttape/StePanel/actions/runs/44735170833)
on `c3e37ebc` with `cache=none` passed the same five operation scenarios once;
see the [reviewed report](docs/lab-results/2026-11-20-kvm-abrupt-loss-operations-cache-none-c3e37eb.md). It is one pass, not repeated or
physical power-loss evidence. The
earlier AlmaLinux 9/Caddy/MariaDB
installed smoke
and single-iteration recovery run
[44686046786](https://github.com/cyberducttape/StePanel/actions/runs/44686046786)
passed on `e1195ccc`; current `main` has later non-Markdown changes, so this is
not current-source evidence. It passed the installed cgroup descendant-containment
probe, control-plane database-outage check, file-manager and PHP-FPM checks,
cPanel and database-client-command restore checks, broker interruption, app
lifecycle, worker interruption during cPanel import/backup/restore/database
restore/termination, account suspension, and deployment checks. The
database-backed WordPress recovery proof passed with measured RTO 5.273
seconds. This is one iteration on one host combination and does not satisfy
the repeated cohort or broader host-failure matrix. Older one-host run
[44679148230](https://github.com/cyberducttape/StePanel/actions/runs/44679148230)
on `9f8c75ac` predates the cgroup probe. The six-host 20-repeat cohort
[44662607070](https://github.com/cyberducttape/StePanel/actions/runs/44662607070)
was cancelled after one six-host repetition because it used older source
`64281ca`. The replacement cohort
[44685011516](https://github.com/cyberducttape/StePanel/actions/runs/44685011516)
was cancelled after the shared install smoke reproduced the Composer-stub
`PATH` defect; no partial cohort is counted. The first exact-source capacity
attempt [44684499265](https://github.com/cyberducttape/StePanel/actions/runs/44684499265)
also failed during install smoke before the capacity workload began. The Rocky Linux 9 capacity rerun
[44674647682](https://github.com/cyberducttape/StePanel/actions/runs/44674647682)
completed on diagnostic source `8382e9c4` but had 210 site-request timeouts;
the reviewed result is linked under M4.

The older-source full matrix [44701903618](https://github.com/cyberducttape/StePanel/actions/runs/44701903618),
cohort [44702646288](https://github.com/cyberducttape/StePanel/actions/runs/44702646288),
and 20-repeat cohort [44686066552](https://github.com/cyberducttape/StePanel/actions/runs/44686066552)
were cancelled because their source trees do not match current `main`; one
completed repetition from 44686066552 is historical only. The six-host cohort
[44725816245](https://github.com/cyberducttape/StePanel/actions/runs/44725816245)
was dispatched while `f464f79d6287741eefd14174caaac35cd9f18534` was `main` and
was canceled after nine complete six-host iterations; iteration ten was
interrupted. Main had changed product code,
including PHP-FPM descriptor and status-probe behavior; this cohort is stale
and none of its repetitions count for current-source certification. The
current-code single-host smoke
[44724016501](https://github.com/cyberducttape/StePanel/actions/runs/44724016501)
completed successfully on source `6e80a189` with a database-backed WordPress
recovery proof (RTO 5.022 s); it predates current main and is one host/one
iteration only. No current-source repeated recovery pass is counted until the
cohort completes and its host artifacts are reviewed.

Earlier exact-source run
[44516615547](https://github.com/cyberducttape/StePanel/actions/runs/44516615547)
passed one full recovery-matrix iteration on each of the six Ubuntu 24.04,
AlmaLinux 9, and Rocky Linux 9 Apache/Caddy combinations, including database-
backed WordPress HTTP proof (4.756–7.106 s measured RTO). The per-host report
is [here](docs/lab-results/2026-11-19-six-host-recovery-64281ca.md). A new
20-repeat six-host cohort
[44662607070](https://github.com/cyberducttape/StePanel/actions/runs/44662607070)
was cancelled because it used stale source. Replacement cohort
[44685011516](https://github.com/cyberducttape/StePanel/actions/runs/44685011516)
was cancelled after the shared smoke failed before recovery scenarios; a new
exact-source cohort is pending the probe fix. The completed KVM guest-loss drill
[44662674624](https://github.com/cyberducttape/StePanel/actions/runs/44662674624)
passed one interruption each during file restore, database restore, Git
deployment, backup retention, and control-plane writes; install also passed 19
smoke/matrix checks. Its reviewed report is
[here](docs/lab-results/2026-11-20-kvm-abrupt-loss-operations-64281ca.md).
The guest used QEMU `writeback` caching, so host page cache can preserve guest
writes; this is not physical host power-loss evidence. One pass per scenario
does not satisfy the required 20–50 repetitions, and no database-restore,
archive-import, or backup-publication ENOSPC case was injected.
The prior exact-code scheduled six-host run
[41189484718](https://github.com/cyberducttape/StePanel/actions/runs/41189484718)
passed one installed recovery-matrix iteration on each Ubuntu 24.04,
AlmaLinux 9, and Rocky Linux 9 host with Apache and Caddy, including an
installed WordPress application proof (5.269–7.308 s measured RTO). See the
[reviewed six-host result](docs/lab-results/2026-10-31-six-host-install-matrix-1e6db18.md).
An earlier exact-source hosted AlmaLinux 9/Caddy/MariaDB run
`41019722358` on `79e1de4` passed one installed recovery-matrix iteration and
the WordPress application proof (7.309 s); see the reviewed
[result](docs/lab-results/2026-10-30-current-source-almalinux-install-79e1de4.md).
Exact-source run `41033987854` on `c155752` also passed one iteration and its
installed WordPress proof (7.440 s); see
[`2026-10-30-almalinux9-install-41033987854.md`](docs/lab-results/2026-10-30-almalinux9-install-41033987854.md).
One Rocky Linux 9 KVM iteration against older source
`1348611198dbb72b5a7099a86f33bf2de43e6012` completed with 123 smoke/matrix
passes; see the reviewed [run report](docs/lab-results/2026-10-30-rocky9-kvm-recovery-matrix-1348611.md).
It is not counted as current-source evidence. Neither run establishes physical
power-loss durability or meets the 20–50 repeat requirement.
On 2026-10-30 the six-host full recovery shard `gate-1ff3233`
([41066287752](https://github.com/cyberducttape/StePanel/actions/runs/41066287752))
passed one iteration on every supported OS/web-server pair. A 20-repetition
cohort was started on `6ff12b6`
([41155137017](https://github.com/cyberducttape/StePanel/actions/runs/41155137017));
until it completes with every host job passing, no repetition counts toward
this milestone.
**Source of truth:** Gate 5 and `docs/REAL_HOST_FAILURE_MATRIX.md`.

Run the installed native systemd stack on each supported OS/filesystem, and
exercise process/broker/worker interruption, database outage, physical or
uncushioned host power loss, and ENOSPC during backup publication, archive
import, file restore, and database restore. Repeat each required
operation/fault combination 20–50 times. Preserve the exact source SHA, guest
and host configuration, injection point, journal/job/database/filesystem
state, service health, and successful post-recovery invariants.

**Acceptance criteria**

- Every required matrix cell has repeatable results on the release candidate;
  no run is counted if the source or environment is not recorded.
- Each interrupted operation deterministically completes, rolls back, or
  presents a documented operator-intervention state; no ambiguous success,
  orphaned active stage, or unreconciled service/database/filesystem state.
- ENOSPC leaves the prior live site recoverable and does not damage unrelated
  sites; host loss is tested without relying on host page-cache survival.
- `docs/V1_PRODUCTION_GATES.md` links reviewed result artifacts for each
  closed gate. Missing hardware or runner capacity remains an explicit block,
  not a pass.

### M2 — Certify application recovery and supported runtime matrix (P0)

**Status:** Partial. The code-equivalent six-host run
[44729127788](https://github.com/cyberducttape/StePanel/actions/runs/44729127788)
passed database-backed WordPress HTTP recovery proof on all six Ubuntu 24.04,
AlmaLinux 9, and Rocky Linux 9 Apache/Caddy combinations, with measured RTOs
of 4.588–6.866 s. See the [reviewed result](docs/lab-results/2026-11-20-six-host-install-smoke-059ba72.md).
This is one pass per combination in disposable systemd containers, not
repeated or physical-host certification. Current-code AlmaLinux 9/Caddy/MariaDB run
[44724016501](https://github.com/cyberducttape/StePanel/actions/runs/44724016501)
passed one installed WordPress recovery proof with measured RTO 5.022 s.
Earlier six-host runs remain historical evidence. The earlier full six-host run
[`41189484718`](https://github.com/cyberducttape/StePanel/actions/runs/41189484718)
is retained as historical evidence. The one-host proof in run
[44686046786](https://github.com/cyberducttape/StePanel/actions/runs/44686046786)
is also on older source `e1195ccc` and does not establish current-source
recovery. The separate six-host 20-repeat cohort
[44725816245](https://github.com/cyberducttape/StePanel/actions/runs/44725816245)
remains active; count a repetition only after all six host artifacts are
reviewed. The KVM abrupt-loss workflow and capacity profile also remain open.

On a clean disposable destination, restore a real WordPress site including
files and a populated database, configure PHP-FPM and the virtual host, then
verify database consistency and a database-backed HTTP response. Run against
each runtime combination the project intends to support and retain evidence
for restore steps, HTTP result, measured recovery time, and excluded state.

**Acceptance criteria**

- Exact release-candidate source passes the published supported matrix on all
  listed OS/web-server/database combinations.
- Application-level proof is distinct from archive integrity and fails if
  database import, PHP execution, configuration, or HTTP probing fails.
- Unsupported combinations and single-run limitations are explicit in
  installation and release documentation.

### M3 — Prove operational alert delivery (P0)

**Status:** Implemented; live delivery/outage evidence remains open.

The alert outbox now has a regression that records an endpoint failure, closes
and reopens the control-plane SQLite database, checks that failure/attempt
state remains visible, and verifies the same event becomes retryable and can be
marked delivered. Normal and race-enabled focused runs and full `make check`
pass. This proves store-level restart persistence only; delivery through the
installed service against configured SMTP/webhook endpoints remains open.

Exercise configured webhook and SMTP endpoints on an installed host, including
endpoint outage, retry, restart, backlog recovery, redaction, and operator
visibility of undelivered/dead-letter events.

**Acceptance criteria**

- A real failed job/backup generates a durable alert and reaches each
  configured channel exactly as specified.
- A temporary endpoint outage produces visible pending/failure state and
  successful delivery after recovery without duplicate harmful actions.
- Evidence records endpoint type (secrets redacted), timestamps, retries, and
  alert state; operational runbooks identify who responds and how.

### M4 — Publish a measured capacity envelope (P0)

**Status:** Open; current figures are targets, not claims. The Rocky Linux 9
large profile [44674647682](https://github.com/cyberducttape/StePanel/actions/runs/44674647682)
seeded 500 sites and completed two heavy backups, but failed because 210 of
9,609 HTTPS site requests timed out and site-page p99 latency reached 7.7–10.0
seconds. API and job-list p99 remained below 23 ms; sampled host memory
availability stayed above 7.8 GiB, and no SQLite lock errors were recorded.
The reviewed diagnostic-source result is
[`here`](docs/lab-results/2026-11-20-capacity-large-8382e9c.md); it is not an
exact-current-source certification. The capacity probe now collects bounded
per-site PHP-FPM queue/saturation and site-cgroup CPU/memory samples from
`/metrics` during load. The earlier profile on source `e1195ccc`
[44686066350](https://github.com/cyberducttape/StePanel/actions/runs/44686066350)
completed with 184/9,791 site requests timing out at 15 seconds. FPM metrics
showed zero saturation and queue depth, but were available for only 473/504
sampled sites; aggregate CPU peaked at 99.9%, while the admin API and job list
remained responsive. The [reviewed diagnostic report](docs/lab-results/2026-11-20-capacity-large-e1195ccc.md)
records the limits and next measurements. This does not identify the root
cause or establish a certified limit; request-stage timing is required before
choosing a tuning correction. The first current-source attempt
[44684499265](https://github.com/cyberducttape/StePanel/actions/runs/44684499265)
failed in the shared installed-host Composer cgroup probe before the workload
started; the probe `PATH` issue has since been fixed.
The capacity failure must be diagnosed and a remediation measured before
publishing the envelope. The phase-instrumented 500-site profile
[44699996203](https://github.com/cyberducttape/StePanel/actions/runs/44699996203)
on `3acc333c` completed setup and workload collection but failed while
aggregating samples: the report helper expected four-field site samples when
admin samples have three fields. No JSON or Markdown report was produced, so
that run supplies no usable latency measurements. A regression test now covers
both sample shapes; rerun the full profile after this harness fix and review
the resulting phase timings before changing product defaults. This run was
not exact-source evidence because other non-Markdown files differ from current
`main`. The corrected 500-site profile
[44725885994](https://github.com/cyberducttape/StePanel/actions/runs/44725885994)
completed and failed on product source `f464f79d`. It recorded 196 first-byte
timeouts among 9,658 customer HTTPS requests (2.03%); both 512 MiB backups
completed, and the management API remained responsive. Periodic FPM samples
showed no saturation, but lacked data for up to 96 sites and were not
correlated with failures. The [reviewed report](docs/lab-results/2026-11-20-capacity-large-f464f79.md)
records the measured latency and resource envelope. It is a real failure at
the tested workload, not a certified limit or an identified root cause. The
probe now attaches the newest bounded per-site FPM/cgroup sample to retained
failures and reports errors independently for baseline and backup windows;
unit tests cover both contracts. Run a new exact-current-source KVM profile
and use its failure-correlated evidence to isolate the request-path bottleneck
before tuning defaults. The earlier profile
[44711012147](https://github.com/cyberducttape/StePanel/actions/runs/44711012147)
was cancelled before producing a report and contributes no measurement.

**Diagnosis (2026-11-21):** the timeouts were reproduced off-host and traced
to the panel's per-minute FPM status sweep. It woke every idle `ondemand`
pool, and the mass idle expiry that followed strands connections in php-fpm.
The probe now reports idle pools without waking them; see `PROGRESS.md`.
The residual php-fpm idle-expiry race (about 0.02% in reproduction) is kept
as a documented limitation (`docs/PERFORMANCE.md`). The before-fix baseline
[44848048837](https://github.com/cyberducttape/StePanel/actions/runs/44848048837)
failed 193/9,903 site requests (1.95%), matching earlier runs; see the
[reviewed report](docs/lab-results/2026-11-21-capacity-large-e19d737-before-fpm-fix.md). The post-fix 500-site KVM run
[44852768241](https://github.com/cyberducttape/StePanel/actions/runs/44852768241)
removed the slow-success tail (successful p99 9.3 s to 112 ms) but still
failed 271/11,810 requests (2.29%) in synchronized bursts that stall every
site at once; see the [reviewed report](docs/lab-results/2026-11-21-capacity-large-e19d737-after-fpm-fix.md).
That host-wide stall is a separate, still-unidentified cause. The probe now
records a per-5-second request timeline and a static-file (no PHP) probe to
locate it.

**Current capacity evidence (2026-11-21):** the static probe and descriptor
diagnostics isolated the PHP-FPM master `LimitNOFILE=1024` ceiling; the
installer now raises it to 65,536 and applies the drop-in to existing hosts.
The profile [44886417491](https://github.com/cyberducttape/StePanel/actions/runs/44886417491)
recorded 81,362 page requests with zero page failures but failed its initial
readiness check while offsite probing was still in progress. The harness now
waits for readiness before measuring. Confirming profile
[44897996332](https://github.com/cyberducttape/StePanel/actions/runs/44897996332)
passed with 70,715 requests, zero errors, and p99 126.9 ms baseline / 132.9 ms
during backups. Both used the same KVM runner type. This supports repeatable
behavior on that profile but does not establish a site-count limit or memory
ceiling; repeated runs across host/storage profiles are still required.

Benchmark a named supported host with representative PHP traffic, databases,
backups, migrations, and control-plane reads/writes. Measure CPU/RAM, disk and
inode pressure, SQLite/WAL growth and lock waits, queue latency, job backlog,
and customer HTTP latency. Include restart and recovery behavior at the target
load.

**Acceptance criteria**

- Each published site/domain/account/concurrency limit is backed by a
  reproducible sustained run on a supported OS/filesystem/storage profile.
- Report p50/p95/p99 API and job latency, resource ceilings, SQLite metrics,
  workload, duration, failures, and recovery results.
- Remove or clearly label any target that lacks evidence; do not extrapolate
  from synthetic session/API-only benchmarks.

### M5 — Certify upgrade, rollback, and security lifecycle (P0)

**Status:** Partial. A historical N-1 run on source `3f2580b`
[44663143624](https://github.com/cyberducttape/StePanel/actions/runs/44663143624)
passed 6/6 v0.6.0/v0.7.0 upgrade jobs on 2026-11-20 (systemd containers and
Ubuntu 24.04 QEMU guests); reviewed details are in
[the run report](docs/lab-results/2026-11-20-upgrade-matrix-3f2580b.md).
That run was code-equivalent to `8556e9fe` at the time, but current `main`
has 50 non-Markdown files changed since source `3f2580b`; it is not evidence
for the current installer, migrations, or rollback code. The injected
broken-candidate path explicitly reports that installer rollback alone did
not complete host recovery; the tested success uses the documented operator
downgrade procedure. This remains historical upgrade evidence, not current
release-candidate certification. Current-code N-1 matrix
[44724039602](https://github.com/cyberducttape/StePanel/actions/runs/44724039602)
was dispatched on `6e80a189`, which is code-equivalent to current `main`.
It passed all six jobs: v0.6.0 and v0.7.0 upgrade cases in disposable systemd
containers and Ubuntu 24.04 QEMU guests, including verification-interruption
cases. This is one run on a limited platform set; populated installations,
other supported distributions, and additional failure boundaries still need
release-candidate evidence. The earlier run
[44712444760](https://github.com/cyberducttape/StePanel/actions/runs/44712444760)
was stopped after an apt-mirror failure before the upgrade scenario.

The API lifecycle policy is now published in
[`docs/API_COMPATIBILITY.md`](docs/API_COMPATIBILITY.md). Candidate validation
against generated SDKs and a supported previous API major remains part of the
acceptance criteria below.

Test clean install, supported N-1 upgrade, interruption at schema and service
activation boundaries, installer rollback, snapshot-based downgrade, and
populated installations on every supported platform. Complete independent
security review and verify the documented disclosure, patch, signing, and
credential-rotation procedures.

**Acceptance criteria**

- Every advertised source-to-target upgrade path passes on the release
  candidate, with schema compatibility, outstanding jobs, secrets, and
  rollback/recovery evidence recorded.
- Independent security findings are triaged and blockers resolved; release
  artifacts have verifiable signing provenance.
- Support matrix, API compatibility policy, security support window, and
  operator runbooks are published and tested against the release candidate.

### M6 — Reconcile release evidence and approve or reject 1.0

**Status:** Repeated each release candidate.

**Acceptance criteria**

- Exact-source `release-evidence` report has no stale or non-equivalent CI
  results and includes all reviewed host evidence.
- Every P0 row in `docs/V1_PRODUCTION_GATES.md` is closed with direct evidence;
  the overall decision is explicitly changed to GO by review.
- If any criterion remains unmet, preserve NO-GO and publish the limitations.

### M7 — Competitive migration and WordPress lifecycle (post-1.0 product work)

**Status:** Planned. These capabilities do not replace or defer M1–M6 and are
not 1.0 release blockers unless called out by an existing production gate.
Scope and ordering are defined in [`docs/ROADMAP.md`](docs/ROADMAP.md).

**Acceptance criteria**

- A cPanel migration report gives every inspected component one explicit
  disposition (imported, transformed, skipped, unsupported, or failed), a
  reason, and evidence. It identifies the source archive digest and product
  build; unknown or assumed values are never presented as verified.
- A completed migration records target-side filesystem/database checks and an
  application HTTP probe. Mail, DNS, TLS, cron, and unsupported configuration
  changes remain visible as incomplete cutover work when they were not
  migrated or verified.
- The documented WordPress lifecycle succeeds on every supported runtime
  combination: create and secure, back up locally and offsite, restore to a
  clean host, verify database-backed HTTPS behavior, clone to staging, and
  promote with database-separation checks and a recoverable rollback point.
- Each claim is backed by repeatable installed-host evidence tied to the exact
  source. Archive integrity alone does not satisfy application recovery.

### M8 — Hosting-panel parity and platform expansion (2.0+)

**Status:** Planned, intentionally outside the single-host 1.0 contract.

**Acceptance criteria**

- Complete the customer file-manager workflow with tenant authorization,
  descriptor-rooted filesystem access, bounded transfers, audit events, and
  installed-host adversarial race and quota evidence.
- DNS provider adapters support zone inventory/import/export, provider-valid
  record reconciliation, and DNSSEC lifecycle with preview, read-after-write
  verification, and recovery instructions.
- Record an ADR deciding first-party versus externally integrated mail before
  offering mailbox creation, migration, delivery, or recovery as supported
  customer workflows.
- Billing/reseller roles, WebAuthn/OIDC, PITR, multi-host operation, and
  stronger tenant isolation each have separate threat models, migration plans,
  and acceptance evidence. Hostile-tenant support requires an isolated VM or
  equivalently strong boundary; it cannot be inferred from Unix users/cgroups.

## Current sequencing

1. Make first-run and customer operation dependable through clear errors,
   context-safe workflows, and resilient status updates (M0aa/M0ab/M0ac/M0ad/M0ae/M0af).
2. Typed service extraction for durable site operations is complete (M0s);
   move the backup/restore and deployment services into `internal/` packages
   in focused changes.
3. Obtain an accessible disposable KVM certification host/runner and execute
   M1 before broader production claims.
4. Complete M2 and M3 with real installed services while retaining failure
   evidence.
5. Measure M4 and complete M5; then perform the M6 exact-source audit.

### M0aa — Keep durable site-operation status truthful through transient outages (P1)

**Status:** Implemented and verified on exact source `d0bc1f17`. CI
[44941126573](https://github.com/cyberducttape/StePanel/actions/runs/44941126573),
CodeQL, and documentation checks passed.

Customer and operator site creation and workspace operations must treat an
accepted durable job as accepted even when status reads temporarily fail. Use
the shared job waiter with durable polling/retries, retain the job ID when
status cannot be confirmed, and report inventory-refresh failures separately
from creation failures.

**Acceptance criteria**

- A transient job-status read failure followed by recovery still reaches the
  correct terminal state without asking the operator to resubmit the mutation.
- Persistent status failure states that the job was accepted and identifies
  its ID; it never claims site creation failed or that nothing was changed.
- A successful create followed by an inventory refresh failure is reported
  as created with a refresh problem, not as a failed create.
- Builds, deployments, dependency installs, and staging operations use the
  same bounded waiter rather than an unbounded custom polling loop.
- Frontend unit/browser checks and exact-source CI pass.

### M0ab — Make operation history understandable without raw API responses (P2)

**Status:** Implemented and verified on exact source `d69e6c4e`. The focused
and full local browser suites passed; hosted CI
[44943145761](https://github.com/cyberducttape/StePanel/actions/runs/44943145761),
CodeQL [44943145904](https://github.com/cyberducttape/StePanel/actions/runs/44943145904),
and documentation checks [44943145744](https://github.com/cyberducttape/StePanel/actions/runs/44943145744)
passed.

The Operations drawer and dashboard activity feed should explain what happened
in the interface. They must not send operators to a raw JSON endpoint for
routine job details. Activity should lead to the matching job in the drawer,
where its identifier, timestamps, retry count, and failure explanation are
readable and keyboard accessible.

**Acceptance criteria**

- Job details render from authorized job summaries using text-safe DOM APIs.
- Dashboard activity opens the corresponding drawer entry and disclosure.
- Routine detail navigation does not open raw JSON.
- An axe/browser regression covers a failed job in the light theme; unit tests,
  frontend syntax, and exact-source CI pass.

### M0ac — Keep the dashboard focused on operator actions (P2)

**Status:** Verified on code-identical source `55c1421a`. Integration CI
[45017173779](https://github.com/cyberducttape/StePanel/actions/runs/45017173779)
passed the full Go, race, recovery, and browser suites; Documentation
[45019886848](https://github.com/cyberducttape/StePanel/actions/runs/45019886848)
and CodeQL [45019886865](https://github.com/cyberducttape/StePanel/actions/runs/45019886865)
passed on source `e78916ed`.

Remove raw API JSON links from the normal dashboard and ensure server-rendered
activity rows do not point at job JSON. Keep the API available through its
documented contract and CLI. Give operators a direct action to open the
readable Operations drawer.

**Acceptance criteria**

- Routine dashboard sections contain no “View JSON” shortcuts.
- Server-rendered activity rows are readable and do not navigate to API
  responses before JavaScript initializes.
- “Open operations” opens the durable job drawer; the customer activity view
  remains readable and accessible.
- Build, unit tests, focused operator/customer browser checks, and
  exact-source CI pass.

### M0ad — Explain database health in the dashboard (P2)

**Status:** Verified on code-identical source `55c1421a`. Integration CI
[45017173779](https://github.com/cyberducttape/StePanel/actions/runs/45017173779)
passed, including the seeded Chromium suite. Documentation checks passed on
the preceding source in
[45019886848](https://github.com/cyberducttape/StePanel/actions/runs/45019886848).

Replace the database diagnostics JSON link with a collapsed, on-demand,
read-only health snapshot. Show connection counts, blocked sessions, long
transactions, deadlocks, database size, safe helper failure explanations, and
the snapshot time. State clearly that these counts are host-wide. Keep missing
measurements unknown, and let the operator retry after an unavailable result.

**Acceptance criteria**

- Opening the disclosure runs the query; the dashboard does not poll it on
  every page load.
- Measurements, contention warnings, scope, and collection time are readable
  without exposing a raw API response.
- A failed probe is shown as unknown with safe remediation detail, and a
  manual retry can update the result.
- Browser accessibility and regression checks, build, unit tests, docs checks,
  and exact-source CI pass.

### M0ae — Keep customer domain onboarding evidence honest (P2)

**Status:** Verified on code-identical source `55c1421a`. Seeded Chromium
journeys and full integration CI passed in
[45017173779](https://github.com/cyberducttape/StePanel/actions/runs/45017173779);
documentation checks passed on the preceding source in
[45019886848](https://github.com/cyberducttape/StePanel/actions/runs/45019886848).

A configured web-server route does not prove that public DNS resolves or that
HTTPS works. The customer checklist must keep domain verification unresolved
until the operator has reviewed DNS and TLS evidence, and its next action must
open that website's domain workspace. A missing route remains a setup action;
missing or unavailable route data remains unknown.

**Acceptance criteria**

- A site with no route is directed to its Domains tab to connect one.
- Sites with configured routes are directed to the Domains tab to review DNS
  and HTTPS; the checklist does not count route configuration as verified.
- Missing route data is shown as unknown rather than as an absent route.
- The customer browser regression covers all three states and passes axe;
  exact-source CI passes.

### M0af — Require backup evidence for each active site (P1)

**Status:** Verified on code-identical source `55c1421a`. Operator and
customer coverage journeys passed in the seeded Chromium suite, and full
integration CI passed in
[45017173779](https://github.com/cyberducttape/StePanel/actions/runs/45017173779).
Documentation checks passed on the preceding source in
[45019886848](https://github.com/cyberducttape/StePanel/actions/runs/45019886848).

The first-run checklist must not imply account-wide recovery coverage when
only one of several sites has a verified backup. Compare the latest bounded
backup listing against every active assigned site. Link directly to an
uncovered site's Backups tab; if the listing may be truncated, report unknown
and ask the operator to inspect that site's complete history. Application
restore proof remains a distinct, stronger claim.

**Acceptance criteria**

- A missing backup for any active site keeps the recovery checklist step open
  and identifies that site.
- The primary action opens the first uncovered site's Backups tab.
- A full 100-result listing with no backup for a site reports unknown rather
  than claiming no backup exists.
- Only when every active site has a recorded integrity-verified archive may
  the checklist count this step complete; its text still distinguishes archive
  integrity from application recovery.
- Operator and customer browser regressions cover single-site coverage,
  multi-site gaps, truncation, and exact navigation.

### M0ag — Reduce first-site setup data entry (P2)

**Status:** Site names and WordPress titles are suggested from the domain;
the WordPress creation form now shows DNS, IPv6, HTTP/HTTPS, and password
prerequisites before submission in both operator and customer panels. The
first-run call to action now selects WordPress and opens those fields, while
the ordinary creation form retains its blank-PHP default. Operator/customer
browser journeys cover both behavior and guidance. The operator checklist now
also separates archive integrity from application-level restore proof for the
first active site and links to its rehearsal action. The browser journey now
covers missing, unavailable, archive-only, failed, and matching application-
level proof states. The complete seeded Chromium suite passed 29 tests with
one optional skip, and full integration CI passed on code-identical source
`55c1421a` in
[45017173779](https://github.com/cyberducttape/StePanel/actions/runs/45017173779).
Documentation checks passed on the preceding source in
[45019886848](https://github.com/cyberducttape/StePanel/actions/runs/45019886848).

Make common first-site setup fields easy to fill without taking control away
from the operator. Suggestions must be valid for their destination, update as
the domain changes, and stop changing after the user customizes them.

**Acceptance criteria**

- A valid domain suggests a site identifier; `www` is ignored and invalid or
  overlong labels are not submitted as identifiers.
- The domain field appears before the suggested site-name field in both the
  operator and customer forms, so users supply the source value before
  reviewing its generated name.
- WordPress titles receive a readable domain-derived suggestion.
- Users can edit either suggestion, and subsequent domain changes preserve
  their custom value.
- Generated WordPress administrator passwords can be copied with clear
  success/fallback feedback, without logging or persisting the secret.
- WordPress creation explains that the domain A record must point to this host,
  AAAA should be published only for working public IPv6, and ports 80/443 must
  be reachable; it must clearly say the form cannot verify those public facts.
- The confirmation repeats the prerequisites, and operator/customer browser
  journeys assert the guidance before site creation.
- When an account has no sites, the first-run action selects WordPress and
  reveals its setup fields before navigation; outside the first-run action,
  blank PHP remains the normal form default.
- The operator first-run checklist treats the latest backup's
  application-level restore proof as a distinct step. Archive-only proof,
  stale proof for another backup, failed rehearsal, and unavailable status
  must not be shown as application recovery success.
- Operator and customer journeys cover both behaviors and remain accessible.
- Build, frontend unit tests, focused browser tests, and exact-source CI pass.

### M0ah — Make generated installer settings directly usable (P1)

**Status:** Complete for the source-based setup workflow on exact source
`7250004d`. Hosted integration CI [44993864711](https://github.com/cyberducttape/StePanel/actions/runs/44993864711), CodeQL [44993864715](https://github.com/cyberducttape/StePanel/actions/runs/44993864715), documentation CI [44993864722](https://github.com/cyberducttape/StePanel/actions/runs/44993864722), and release evidence [44994033387](https://github.com/cyberducttape/StePanel/actions/runs/44994033387) passed. Setup output and the existing-file retry message both provide the root-owned private-copy path; parser regressions reject truncated quotes, trailing characters, and invalid escapes. This does not make the workflow available in the published v0.7.0 archive or certify an official release package.

Installer cancellation now prints its reuse command with Bash-safe quoting, so
saved configuration paths containing spaces or shell metacharacters remain
copyable. A regression checks the rendered command and round-trips a path with
spaces and quotes. Local installer settings and full shell-contract checks
passed; the follow-up is included in product source `5da789bb`, whose hosted
integration [45201896488](https://github.com/cyberducttape/StePanel/actions/runs/45201896488),
documentation [45201896612](https://github.com/cyberducttape/StePanel/actions/runs/45201896612),
and CodeQL [45201896556](https://github.com/cyberducttape/StePanel/actions/runs/45201896556)
all passed.

The documented `stepanel setup` flow must continue successfully into the
installer. Treat its generated settings as the supported contract: promote a
user-owned setup file into a root-only destination without changing it in
place, then preview and install from that copy. Reject malformed or truncated
settings rather than silently changing values.

**Acceptance criteria**

- CLI output includes copyable root-only copy, dry-run, and install commands.
- Paths with whitespace or shell metacharacters are quoted correctly.
- The docs show the same working manual command sequence.
- The installer rejects unterminated quotes, trailing data, invalid escapes,
  and final lines without newlines; valid generated values still round-trip.
- Targeted tests, shell syntax, documentation checks, and exact-source CI pass.

### M0ai — Bind domain claims to current tenant ownership (P1)

**Status:** Implemented and verified on code source `94fbf293`. Hosted
integration CI [45002811283](https://github.com/cyberducttape/StePanel/actions/runs/45002811283)
passed, including the full Go checks. Focused reservation-generation,
expiration-pruning, tenant binding, and failed-persistence regressions pass.

Domain verification must follow the durable tenant that owns a site, not a
reusable site-name string. Reservation confirmation and failure recovery must
be scoped to the exact claim generation admitted with the creation job. Bound
abandoned reservation growth without deleting confirmed claims or audit
history.

**Acceptance criteria**

- An expired tenant A claim cannot be unexpired or verified when tenant B
  reserves and creates the same site name.
- Customer route, staging, verification, and durable creation paths compare
  the verified claim tenant with the currently authorized durable site owner.
- Creation confirmation and retry cleanup affect only captured domain/token
  generations for the owning tenant.
- Expired reservations are pruned in bounded durable batches; confirmed claims
  are preserved and cleanup is observable/audited.
- Per-tenant pending reservations and serialized claim-state size have explicit
  limits, with failed persistence leaving in-memory state unchanged.
- Focused regressions, full Go checks, and exact-source hosted CI pass.

### M0aj — Keep file operations race-safe and tenant-fair (P2)

**Status:** Implemented and verified on code source `94fbf293`. Hosted
integration CI [45002811283](https://github.com/cyberducttape/StePanel/actions/runs/45002811283)
passed, including the full Go checks. The focused no-replace race regression
also passes on the current checkout.

The local file manager must keep the production no-overwrite rename contract,
and concurrent uploads must not let one tenant monopolize every host slot.

**Acceptance criteria**

- Rename uses an atomic no-replace publication primitive relative to opened,
  document-root-confined parent directories; unsupported platforms fail closed.
- A destination created after the initial existence check is preserved, the
  source remains intact, and the request reports a destination conflict.
- Upload admission has explicit per-tenant and host-wide limits, returns a
  retryable response on saturation, and releases slots after success or error.
- A saturated tenant cannot occupy all host upload slots, and another tenant
  can still be admitted.
- Focused regressions, full Go checks, and exact-source hosted CI pass.

### M0ak — Recover from transient customer file-list failures (P2)

**Status:** Implemented and verified on code source `94fbf293`. Hosted
integration CI [45002811283](https://github.com/cyberducttape/StePanel/actions/runs/45002811283)
passed, including the seeded Chromium customer-panel journey with transient
503 and successful retry. A failed folder listing stays visible while the
customer is in Files, and returning to that view retries automatically.
Downloads now stream up to the separately enforced 40 GiB limit; see M0ab.

**Acceptance criteria**

- A failed folder listing clears stale rows and reports the actual error.
- Returning to Files retries the current folder without requiring the
  customer to find and press Refresh.
- A successful retry restores the listing and actions for the current
  site/path only.
- The seeded Chromium customer-panel journey and exact-source CI pass.

### M0al — Make the offsite rclone config readable to the installed service (P1)

**Status:** Implemented and verified on exact source `55c1421a`. Integration
CI [45017173779](https://github.com/cyberducttape/StePanel/actions/runs/45017173779)
and the Ubuntu 24.04 + Caddy + MariaDB installed-host smoke
[45017173718](https://github.com/cyberducttape/StePanel/actions/runs/45017173718)
passed. The install's root-only default config discovery, staging ownership
and mode, source preservation, service-user remote access, and offsite `/readyz`
probe are recorded in the [host report](docs/lab-results/2026-11-22-ubuntu-rclone-staging-smoke-55c1421a.md).
This closes the config handoff check on this one profile; it is not repeated
host certification or public cloud-provider verification. The installer now
stages the selected root or user rclone config at
`/etc/stepanel/rclone.conf` as `root:stepanel` mode `0640`, records it in the
installer transaction, and points the service environment at the staged file.
The host smoke now starts from a root-only `0600` config and checks that the
original stays private while the service account can load the copied remote.

The guided installer runs as root to configure the host, so `rclone config`
normally selects a file under `/root` that the unprivileged panel cannot read.
Do not make `/root` or the original credentials file broadly readable. Copy
the config into the service's dedicated group-readable location and verify it
as the service account before activation.

**Acceptance criteria**

- The installer resolves the configured/default root rclone config before
  mutation and rejects missing, symlinked, non-regular, or oversized input
  with a direct diagnostic.
- The staged file is a transaction-managed target, root-owned, mode `0640`,
  group-readable by `stepanel`, and used as `RCLONE_CONFIG` by both services.
- The original root-only config remains unchanged and unreadable to `stepanel`.
- The service account can list the configured remote, and installed-host
  offsite readiness succeeds using the staged config.
- Shell syntax, ShellCheck, focused installer contracts, and exact-source
  installed-host CI pass.

### Guided site lifecycle experience (product work; not a release-gate closure)

**Status:** Implemented in the site workspace, with browser regression coverage.

The per-site guide connects existing cPanel/WordPress import, site validation,
deployment history, domain and TLS troubleshooting, recovery points and
rehearsals, staging clone/promotion, logs, PHP-FPM diagnostics, and the
administrator termination preview. New site creation opens the guide after
the creation job completes.

Recovery status identifies the most recent archive-validated backup, the last
restore attempt, the exact backup and evidence level of the last passing
rehearsal, and per-backup offsite upload/restore records. It explicitly avoids
equating an upload record with current remote reachability or application
health. A green recovery confidence state requires proof for the current
newest backup; evidence only for an older backup is degraded and identifies
the exact older recovery point.

Acceptance criteria:

- Each step links to the existing authorized workflow and does not bypass its
  preview, job, or confirmation contract.
- Missing data is shown as unknown/unavailable; process state, `wp-config.php`,
  database inventory, or Caddy certificate metadata is never presented as a
  successful live application/TLS check.
- WordPress WP-CLI status and PHP-FPM/log evidence render with authorization
  checks and bounded API reads.
- The browser regression covers steps, evidence limits, WordPress status,
  logs, staging navigation, and both passing and failing recovery evidence;
  backend recovery-status tests verify the exact offsite timestamps and UI
  accessibility E2E passes.
- This UI does not close the independent installed-host, application recovery,
  or capacity certification gates above.

### M0am — Keep the operator landing page focused on the next action (P2)

**Status:** Implemented and verified on `64bb74a4`. The overview keeps the
first-run checklist, host status, site creation, and recent jobs directly
available. Customer/account/database controls and migration/application forms
are grouped into two collapsed disclosures. Existing navigation and direct
hash links expand the owning disclosure before scrolling to the requested
section. The details use the selected appearance theme and remain keyboard
operable.

Acceptance criteria:

- The first-run operator view emphasizes health, first site creation, and
  recovery without presenting every optional administration form at once.
- Customer/database and migration/application tools remain in the document,
  but start collapsed for the operator; the customer page remains unchanged.
- Sidebar, footer, and direct URL navigation into a collapsed group reveal the
  requested section and scroll it into view.
- Mobile layout, keyboard interaction, and axe accessibility checks pass.
- The seeded Chromium navigation/accessibility suite and exact-source CI pass.

### M0an — Make first-run health failures understandable (P2)

**Status:** Implemented and verified on `7a271f18`. The first-run checklist
translates storage readiness failures into named storage areas,
human-readable free/required sizes, and a concrete refresh/remediation step.
Other readiness checks retain their provided evidence while receiving
human-readable labels. Exact-source hosted CI run
[45054850302](https://github.com/cyberducttape/StePanel/actions/runs/45054850302)
passed, including Go tests, race tests, recovery and ENOSPC drills, database
restore checks, static analysis, container checks, and browser accessibility
and end-to-end tests.

Acceptance criteria:

- Backup, import, job-state, and recovery-journal capacity failures do not
  expose raw byte counts or internal check keys in the first-run checklist.
- The checklist identifies the affected storage and says what an operator can
  do next without implying that deleting customer data is safe.
- Non-capacity readiness evidence remains visible and actionable.
- Browser regression, frontend unit tests, and exact-source CI pass.

### M0ao — Make offsite-storage setup understandable before installation (P2)

**Status:** Implemented and verified on `d9601463`. It now gives root-owned rclone
configuration commands, explains remote creation without assuming a provider,
links to the official headless authorization instructions, and shows the
`remote:path` form used by the wizard. It explains that StePanel tests
write/read/delete access at the chosen destination before collecting
administrator credentials. Exact-source documentation CI
[45058238503](https://github.com/cyberducttape/StePanel/actions/runs/45058238503)
and CodeQL [45058238442](https://github.com/cyberducttape/StePanel/actions/runs/45058238442)
passed.

Acceptance criteria:

- A first-time operator can identify the root config identity and configure
  a remote without exposing credentials through shell arguments or broadening
  `/root` permissions.
- The guide describes headless OAuth setup, destination naming, and the
  installer's write/read/delete probe accurately.
- Documentation links, source references, and exact-source documentation CI
  pass.

### M0ap — Preserve site-workspace form entries across tabs (P1)

**Status:** Implemented; browser and accessibility CI passed on `59248550`.
The full integration workflow is still finishing its Go race/recovery stages.
When a tab is re-rendered,
ordinary form values are captured and restored in memory for the current site
workspace. Passwords, tokens, credential-like fields, and selected files are
never retained; leaving a tab with one of those values prompts first. The
workspace announces restored entries and asks the operator to verify current
site state before applying them.

Acceptance criteria:

- Switching away and back preserves ordinary unsaved form values.
- A password or selected upload is not copied into the draft; the operator is
  warned and must re-enter it after returning.
- Drafts do not cross site workspaces or browser sessions.
- The browser regression covers a text-field round trip and password warning;
  frontend syntax, unit, accessibility, and hosted browser checks pass.

### M0aq — Remove the extra installer-settings copy step (P1)

**Status:** Implemented and verified on exact source `07726007`; the hosted Go
and shell-contract jobs passed in
[run 45108095198](https://github.com/cyberducttape/StePanel/actions/runs/45108095198).
`stepanel setup`
now prints a dry-run and install command that use its generated path directly.
For every `--config` install path, the installer validates that the input is a
regular non-symlink file, has one link, is private, and is owned by root or the
invoking sudo user; it then promotes the file to root ownership before parsing
secrets. This keeps guided setup and manual setup under one admission rule.

**Acceptance criteria**

- A normal setup-generated private file can be passed directly to the
  installer without a separate `sudo install` command.
- Unsafe owners, modes, links, and symlinks remain rejected before parsing.
- CLI instructions and installation documentation show the same direct flow.
- Setup unit tests, installer settings contracts, shell-contract checks,
  formatting, and exact-source CI pass.

### M0ar — Shorten the recommended first-install wizard (P2)

**Status:** Implemented and verified. The native wizard presents the tested
Caddy + MariaDB combination as one recommended profile, accepted with Enter.
The Custom path still asks for the web server and database separately and
retains exact-stack support classification before offsite setup or credential
collection. The guide and installation docs now describe six steps rather than
separate server and database steps. Exact-source integration CI on `220e9cab`
passed ([run 45071930248](https://github.com/cyberducttape/StePanel/actions/runs/45071930248));
the documentation and CodeQL workflows passed on documentation-only descendant
`fedc617a` ([docs](https://github.com/cyberducttape/StePanel/actions/runs/45072808070),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/45072808111)).

**Acceptance criteria**

- The recommended Caddy + MariaDB stack requires one profile choice and is
  selected by Enter.
- Custom remains available and preserves all existing component choices,
  TLS behavior, and support warnings.
- Transcript tests cover both the default and custom paths; setup package and
  full repository checks pass; exact-source hosted CI passes.

### M0as — Gate tagged packages on a real fresh-host install (P1)

**Status:** The non-publishing exact-source `v0.9.0` candidate preflight
[45202075337](https://github.com/cyberducttape/StePanel/actions/runs/45202075337)
passed on product source `5da789bb51e6da28e5c94667ebc8cf2f27049462`. The
Ubuntu 24.04 + Caddy + MariaDB disposable systemd host installed the versioned
archive, completed cleanup, and passed the installed WordPress HTTPS/database
recovery proof with measured RTO 7,668 ms. One recovery-matrix iteration
passed. The amd64/arm64 archives, checksums, package contents, exact-source
evidence, SPDX SBOM, and packaged setup/CLI checks all passed. Full run evidence
and artifact checksums are in the [reviewed result](docs/lab-results/2026-11-23-v0.9.0-ubuntu-install-45202075337.md).
The candidate container lacks project-quota enforcement, so Git deployment
refused safely. A separate ext4 quota-enabled installed-host smoke on the same
product source [45203221241](https://github.com/cyberducttape/StePanel/actions/runs/45203221241)
verified active user/project quotas before and after abrupt QEMU loss and
exercised a real project-quota rejection; see its [scoped report](docs/lab-results/2026-11-23-ubuntu-ext4-quota-install-45203221241.md).
This is one Ubuntu/ext4 run; XFS, production mounts, and repeated quota/recovery
evidence remain open. A single iteration does not satisfy repeated recovery or
physical power-loss gates. This is not
production certification; no tag or public release was created. The tag-triggered
publication event remains unexercised.
The tag-triggered release workflow passes the tag into the packaged Ubuntu
24.04 + Caddy + MariaDB install-smoke workflow. With a tag, that workflow
builds the exact versioned archive using GoReleaser's current-tag override and
`--skip=publish`, then waits for its database-backed WordPress HTTPS recovery
proof before the release job can publish. Routine smoke runs continue to use
snapshot archives. A manually dispatched release-candidate preflight performs
the same install proof, version/source checks, release checks, artifact
verification, and temporary artifact retention with read-only permissions and
no publish step. Contract checks prevent these safeguards from drifting.
This improves the package onboarding gate but does not certify production or
replace repeated failure-recovery, quota, power-loss, capacity, and security
gates. The latest packaged install smoke passed on source `2e9e06e9`: installation
and cleanup completed, WordPress HTTPS/database recovery measured 4,398 ms,
and one recovery-matrix iteration passed. See the
[`first-host smoke report`](docs/lab-results/2026-11-23-ubuntu-packaged-first-host-smoke-2e9e06e.md).
The exact-source `v0.9.0` preflight's installed-host job also passed on
`ab8a5151` in [run 45173194319](https://github.com/cyberducttape/StePanel/actions/runs/45173194319): the versioned archive installed and cleanup completed, the WordPress HTTPS/database recovery proof measured 4,905 ms, and one recovery-matrix iteration passed. The disposable host had no project quota enforcement, so Git deployment refused safely. The final candidate-artifact verification job is still queued; this is not a completed current-head preflight or a published release. See the [scoped report](docs/lab-results/2026-11-23-v0.9.0-ubuntu-install-45173194319.md).
The read-only manual preflight for `v0.9.0` passed on source
`679cd6db9c283d78e70102f1dd87943fbe70e4d4` in
[workflow run 45134667043](https://github.com/cyberducttape/StePanel/actions/runs/45134667043).
It built and installed the exact versioned archive, completed the WordPress
HTTPS/database recovery proof in 7,359 ms, passed one recovery-matrix iteration,
generated and verified exact-source release evidence and an SBOM, and retained
the candidate artifact. This was a disposable systemd container without project
quotas; Git deployment correctly failed closed. No tag was pushed and no release
was published. The newer exact-source preflight on `2012d87`
[45146220535](https://github.com/cyberducttape/StePanel/actions/runs/45146220535)
also passed: the versioned package installed with cleanup complete, the
database-backed WordPress HTTPS recovery proof measured 7,597 ms, and one
recovery-matrix iteration passed. Both amd64/arm64 archive checksums were
verified locally; the candidate, SPDX SBOM, and exact-source release evidence
were retained. The report records 4/21 P0 gates complete and the NO-GO release
decision. This was still a quota-less disposable systemd container, and no tag
was pushed or release published. See the
[reviewed result](docs/lab-results/2026-11-23-v0.9.0-ubuntu-install-45146220535.md).
The candidate's original embedded workflow-evidence table omitted its
reusable install job because GitHub attributes that job to the caller. The
evidence collector now verifies the required caller job and reports its own
status; regression tests cover unrelated callers and failed install jobs.
The refreshed attribution is documented
[here](docs/lab-results/2026-11-23-v0.9.0-evidence-attribution.md).
Broader production certification remains separate.

**Acceptance criteria**

- Every tagged release waits for the exact-tag release archive to complete the
  recommended fresh-host installation and WordPress recovery smoke.
- Manual preflight can build, install-test, verify, and retain a versioned
  candidate without creating a tag or publishing artifacts.
- A failed or unavailable smoke prevents all package and container publication.
- Release contract and full local checks pass; the manual hosted preflight
  confirms the reusable workflow builds, installs, and verifies the requested
  versioned archive without publication. The actual tag-triggered release event
  remains unexercised because it would enter the publication workflow.
- The installation guide states the guarantee and its limits.

### M0at — Make customer MFA enrollment self-service (P1)

**Status:** Implemented and verified on exact source `00969432`. The account
page requests a one-time QR/setup response, offers a copyable seed if QR
scanning is unavailable, and enables MFA only after the customer enters a
matching authenticator code. The native setup wizard prints a terminal QR for
the initial administrator, with the grouped seed and `otpauth://` link as
fallbacks. Setup responses are `no-store`; the customer's new seed is not
persisted until verification. Full local `make check` and the hosted
[integration](https://github.com/cyberducttape/StePanel/actions/runs/45077529892),
[browser E2E/accessibility](https://github.com/cyberducttape/StePanel/actions/runs/45077529892),
[documentation](https://github.com/cyberducttape/StePanel/actions/runs/45077529818),
[CodeQL](https://github.com/cyberducttape/StePanel/actions/runs/45077529901),
and [release-evidence](https://github.com/cyberducttape/StePanel/actions/runs/45077701149)
workflows passed.

**Acceptance criteria**

- A customer can start enrollment from Account without manually inventing or
  typing a TOTP seed; the UI presents an authenticator QR and a manual-key
  fallback.
- The server does not persist the new seed until it verifies a current TOTP
  code for that seed, and setup data is not cacheable.
- Browser coverage verifies the setup, code submission, and secret removal
  after success; backend tests reject an incorrect code and accept a valid one.
- OpenAPI, documentation references, formatting, and full repository CI pass.
- Initial administrator enrollment can scan the terminal QR or use either
  fallback; the setup guide test confirms these instructions are present.

### M0au — Make the first customer site follow the supported WordPress path (P2)

**Status:** Implemented and pushed as `2c1b4693`; the feature and its follow-up
are included in product source `5da789bb`. When an account has no assigned
sites, the customer site form selects WordPress by default and exposes its
setup fields. Blank PHP remains available for customers deploying another
application, with concise guidance explaining that choice. Existing mandatory
domain-ownership verification is unchanged. `make check`, frontend unit tests,
and the customer-panel browser E2E/accessibility job passed. Exact-source
hosted integration [45201896488](https://github.com/cyberducttape/StePanel/actions/runs/45201896488),
documentation [45201896612](https://github.com/cyberducttape/StePanel/actions/runs/45201896612),
and CodeQL [45201896556](https://github.com/cyberducttape/StePanel/actions/runs/45201896556)
passed; the later packaged candidate install [45202075337](https://github.com/cyberducttape/StePanel/actions/runs/45202075337)
also passed. Its one recovery iteration and missing quota enforcement remain
limited evidence, not production certification.

**Acceptance criteria**

- A new customer account sees WordPress selected and its setup fields visible.
- Blank PHP remains selectable and explains when it is appropriate.
- Browser coverage verifies the empty-account first-site state; frontend unit
  tests, full local checks, and exact-source CI pass.
- File-manager browser coverage also asserts each rendered row has the same
  five cells as the table header, guarding the core file-browsing layout.

### M0ba — Defer empty-customer inventory work (P2)

**Status:** Implemented and verified on exact source `e7a48b75`. The seeded
account-owner journey passes on a rebuilt panel binary, and the full browser
suite passes (33 passed, one expected skip). Frontend unit tests pass (19/19),
`make check`, release checks, and documentation validation pass. Hosted
integration, browser E2E, documentation, CodeQL, and release-evidence workflows
passed; see [integration run 45123113734](https://github.com/cyberducttape/StePanel/actions/runs/45123113734).

The customer home page should not enumerate sites or backups when the account
has no sites, and the hidden site grid should not load its own inventory before
the customer opens Websites. A server-rendered site count is only a scheduling
hint: if the authoritative account response disagrees, load the evidence needed
for the real current state. Customer pages should not call administrator-only
suspension endpoints.

**Acceptance criteria**

- A new account with no sites loads its hosting identity and first-site setup
  checklist without requesting the site overview, backup inventory, or
  suspension inventory.
- Opening Websites loads the site grid and its backup labels; a direct site
  workspace link still opens its requested workspace.
- If hosting assignments change after the page was rendered, the authoritative
  response triggers the required overview and recovery reads.
- Customer pages do not issue the administrator-only suspension request.
- Seeded owner browser coverage proves request counts and first-site behavior;
  full browser E2E, unit tests, local checks, and exact-source CI pass.

### M0bb — Prove the 100 MiB customer upload path end to end (P2)

**Status:** Implemented and verified on exact source `e7a48b75`. The browser
file manager already supports
resumable uploads up to 100 MiB in ordered 4 MiB chunks with capacity
reservation, tenant checks, checksum verification, and atomic publication.
The customer API regression now transfers the full limit, recreates the App
between chunks to model a control-plane restart, resumes from durable session
state, and hashes the published file as a stream. Its fixture uses one chunk
buffer rather than retaining the full artifact and output in memory. Focused
Go tests, full `make check`, sitefiles race test, and exact-source hosted CI
pass. The CI run includes race tests, recovery, ENOSPC, MySQL/PostgreSQL
integration, and browser E2E. This does not certify physical power-loss
recovery or a production host.

**Acceptance criteria**

- A 100 MiB transfer uses requests no larger than 4 MiB and resumes from
  persisted session metadata after App/manager reconstruction.
- Published bytes match the expected SHA-256; wrong-tenant access and paths
  outside the document root are refused.
- Capacity exhaustion is rejected before session admission, and upload
  concurrency remains tenant-bounded.
- The seeded customer E2E verifies chunk ordering and multi-request upload
  behavior; the 100 MiB durable-session path is exercised through the API.
- Focused and full Go checks, browser tests, and exact-source hosted CI pass.

### M0bc — Keep themed primary actions accessible on first paint (P2)

**Status:** Implemented and verified on exact source `e7a48b75`. CI for
`db39eb83` found that the global button
color transition briefly rendered “Add website” at 4.16:1 contrast while the
light theme initialized. The appearance stylesheet now applies primary-action
colors without an intermediate animation and sets a contrasting foreground,
including the Windows 95/3.1 palettes. A unit test checks the foreground and
accent contrast across all ten themes and prohibits the first-paint tween.
Frontend unit tests and the seeded customer browser accessibility suite pass;
full hosted integration, documentation, CodeQL, and release evidence also pass.

**Acceptance criteria**

- Every theme's primary-action foreground/accent pair meets WCAG AA 4.5:1.
- Theme initialization cannot animate through an insufficient-contrast color.
- Frontend unit tests and the seeded customer browser accessibility suite pass;
  exact-source hosted CI passes.

### M0bd — Make large-file SFTP handoff clear to customers (P2)

**Status:** Implemented and verified on exact source `07f83f2a`, an ancestor of
current `main`. The SFTP guide gives the selected site's Unix username,
document root, and Security settings link, and explains that host/port details
and SFTP availability depend on host policy. Oversized browser uploads and
editor saves open the guide automatically; when site details are unavailable,
the user gets a direct administrator handoff. The seeded regression covers the
oversized upload path. Hosted [CI integration 45129625487](https://github.com/cyberducttape/StePanel/actions/runs/45129625487)
passed seeded browser/accessibility tests, the full Go suite, race tests,
recovery and ENOSPC drills, database restore checks, and container and
infrastructure validation. [Documentation 45129625591](https://github.com/cyberducttape/StePanel/actions/runs/45129625591),
[CodeQL 45129625495](https://github.com/cyberducttape/StePanel/actions/runs/45129625495),
and [release evidence 45132709532](https://github.com/cyberducttape/StePanel/actions/runs/45132709532)
also passed. This improves the supported SFTP handoff; it does not implement
first-party SFTP provisioning or change the bounded 100 MiB browser-upload
limit.

**Acceptance criteria**

- A seeded customer sees the SFTP guide with the correct account username and
  document root for each selected site.
- The settings link targets that site's Security workspace; switching sites
  updates both folder and link.
- Guidance states that host/port and SFTP availability are host-policy-specific
  and does not request private-key disclosure.
- Attempting to upload a file above 100 MiB opens the guide and points to the
  current site's details; if details are unavailable, the UI directs the
  customer to the hosting administrator.
- Frontend checks, seeded browser regression, documentation checks, and
  exact-source hosted CI pass.

### M0be — Keep the mobile operator first-run view readable (P2)

**Status:** Implemented and verified on exact source `2012d87c`. The screenshot
evidence test waits for the mobile navigation drawer's closed transition before
capture, and security-posture rows switch to a two-column mobile layout so each
check's evidence sits under its heading instead of competing for a narrow third
column. The seeded Chromium screenshot journey passes; the 390px artifact was
visually inspected. The exact-source integration workflow passed, including the
full Go suite, race tests, recovery and ENOSPC drills, database restore
integrations, and browser/accessibility E2E in
[run 45146201250](https://github.com/cyberducttape/StePanel/actions/runs/45146201250).
Documentation, CodeQL, and release-evidence workflows also passed.

**Acceptance criteria**

- Mobile screenshots are captured only after the drawer has reached its
  closed position; its ARIA state and backdrop agree.
- Security-check labels remain readable at a 390px viewport, with evidence
  below the label and beside the status marker.
- The seeded browser screenshot regression and frontend unit suite pass;
  exact-source hosted CI passes.

### M0bf — Make setup cancellation honest about rclone changes (P2)

**Status:** Implemented and verified on product source `5da789bb`. The setup
guide says choosing an offsite remote updates the current user's rclone
configuration immediately and that it remains if setup is canceled. The
cancellation error distinguishes unsaved installer settings from that
retained configuration. Focused setup tests, `make check`, and
`go test -race ./internal/setup -count=1` passed locally. Exact-source hosted
integration [45201896488](https://github.com/cyberducttape/StePanel/actions/runs/45201896488),
documentation [45201896612](https://github.com/cyberducttape/StePanel/actions/runs/45201896612),
and CodeQL [45201896556](https://github.com/cyberducttape/StePanel/actions/runs/45201896556)
passed. The later packaged install preflight [45202075337](https://github.com/cyberducttape/StePanel/actions/runs/45202075337)
also passed, with quota/repeated-certification limitations recorded in its
scoped report.

**Acceptance criteria**

- The guide explains the rclone configuration side effect before asking the
  operator to make it.
- Canceling after rclone configuration reports that installer settings were
  not saved and that the chosen rclone configuration remains.
- A regression exercises the configure-then-cancel path and confirms settings
  are not reported saved.
- Setup tests, full checks, and exact-source CI pass.

### M0bg — Keep evaluation instructions consistent in the setup wizard (P2)

**Status:** Implemented and verified on product source `5da789bb`. The setup
wizard points first-time evaluators to `bash scripts/evaluate-local.sh`,
matching the README and installation guide. The setup regression checks the
wizard text, and `make check` passed locally. Exact-source hosted integration
[45201896488](https://github.com/cyberducttape/StePanel/actions/runs/45201896488),
documentation [45201896612](https://github.com/cyberducttape/StePanel/actions/runs/45201896612),
and CodeQL [45201896556](https://github.com/cyberducttape/StePanel/actions/runs/45201896556)
passed.

**Acceptance criteria**

- The setup wizard's evaluation path works without GNU Make.
- User-facing entry points consistently identify the Bash script as the
  primary command and describe `make evaluate` only as an optional alias.
- The setup test and `make check` pass; exact-source CI passes.

### M0bh — Make the native install guide scannable (P2)

**Status:** Implemented and documentation-checked on product source
`5da789bb`. The install guide presents the guided flow as four ordered
steps—read-only preflight, user-level build, credential setup, and host
installation—and removes repeated explanations of the same checks and rclone
setup. It keeps the support limits, safe local preview, package prerequisites,
and host-change consent. Documentation links/references and hosted
documentation CI [45201896612](https://github.com/cyberducttape/StePanel/actions/runs/45201896612)
passed. The separate installed-host candidate [45202075337](https://github.com/cyberducttape/StePanel/actions/runs/45202075337)
passed on Ubuntu 24.04 + Caddy + MariaDB; it does not close the documented
quota or repeated recovery gates.

**Acceptance criteria**

- A first-time operator can find the one recommended command and understand
  prerequisites, preflight effects, credential timing, and host mutations in
  one chronological section.
- The read-only preflight, quota requirements, offsite-backup requirement,
  support-tier limits, and existing-web-server consent remain explicit.
- The local preview remains clearly separate from a native install and forbids
  network exposure and real customer data.
- Documentation links/references and exact-source documentation CI pass.

### M0bi — Keep first-run checklist actions readable (P2)

**Status:** Implemented and verified on `5da789bb`. Setup checklist actions
now sit below their explanations instead of competing for a narrow third grid
column. The seeded browser regression checks the explanation/action geometry
at desktop and mobile widths. The exact-source seeded browser/accessibility job passed in
[run 45201896488](https://github.com/cyberducttape/StePanel/actions/runs/45201896488),
as did the full integration job in the same run, documentation
[45201896612](https://github.com/cyberducttape/StePanel/actions/runs/45201896612),
and CodeQL [45201896556](https://github.com/cyberducttape/StePanel/actions/runs/45201896556).
Local `make check` and frontend unit tests pass. The separate packaged
installed-host candidate [45202075337](https://github.com/cyberducttape/StePanel/actions/runs/45202075337)
also passed; the scoped report records its limits and remaining release gates.

**Acceptance criteria**

- Long first-run diagnostic descriptions and their actions never overlap at
  desktop or mobile viewport widths.
- A browser regression checks the action position at 1440px and 390px.
- The local evaluation preview has no uncaught browser errors and no horizontal
  overflow at those widths.
- Frontend unit tests, installer publication recovery tests, and exact-source
  seeded browser CI pass.

