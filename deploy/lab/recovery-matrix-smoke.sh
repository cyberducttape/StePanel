#!/usr/bin/env bash
# Run an additional real-host recovery matrix at alternate process-kill
# boundaries. This script is intentionally opt-in because it adds several
# minutes to the disposable-host installation smoke.
set -Eeuo pipefail

repo=/work/deploy/lab
: "${RECOVERY_MATRIX_PREFIX:=ci-matrix-$(date +%s)}"
base_prefix=$RECOVERY_MATRIX_PREFIX
run_token=$(printf '%s' "$base_prefix" | sha256sum | cut -c1-8)

full_site() {
  local phase=$1 boundary=$2 prefix stage name
  prefix="m${run_token}-r${matrix_iteration}"
  case "$phase:$boundary" in
    suspend:suspend:before-persist) stage=before ;;
    suspend:suspend:persisted) stage=persist ;;
    *) stage=${boundary##*:} ;;
  esac
  printf -v name '%s-f-%s-%s' "$prefix" "${phase:0:4}" "$stage"
  if (( ${#name} > 32 )); then
    echo "generated recovery-matrix identity exceeds 32 characters: $name" >&2
    return 1
  fi
  printf '%s' "$name"
}

validate_full_site_names() {
  local iteration pair phase boundary name
  local -A seen=()
  local cases=(
    'cpmove cpmove:activate'
    'backup backup:init' 'backup backup:archive' 'backup backup:verify' 'backup backup:commit'
    'restore restore:database' 'restore restore:verify' 'restore restore:extract'
    'restore restore:provision' 'restore restore:provisioned'
    'restore restore:activate' 'restore restore:commit'
    'terminate terminate:init' 'terminate terminate:backup' 'terminate terminate:database'
    'terminate terminate:routes' 'terminate terminate:proxies' 'terminate terminate:tasks'
    'terminate terminate:services' 'terminate terminate:site-state' 'terminate terminate:ownership'
    'suspend suspend:before-persist' 'suspend suspend:persisted'
  )
  for iteration in $(seq 1 "$repeats"); do
    matrix_iteration=$iteration
    for pair in "${cases[@]}"; do
      read -r phase boundary <<<"$pair"
      name=$(full_site "$phase" "$boundary") || return 1
      if [[ -n ${seen[$name]+present} ]]; then
        echo "recovery-matrix identity collision at iteration $iteration: $name" >&2
        return 1
      fi
      seen[$name]=1
    done
  done
}

run_import_recovery() {
  CPMOVE_RECOVERY_SMOKE_SITE="${RECOVERY_MATRIX_PREFIX}-cpmove" \
    CPMOVE_KILL_AT=cpmove:activate \
    bash "$repo/cpmove-recovery-smoke.sh"
}

# The backup drill creates, restores, and terminates its site. Use distinct
# alternate boundaries from the default smoke run so every result is tied to
# one isolated durable job.
run_backup_recovery() {
  CPMOVE_SMOKE_SITE="${RECOVERY_MATRIX_PREFIX}-backup" \
    bash "$repo/cpmove-import-smoke.sh"
  BACKUP_RECOVERY_SMOKE_SITE="${RECOVERY_MATRIX_PREFIX}-backup" \
    BACKUP_KILL_AT=backup:verify \
    RESTORE_KILL_AT=restore:commit \
    TERMINATE_KILL_AT=terminate:routes \
    bash "$repo/backup-recovery-smoke.sh"
}

run_suspension_recovery() {
  SUSPENSION_SMOKE_ACCOUNT="${RECOVERY_MATRIX_PREFIX}-suspend" \
    SUSPENSION_KILL_AT=suspend:persisted \
    bash "$repo/account-suspension-recovery-smoke.sh"
}

run_deploy_recovery() {
  DEPLOY_RECOVERY_SMOKE_SITE=ci-smoke \
    DEPLOY_KILL_AT=deploy:activate \
    bash "$repo/deploy-recovery-smoke.sh"
}

run_matrix_once() {
  local iteration=$1
  matrix_iteration=$iteration
  RECOVERY_MATRIX_PREFIX="${base_prefix}-r${iteration}"
  echo "recovery matrix iteration ${iteration}/${repeats} (prefix ${RECOVERY_MATRIX_PREFIX})"
  run_import_recovery
  run_backup_recovery
  run_suspension_recovery
  run_deploy_recovery

# The default matrix is deliberately bounded for the normal install smoke.
# Set RECOVERY_MATRIX_FULL=1 on an isolated host to execute every supported
# journal boundary. Each run uses a unique site/account prefix, so a failed
# boundary cannot contaminate the next case.
if [[ ${RECOVERY_MATRIX_FULL:-0} == 1 ]]; then
  site=$(full_site cpmove cpmove:activate)
  CPMOVE_RECOVERY_SMOKE_SITE="$site" CPMOVE_KILL_AT=cpmove:activate bash "$repo/cpmove-recovery-smoke.sh"

  for boundary in backup:init backup:archive backup:verify backup:commit; do
    site=$(full_site backup "$boundary")
    CPMOVE_SMOKE_SITE="$site" bash "$repo/cpmove-import-smoke.sh"
    BACKUP_RECOVERY_SMOKE_SITE="$site" BACKUP_KILL_AT="$boundary" \
      RESTORE_KILL_AT=restore:activate TERMINATE_KILL_AT=terminate:site-state \
      DATABASE_RESTORE_RECOVERY=1 \
      bash "$repo/backup-recovery-smoke.sh"
  done
  # Exercise each restore checkpoint on the path that reaches it: verify,
  # extract, activate and commit are file-restore checkpoints
  # (backupRestoreFiles); database is the database-only restore. Each case
  # kills the other restore at a known-good checkpoint, and creates its own
  # backup and database so a killed worker cannot contaminate the next case.
  #
  # restore:provision and restore:provisioned are reached only by the
  # restore-to-staging database path (restoreDatabaseIntoStagingContext).
  # The drill below kills the worker at each checkpoint, recovers the durable
  # job, then verifies the staged files, route, HTTP response and database.
  for boundary in restore:database restore:verify restore:extract restore:activate restore:commit; do
    site=$(full_site restore "$boundary")
    if [[ $boundary == restore:database ]]; then
      file_boundary=restore:activate; database_boundary=$boundary
    else
      file_boundary=$boundary; database_boundary=restore:database
    fi
    CPMOVE_SMOKE_SITE="$site" bash "$repo/cpmove-import-smoke.sh"
    BACKUP_RECOVERY_SMOKE_SITE="$site" BACKUP_KILL_AT=backup:archive \
      RESTORE_KILL_AT="$file_boundary" DATABASE_RESTORE_KILL_AT="$database_boundary" \
      TERMINATE_KILL_AT=terminate:site-state DATABASE_RESTORE_RECOVERY=1 \
      bash "$repo/backup-recovery-smoke.sh"
  done
  # The database restore-to-staging path has two additional durable
  # provisioning boundaries. Exercise them with a real staged site, database,
  # webserver route, and HTTP probe rather than relying on unit fault tests.
  for boundary in restore:provision restore:provisioned; do
    site=$(full_site restore "$boundary")
    CPMOVE_SMOKE_SITE="$site" bash "$repo/cpmove-import-smoke.sh"
    BACKUP_RECOVERY_SMOKE_SITE="$site" BACKUP_KILL_AT=backup:archive \
      RESTORE_KILL_AT=restore:activate DATABASE_RESTORE_KILL_AT=restore:database \
      STAGING_RESTORE_KILL_AT="$boundary" TERMINATE_KILL_AT=terminate:site-state \
      DATABASE_RESTORE_RECOVERY=1 bash "$repo/backup-recovery-smoke.sh"
  done
  for boundary in terminate:init terminate:backup terminate:database terminate:routes terminate:proxies terminate:tasks terminate:services terminate:site-state terminate:ownership; do
    site=$(full_site terminate "$boundary")
    CPMOVE_SMOKE_SITE="$site" bash "$repo/cpmove-import-smoke.sh"
    BACKUP_RECOVERY_SMOKE_SITE="$site" BACKUP_KILL_AT=backup:archive \
      RESTORE_KILL_AT=restore:activate TERMINATE_KILL_AT="$boundary" \
      bash "$repo/backup-recovery-smoke.sh"
  done
  for boundary in suspend:before-persist suspend:persisted; do
    account=$(full_site suspend "$boundary")
    SUSPENSION_SMOKE_ACCOUNT="$account" SUSPENSION_KILL_AT="$boundary" \
      bash "$repo/account-suspension-recovery-smoke.sh"
  done
fi
}

repeats=${RECOVERY_MATRIX_REPEATS:-1}
[[ $repeats =~ ^[1-9][0-9]*$ && $repeats -le 50 ]] || {
  echo 'RECOVERY_MATRIX_REPEATS must be an integer from 1 through 50' >&2
  exit 64
}
if [[ ${RECOVERY_MATRIX_FULL:-0} == 1 ]]; then
  validate_full_site_names
  if [[ ${RECOVERY_MATRIX_VALIDATE_ONLY:-0} == 1 ]]; then
    echo "recovery matrix identity validation passed ($repeats iterations)"
    exit 0
  fi
fi

[[ $EUID -eq 0 ]] || { echo 'recovery matrix must run as root' >&2; exit 1; }
command -v systemctl >/dev/null || { echo 'recovery matrix requires systemd' >&2; exit 77; }

for iteration in $(seq 1 "$repeats"); do
  run_matrix_once "$iteration"
done
echo "recovery matrix passed (${repeats} iteration(s), base prefix $base_prefix)"
