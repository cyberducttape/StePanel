#!/usr/bin/env bash
set -Eeuo pipefail

validator=scripts/validate-recovery-matrix-inputs.sh

expect_accept() {
  local repeats=$1 full=$2 shard=$3 label=$4
  if ! env RECOVERY_MATRIX_REPEATS="$repeats" FULL_RECOVERY_MATRIX="$full" RECOVERY_MATRIX_SHARD="$shard" bash "$validator" >/dev/null 2>&1; then
    echo "recovery-matrix validator rejected valid input: $label" >&2
    return 1
  fi
}

if ! env -u RECOVERY_MATRIX_REPEATS -u FULL_RECOVERY_MATRIX -u RECOVERY_MATRIX_SHARD bash "$validator" >/dev/null 2>&1; then
  echo 'recovery-matrix validator rejected input-less scheduled defaults' >&2
  exit 1
fi

expect_reject() {
  local repeats=$1 full=$2 shard=$3 label=$4 status
  if env RECOVERY_MATRIX_REPEATS="$repeats" FULL_RECOVERY_MATRIX="$full" RECOVERY_MATRIX_SHARD="$shard" bash "$validator" >/dev/null 2>&1; then
    echo "recovery-matrix validator accepted invalid input: $label" >&2
    return 1
  else
    status=$?
  fi
  if (( status != 64 )); then
    echo "recovery-matrix validator returned $status for $label; want 64" >&2
    return 1
  fi
}

expect_accept 1 false '' 'single bounded smoke run'
expect_accept 1 true 'production-20261023-a' 'single exhaustive recovery shard'
expect_reject 1 true '' 'full recovery run without shard identity'
expect_reject 2 true 'production-20261023-b' 'two-iteration shard exceeds hosted job budget'
expect_reject 2 false 'production-20261023-c' 'repeated bounded smoke run'
expect_reject 1 true '../unsafe' 'malformed shard identity'
expect_reject 3 true 'production-20261023-d' 'too many repeats in one shard'
expect_reject 0 false '' 'zero repeats'
expect_reject 1 maybe '' 'invalid full-matrix boolean'

echo 'Recovery-matrix input contract tests passed.'
