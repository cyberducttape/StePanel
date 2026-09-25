# Contributing to StePanel

Thanks for helping improve StePanel. The project handles server state and backups, so changes should favor explicit behavior, safe defaults, and clear operational documentation.

## Development setup

Requirements: Go 1.26+, Git, and a Unix-like shell.

```sh
git clone git@github.com:cyberducttape/StePanel.git
cd StePanel
make check
go run .
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

## Design principles

1. Validate untrusted archive input before extraction.
2. Make destructive actions explicit and auditable.
3. Keep the dependency surface small.
4. Prefer recoverable staging over direct writes to live sites.
5. Record durable architectural choices as numbered ADRs in `docs/adr/`.
