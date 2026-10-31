# Contributing to StePanel

Thanks for helping improve StePanel. The project handles server state and backups, so changes should favor explicit behavior, safe defaults, and clear operational documentation.

## Development setup

Requirements: Go 1.26+ (any patch version), Git, and a Unix-like shell.

Go 1.26 is required for `range over int` and other standard library improvements. Any Go 1.26.x patch version is acceptable; we do not require a specific patch version to support air-gapped builds and varied CI environments.

```sh
git clone git@github.com:cyberducttape/StePanel.git
cd StePanel
make check
go run ./cmd/stepanel
```

The Make targets intentionally bound Go package and runtime concurrency because
the test suite starts SQLite workers and helper subprocesses. This keeps local
validation usable on small VMs, containers, and hosts with a low process
limit. The defaults are equivalent to `GOMAXPROCS=2` and `go test -p 1`;
override them only when the host has sufficient capacity:

```sh
make check TEST_PROCS=4 TEST_PARALLELISM=2
```

For the most constrained environments, use `make check TEST_PROCS=1`.

For the full pre-review gate, run `make audit`. It adds race detection and
release-metadata validation to the normal formatting, vet, and test checks.

The local server uses `data/imports` and `data/www` by default, so development does not require root. Never test a restore against a production backup or live web root.

## Pull requests

- Explain the operator-facing behavior and the security implications.
- Add or update tests for parsing, validation, and failure paths.
- Run `make audit`, `bash -n install.sh`, and ShellCheck before opening a pull
  request. On constrained hosts, run the race target in CI and record any
  environment limitation rather than weakening the check.
- Update the README, relevant guide, and `CHANGELOG.md` when behavior changes.
- Keep commits focused and avoid bundling unrelated formatting changes.
- Keep package-local unit tests beside the package they exercise. Root-package
  tests intentionally remain beside the root handlers while those handlers
  still use unexported control-plane types; extracted domains belong with their
  implementation under `internal/<domain>/`.
- Use your attributable developer identity for commits; do not create project
  history as `root`.
- Open a pull request for substantial changes, even when you are the sole
  maintainer, so design and review history remains visible.

## Testing against real behavior

Several production defects have passed tests that used invented input or ran
only in development mode. Prefer these patterns:

- **Use captured tool output, not hand-written values.** Parsers of system
  tools are tested against real output stored under `testdata/` (for example
  `testdata/systemctl/`, captured with the commands in its README). systemd
  prints `*USec` properties as timespans such as `1.500000s`, not integers.
- **Test remote commands as the remote shell sees them.** `ssh` joins its
  remote arguments with spaces and the remote shell parses them again; the
  recording fake `ssh` in `ssh_inventory_test.go` asserts the exact words.
- **Inject failures at every step of a destructive workflow.** Use
  `failureInjection("<operation>", "<point>")` and drive it with
  `STEPANEL_FAIL_AT=<operation>:<point>` (points are listed in
  `docs/V1_PRODUCTION_GATES.md`). Assert the end state after each failure, not
  just the error: see `staging_promotion_flow_test.go`. Then break the
  compensation code once and confirm the test fails.
- **Exercise production-only branches.** Code guarded by `cfg.Production`
  (typed broker calls, the capacity ledger) is skipped by most tests. Use the
  shared helpers (`createSiteBackupContext(..., a.capacityLedger())`,
  `databaseLockKey`) instead of reimplementing them; source guards fail the
  build when panel code bypasses them.
- **Validate alert rules with `promtool`.** CI runs `promtool check rules` and
  `promtool test rules observability/alerts_test.yml`; add a test case for
  each new alert, including that warning and critical pairs do not overlap.

## Design principles

1. Validate untrusted archive input before extraction.
2. Make destructive actions explicit and auditable.
3. Keep the dependency surface small.
4. Prefer recoverable staging over direct writes to live sites.
5. Record durable architectural choices as numbered ADRs in `docs/adr/`.
