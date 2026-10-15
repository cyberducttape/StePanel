# Current-head hosted checks — 2026-10-15

| Field | Value |
|---|---|
| Tested source | `7badb0ba3bd158b9dda68e64520dcdb7e1f80c6a` |
| Worktree | Clean at inspection |
| Local toolchain | Go 1.26.7 |
| Hosted CI | [Run 38690891081](https://github.com/cyberducttape/StePanel/actions/runs/38690891081) — passed |
| CodeQL | [Run 38690891066](https://github.com/cyberducttape/StePanel/actions/runs/38690891066) — passed |
| Captured | 2026-10-15 |

## Results

- Local `go test -p 1 -timeout 10m ./...` passed (root package 65.834 s).
- Local `go test . -run '^TestOffsite' -count=1` passed, including a
  published-backup round trip through a fake rclone backend that injects a
  completion-marker publication failure, retries the same artifact, downloads
  it, and runs strict backup verification.
- The focused off-site retry test passed under `go test -race`.
- Local `go vet ./...` and `git diff --check` passed.
- Hosted CI passed the full Go suite and configured coverage floors, race suite,
  repository recovery drills, real tmpfs ENOSPC recovery, mixed-load checks,
  shell/docs/security checks, container build and smoke/restart test, Helm and
  Kubernetes validation, Terraform validation, and container scan.
- Hosted UI E2E/accessibility checks passed as the second CI job.
- CodeQL completed successfully. The repository code-scanning alert query
  returned no alerts in the open state at capture.

## Pending and out-of-scope evidence

- The exact-head six-host installed-host smoke, run
  [38690891035](https://github.com/cyberducttape/StePanel/actions/runs/38690891035),
  was pending at capture.
- The exact-head quota-enabled host run,
  [38690891070](https://github.com/cyberducttape/StePanel/actions/runs/38690891070),
  was still running its Ubuntu guest install at capture.
- The 20-repeat six-host recovery matrix,
  [38670189826](https://github.com/cyberducttape/StePanel/actions/runs/38670189826),
  remains in progress on older source `1fb3879`; it is not evidence for this
  commit and is not counted as complete.
- These checks do not establish physical host power-loss recovery, complete
  publication/import/database-restore ENOSPC coverage, a certified capacity
  envelope, the full supported runtime matrix, or production readiness.

