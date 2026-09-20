#!/usr/bin/env bash
set -Eeuo pipefail

# Per-package coverage targets for critical components
# These enforce quality standards for safety-critical code

declare -A COVERAGE_TARGETS=(
  ["github.com/itchyitchy123/StePanel/internal/auth"]="90"
  ["github.com/itchyitchy123/StePanel/internal/backup"]="85"
  ["github.com/itchyitchy123/StePanel/internal/helper"]="90"
  ["github.com/itchyitchy123/StePanel/internal/state"]="85"
  ["github.com/itchyitchy123/StePanel/internal/migration"]="85"
  ["github.com/itchyitchy123/StePanel/internal/operations"]="80"
  ["github.com/itchyitchy123/StePanel/internal/session"]="90"
  ["github.com/itchyitchy123/StePanel/internal/jobs"]="85"
  ["github.com/itchyitchy123/StePanel/internal/audit"]="95"
)

profile=${1:-coverage.out}

[[ -f "$profile" ]] || { echo "coverage profile not found: $profile" >&2; exit 1; }

echo "Checking per-package coverage targets..."
echo ""

failed=0
go tool cover -func="$profile" | awk '
BEGIN {
  targets["github.com/itchyitchy123/StePanel/internal/auth"] = 90
  targets["github.com/itchyitchy123/StePanel/internal/backup"] = 85
  targets["github.com/itchyitchy123/StePanel/internal/helper"] = 90
  targets["github.com/itchyitchy123/StePanel/internal/state"] = 85
  targets["github.com/itchyitchy123/StePanel/internal/migration"] = 85
  targets["github.com/itchyitchy123/StePanel/internal/operations"] = 80
  targets["github.com/itchyitchy123/StePanel/internal/session"] = 90
  targets["github.com/itchyitchy123/StePanel/internal/jobs"] = 85
  targets["github.com/itchyitchy123/StePanel/internal/audit"] = 95
}

/^github.com\/itchyitchy123\/StePanel/ {
  package = $1
  coverage = $NF
  gsub("%", "", coverage)

  if (package in targets) {
    target = targets[package]
    if (coverage + 0 >= target + 0) {
      printf "✅ %-50s %6.1f%% ≥ %3.0f%%\n", package, coverage, target
    } else {
      printf "❌ %-50s %6.1f%% < %3.0f%%\n", package, coverage, target
      exit_code = 1
    }
  }
}

END {
  if (exit_code) exit 1
}
' || failed=1

echo ""
if [[ $failed -eq 0 ]]; then
  echo "✅ All per-package coverage targets met!"
  exit 0
else
  echo "❌ Some packages failed coverage targets."
  echo ""
  echo "To improve coverage for a package:"
  echo "  go test -v -coverprofile=coverage.out ./internal/package/..."
  echo "  go tool cover -html=coverage.out"
  exit 1
fi
