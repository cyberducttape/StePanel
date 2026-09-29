#!/usr/bin/env bash
# Run an additional real-host recovery matrix at alternate process-kill
# boundaries. This script is intentionally opt-in because it adds several
# minutes to the disposable-host installation smoke.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'recovery matrix must run as root' >&2; exit 1; }
command -v systemctl >/dev/null || { echo 'recovery matrix requires systemd' >&2; exit 77; }

repo=/work/deploy/lab
: "${RECOVERY_MATRIX_PREFIX:=ci-matrix-$(date +%s)}"

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

run_import_recovery
run_backup_recovery
run_suspension_recovery
run_deploy_recovery

echo "recovery matrix passed (prefix $RECOVERY_MATRIX_PREFIX)"
