#!/usr/bin/env bash
# Run the repository-level production gate. Real-host and power-loss evidence
# remain separate because they require an isolated VM or physical host.
set -Eeuo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

run() {
  printf '\n==> %s\n' "$*"
  "$@"
}

gofmt_files=$(gofmt -l .)
if [[ -n $gofmt_files ]]; then
  printf 'gofmt required for:\n%s\n' "$gofmt_files" >&2
  exit 1
fi
run go vet ./...
coverage_profile=${COVERAGE_PROFILE:-/tmp/stepanel-production-coverage.out}
run go test -p "${GO_TEST_PARALLELISM:-1}" -timeout "${GO_TEST_TIMEOUT:-10m}" ./... -coverprofile="$coverage_profile" -covermode=atomic
run bash scripts/check-coverage.sh "$coverage_profile"
run bash scripts/check-coverage-targets.sh "$coverage_profile"
run go test -p "${GO_TEST_PARALLELISM:-1}" -race -timeout "${GO_RACE_TIMEOUT:-10m}" ./...
run bash deploy/lab/run-recovery-drills.sh "${RECOVERY_DRILL_OUTPUT:-/tmp/stepanel-recovery-drills.md}"
run go test -p 1 -run '^$' -bench '^BenchmarkControlPlane' -benchtime=1x -count=1 .
run bash scripts/check-docs-links.sh docs
run bash scripts/check-action-pins.sh
run bash scripts/validate-assets.sh
run bash -n install.sh deploy/lab/*.sh scripts/*.sh

printf '\nRepository production gate passed.\n'
printf 'Real-host ENOSPC, power-loss, distro, screenshot, and load-capacity evidence remain external gates.\n'
