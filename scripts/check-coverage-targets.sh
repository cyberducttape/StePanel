#!/usr/bin/env bash
set -Eeuo pipefail

# Per-package coverage targets for critical components
# These enforce quality standards for safety-critical code.
#
# internal/backup is intentionally absent: it currently declares only types,
# so it has no statements to measure. The backup engine still lives in the
# root package (backups.go). Add a target here when that logic is extracted;
# the checker fails on any listed package with no coverage data.

declare -A COVERAGE_TARGETS=(
  ["github.com/cyberducttape/StePanel/internal/auth"]="90"
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

# Pass the declared targets to awk so COVERAGE_TARGETS is the only list;
# a package declared above can never be silently skipped by the checker.
target_spec=""
for package in "${!COVERAGE_TARGETS[@]}"; do
  target_spec+="${package}=${COVERAGE_TARGETS[$package]};"
done

awk -v target_spec="$target_spec" '
BEGIN {
  entries = split(target_spec, pairs, ";")
  for (i = 1; i <= entries; i++) {
    if (pairs[i] == "") continue
    split(pairs[i], kv, "=")
    targets[kv[1]] = kv[2] + 0
  }
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
