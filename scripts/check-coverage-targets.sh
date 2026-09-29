#!/usr/bin/env bash
set -Eeuo pipefail

# Per-package coverage targets for critical components
# These enforce quality standards for safety-critical code

declare -A COVERAGE_TARGETS=(
  ["github.com/cyberducttape/StePanel/internal/auth"]="90"
  ["github.com/cyberducttape/StePanel/internal/backup"]="85"
  ["github.com/cyberducttape/StePanel/internal/helper"]="90"
  ["github.com/cyberducttape/StePanel/internal/state"]="85"
  ["github.com/cyberducttape/StePanel/internal/migration"]="85"
  ["github.com/cyberducttape/StePanel/internal/operations"]="80"
  ["github.com/cyberducttape/StePanel/internal/session"]="90"
  ["github.com/cyberducttape/StePanel/internal/jobs"]="85"
  ["github.com/cyberducttape/StePanel/internal/audit"]="95"
)

profile=${1:-coverage.out}

[[ -f "$profile" ]] || { echo "coverage profile not found: $profile" >&2; exit 1; }

echo "Checking per-package coverage targets..."
echo ""

awk '
BEGIN {
  targets["github.com/cyberducttape/StePanel/internal/auth"] = 90
  targets["github.com/cyberducttape/StePanel/internal/backup"] = 85
  targets["github.com/cyberducttape/StePanel/internal/helper"] = 90
  targets["github.com/cyberducttape/StePanel/internal/state"] = 85
  targets["github.com/cyberducttape/StePanel/internal/migration"] = 85
  targets["github.com/cyberducttape/StePanel/internal/operations"] = 80
  targets["github.com/cyberducttape/StePanel/internal/session"] = 90
  targets["github.com/cyberducttape/StePanel/internal/jobs"] = 85
  targets["github.com/cyberducttape/StePanel/internal/audit"] = 95
}

/^mode:/ { next }
{
  file = $1
  statements = $2 + 0
  count = $3 + 0
  for (package in targets) {
    prefix = package "/"
    if (index(file, prefix) == 1) {
      total[package] += statements
      if (count > 0) covered[package] += statements
      break
    }
  }
}

END {
  failed = 0
  for (package in targets) {
    if (total[package] == 0) {
      printf "❌ %-50s no coverage data\n", package
      failed = 1
      continue
    }
    coverage = 100 * covered[package] / total[package]
    target = targets[package]
    if (coverage + 0 >= target + 0) {
      printf "✅ %-50s %6.1f%% ≥ %3.0f%%\n", package, coverage, target
    } else {
      printf "❌ %-50s %6.1f%% < %3.0f%%\n", package, coverage, target
      failed = 1
    }
  }
  if (failed) exit 1
}
' "$profile"

echo ""
echo "✅ All per-package coverage targets met!"
