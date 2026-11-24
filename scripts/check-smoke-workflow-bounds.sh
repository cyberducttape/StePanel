#!/usr/bin/env bash
set -Eeuo pipefail

workflows=(
  .github/workflows/install-smoke.yml
  .github/workflows/upgrade-smoke.yml
)

cohort_workflow=.github/workflows/recovery-cohort.yml
[[ -f $cohort_workflow ]] || { echo "missing recovery cohort workflow: $cohort_workflow" >&2; exit 1; }
for contract in \
  'full_recovery_matrix: true' \
  'recovery_matrix_repeats: 1' \
  'max-parallel: 1' \
  'fail-fast: false' \
  'matrix: ${{ fromJSON(needs.plan.outputs.matrix) }}' \
  'uses: ./.github/workflows/install-smoke.yml' \
  'RESULT == success'; do
  grep -Fq -- "$contract" "$cohort_workflow" || {
    echo "$cohort_workflow is missing recovery cohort contract: $contract" >&2
    exit 1
  }
done
python3 scripts/test-recovery-cohort-plan.py

abrupt_loss_workflow=.github/workflows/abrupt-loss-operations.yml
[[ -f $abrupt_loss_workflow ]] || { echo "missing abrupt-loss workflow: $abrupt_loss_workflow" >&2; exit 1; }
grep -Fq 'PHASES: boot install abrupt-loss-operations enospc db-enospc operator-alerts' "$abrupt_loss_workflow" || {
  echo "$abrupt_loss_workflow must exercise backup/file-restore and database-filesystem ENOSPC" >&2
  exit 1
}
grep -Fq 'VM_DISK_CACHE: none' "$abrupt_loss_workflow" || {
  echo "$abrupt_loss_workflow must bypass the runner page cache for abrupt-loss drills" >&2
  exit 1
}
grep -Fq '${{ env.RUN_DIR }}/environment.txt' "$abrupt_loss_workflow" || {
  echo "$abrupt_loss_workflow must retain the KVM environment manifest with each result artifact" >&2
  exit 1
}
grep -Fq 'writeback|none|directsync|writethrough' deploy/lab/local-kvm-certification.sh || {
  echo 'local KVM certification must keep the supported disk-cache modes explicit' >&2
  exit 1
}

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
      for contract in \
        'goreleaser/goreleaser-action@a4f02ab5355a589de60d917a659ce617ed19b3de' \
        "args: \${{ inputs.release_tag != '' && 'release --skip=publish --clean' || 'release --snapshot --clean' }}" \
        'GORELEASER_CURRENT_TAG: ${{ inputs.release_tag }}' \
        'archives=(dist/*_linux_amd64.tar.gz)' \
        'Stage pinned WordPress recovery fixtures' \
        'fetch-wordpress-smoke-fixtures.sh' \
        '-v "$STEPANEL_WORDPRESS_FIXTURES_DIR:/opt/stepanel-wordpress-fixtures:ro"' \
        '-e STEPANEL_WORDPRESS_FIXTURES_DIR=/opt/stepanel-wordpress-fixtures' \
        'deploy/lab/install-smoke.sh' \
        'web/index.html' \
        '-v "$STEPANEL_RELEASE_TREE:/work"'; do
        grep -Fq -- "$contract" "$workflow" || {
          echo "$workflow must install the exact-source packaged archive in its host smoke (missing: $contract)" >&2
          exit 1
        }
      done
      grep -Fq "github.event_name == 'push' && 'ubuntu-24.04-caddy-mariadb'" "$workflow" || {
        echo "$workflow must smoke-test the recommended Ubuntu/Caddy/MariaDB profile on source pushes" >&2
        exit 1
      }
      grep -Fq 'default: all' "$workflow" || {
        echo "$workflow manual dispatch must retain the complete host matrix by default" >&2
        exit 1
      }
      grep -Fq 'timeout-minutes: 12' "$workflow" || {
        echo "$workflow is missing the disposable-host boot step deadline" >&2
        exit 1
      }
      grep -Fq 'STEPANEL_WORDPRESS_FIXTURES_DIR:-' deploy/lab/wordpress-recovery-proof-smoke.sh || {
        echo 'WordPress recovery proof must accept fixtures staged before the host smoke' >&2
        exit 1
      }
      grep -Fq 'timeout-minutes: ${{ inputs.full_recovery_matrix && 360 || 150 }}' "$workflow" || {
        echo "$workflow must reserve the six-hour hosted job budget for a full recovery iteration" >&2
        exit 1
      }
      grep -Fq "inputs.full_recovery_matrix && '350m' || '140m'" "$workflow" || {
        echo "$workflow must bound one full iteration below the hosted job limit" >&2
        exit 1
      }
      grep -Fq 'max-parallel: 6' "$workflow" || {
        echo "$workflow must cap its OS matrix to six hosted runners" >&2
        exit 1
      }
      grep -Fq 'FULL_RECOVERY_MATRIX: ${{ inputs.full_recovery_matrix || false }}' "$workflow" || {
        echo "$workflow must pass the exhaustive recovery input to the job" >&2
        exit 1
      }
      grep -Fq 'workflow_call:' "$workflow" || {
        echo "$workflow must support the reusable cohort workflow" >&2
        exit 1
      }
      grep -Fq 'RECOVERY_MATRIX_PREFIX="$recovery_prefix"' "$workflow" || {
        echo "$workflow must pass a bounded, unique identity prefix to the host drill" >&2
        exit 1
      }
      grep -Fq 'stepanel-install-diagnostics-${{ inputs.recovery_matrix_shard || '\''default'\'' }}-${{ matrix.name }}' "$workflow" || {
        echo "$workflow must keep diagnostic artifacts unique across shards" >&2
        exit 1
      }
      grep -Fq '[[ $full == true ]]' scripts/validate-recovery-matrix-inputs.sh || {
        echo 'recovery-matrix input validator must reject repeated runs without the full matrix' >&2
        exit 1
      }
      grep -Fq 'run: bash scripts/validate-recovery-matrix-inputs.sh' "$workflow" || {
        echo "$workflow must invoke the tested recovery input validator" >&2
        exit 1
      }
      bash scripts/test-recovery-matrix-inputs.sh
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
