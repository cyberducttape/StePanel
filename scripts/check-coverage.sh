#!/usr/bin/env bash
set -Eeuo pipefail

profile=${1:-coverage.out}
minimum=${COVERAGE_MINIMUM:-35}

[[ -f "$profile" ]] || { echo "coverage profile not found: $profile" >&2; exit 1; }
total=$(go tool cover -func="$profile" | awk '/^total:/ {gsub("%", "", $3); print $3}')
[[ -n "$total" ]] || { echo "coverage total not found in $profile" >&2; exit 1; }

if ! awk -v total="$total" -v minimum="$minimum" 'BEGIN { exit !(total + 0 >= minimum + 0) }'; then
	printf 'coverage %.1f%% is below required %.1f%%\n' "$total" "$minimum" >&2
	exit 1
fi
printf 'coverage %.1f%% meets required %.1f%%\n' "$total" "$minimum"
