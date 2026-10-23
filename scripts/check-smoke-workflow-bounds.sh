#!/usr/bin/env bash
set -Eeuo pipefail

workflows=(
  .github/workflows/install-smoke.yml
  .github/workflows/upgrade-smoke.yml
)

for workflow in "${workflows[@]}"; do
  [[ -f $workflow ]] || { echo "missing host smoke workflow: $workflow" >&2; exit 1; }
  if grep -En '^[[:space:]]*(docker (info|ps|events|logs|inspect|exec|run|rm)([[:space:]]|$)|run:[[:space:]]+docker (info|ps|events|logs|inspect|exec|run|rm)([[:space:]]|$))' "$workflow"; then
    echo "$workflow contains a direct, unbounded Docker host command" >&2
    exit 1
  fi
  for contract in \
    'docker_bounded() {' \
    'timeout --foreground --kill-after=2s "$deadline" docker "$@"' \
    'docker_diag() { docker_bounded 10s "$@"; }' \
    'docker_diag logs --tail 200' \
    'timeout-minutes: 20' \
    'timeout --foreground --kill-after=5s'; do
    if ! grep -Fq -- "$contract" "$workflow"; then
      echo "$workflow is missing bounded host-smoke contract: $contract" >&2
      exit 1
    fi
  done
  case "$workflow" in
    .github/workflows/install-smoke.yml)
      grep -Fq 'timeout-minutes: 12' "$workflow" || {
        echo "$workflow is missing the disposable-host boot step deadline" >&2
        exit 1
      }
      grep -Fq 'max-parallel: ${{ inputs.recovery_matrix_repeats > 1 && 1 || 6 }}' "$workflow" || {
        echo "$workflow must bound repeated recovery shards to avoid runner starvation" >&2
        exit 1
      }
      grep -Fq 'FULL_RECOVERY_MATRIX: ${{ inputs.full_recovery_matrix }}' "$workflow" || {
        echo "$workflow must pass the exhaustive recovery input to the job" >&2
        exit 1
      }
      grep -Fq '[[ "$FULL_RECOVERY_MATRIX" == "true" ]]' "$workflow" || {
        echo "$workflow must reject repeated recovery runs without the full matrix" >&2
        exit 1
      }
      ;;
    .github/workflows/upgrade-smoke.yml)
      grep -Fq 'timeout-minutes: 45' "$workflow" || {
        echo "$workflow is missing the N-1 upgrade step deadline" >&2
        exit 1
      }
      ;;
  esac
done

echo 'Install and upgrade smoke Docker calls are bounded.'
