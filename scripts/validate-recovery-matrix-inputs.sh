#!/usr/bin/env bash
set -Eeuo pipefail

repeats=${RECOVERY_MATRIX_REPEATS:-1}
shard=${RECOVERY_MATRIX_SHARD:-}
full=${FULL_RECOVERY_MATRIX:-false}

[[ $repeats =~ ^[12]$ ]] || {
  echo 'recovery_matrix_repeats must be 1 or 2 per shard; split 20-50 repeats across unique shards' >&2
  exit 64
}
[[ $full == true || $full == false ]] || {
  echo 'full_recovery_matrix must be true or false' >&2
  exit 64
}
if [[ -n $shard && ! $shard =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$ ]]; then
  echo 'recovery_matrix_shard must be a 1-40 character alphanumeric identifier' >&2
  exit 64
fi
if (( repeats > 1 )); then
  [[ $full == true ]] || {
    echo 'repeated recovery certification shards require full_recovery_matrix=true' >&2
    exit 64
  }
  [[ -n $shard ]] || {
    echo 'two-iteration recovery shards require a unique recovery_matrix_shard' >&2
    exit 64
  }
fi
