# Current-head validation — 2026-10-15

| Field | Value |
|---|---|
| Tested source | `7ca34dcd027c1db438202777c8230d692b8f1a3d` |
| Worktree | Clean at inspection |
| Local toolchain | Go 1.26.7 |
| Hosted CI | [Run 38692216922](https://github.com/cyberducttape/StePanel/actions/runs/38692216922) — passed |
| CodeQL | [Run 38692216955](https://github.com/cyberducttape/StePanel/actions/runs/38692216955) — passed |
| Captured | 2026-10-15 |

## Results

- Local `go test -race -p 1 -timeout 20m ./...` passed on this source. The root
  package completed in 443.572 seconds; every Go package passed.
- Hosted CI passed `gofmt`, `make release-check`, `go vet ./...`, the full Go
  test suite and coverage floors, race tests, repository recovery drills, two
  real tmpfs ENOSPC checks, mixed-load tests, clean Python deployment, shell
  lint/syntax, documentation validation, alert-rule tests, vulnerability and
  container scans, production-container smoke, Helm/Kubernetes/Terraform
  validation, and UI E2E/accessibility tests.
- CodeQL completed successfully. The UI E2E job uploaded live seeded
  screenshots; its accessibility/browser tests passed.
- Focused offsite transfer deadline, publication retry, and restore-verification
  tests passed during this audit. The general transfer deadline is configurable
  and separate from the rclone inactivity timeout.

## Pending and out-of-scope evidence

- The exact-head six-host install smoke [38692216889](https://github.com/cyberducttape/StePanel/actions/runs/38692216889)
  is queued. The exact-head quota-enabled guest run [38692216846](https://github.com/cyberducttape/StePanel/actions/runs/38692216846)
  is pending.
- The full 20-repeat six-host recovery matrix [38692636041](https://github.com/cyberducttape/StePanel/actions/runs/38692636041)
  is pending behind the older live matrix [38670189826](https://github.com/cyberducttape/StePanel/actions/runs/38670189826),
  which targets source `1fb3879` and is not current-head evidence.
- Physical host power-loss recovery, publication/import/database-restore
  ENOSPC, live notification outage/retry, certified capacity, complete runtime
  support certification, and independent security review remain open. These
  checks do not establish production readiness.
