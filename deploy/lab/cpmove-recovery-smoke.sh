#!/usr/bin/env bash
# Exercise a real durable cpmove job across a worker SIGKILL and restart.
set -Eeuo pipefail

on_error() {
  local status=$?
  echo "cpmove recovery smoke failed at line ${BASH_LINENO[0]:-unknown} (status $status)" >&2
  exit "$status"
}
trap on_error ERR

[[ $EUID -eq 0 ]] || { echo 'cpmove recovery smoke must run as root' >&2; exit 1; }
command -v systemctl >/dev/null || { echo 'cpmove recovery smoke requires systemd' >&2; exit 77; }

dropin_dir=/run/systemd/system/stepanel-worker.service.d
dropin="$dropin_dir/recovery-smoke.conf"
mkdir -p "$dropin_dir"
cleanup() {
  rm -f -- "$dropin"
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl restart stepanel-worker.service >/dev/null 2>&1 || true
}
trap cleanup EXIT

printf '%s\n' '[Service]' 'Environment=STEPANEL_KILL_AT=cpmove:activate' > "$dropin"
systemctl daemon-reload
systemctl restart stepanel-worker.service
systemctl is-active --quiet stepanel-worker.service
before=$(systemctl show stepanel-worker.service -p MainPID --value)
[[ "$before" =~ ^[1-9][0-9]*$ ]] || { echo "could not determine worker PID: $before" >&2; exit 1; }

# The preceding ordinary import smoke authenticated with the same lab TOTP
# secret. Wait for a fresh counter so the recovery drill does not mistake a
# boundary-window authentication rejection for an operation failure.
sleep $((31 - $(date +%s) % 30))

CPMOVE_SMOKE_SITE=${CPMOVE_RECOVERY_SMOKE_SITE:-ci-import-recovery} \
  bash /work/deploy/lab/cpmove-import-smoke.sh &
import_pid=$!

killed=0
for _ in $(seq 1 90); do
  current=$(systemctl show stepanel-worker.service -p MainPID --value)
  if [[ "$current" =~ ^[1-9][0-9]*$ && "$current" != "$before" ]]; then
    killed=1
    rm -f -- "$dropin"
    systemctl daemon-reload
    systemctl restart stepanel-worker.service
    break
  fi
  if ! kill -0 "$import_pid" 2>/dev/null; then
    break
  fi
  sleep 1
done

if (( ! killed )); then
  kill "$import_pid" 2>/dev/null || true
  wait "$import_pid" 2>/dev/null || true
  echo 'worker PID never changed; the injected process-kill boundary was not observed' >&2
  exit 1
fi

wait "$import_pid"
test -f "/var/www/sites/${CPMOVE_RECOVERY_SMOKE_SITE:-ci-import-recovery}/public/index.html"
grep -Fx 'stepanel cpmove import smoke' \
  "/var/www/sites/${CPMOVE_RECOVERY_SMOKE_SITE:-ci-import-recovery}/public/index.html" >/dev/null
echo "cpmove recovery smoke passed (worker $before was killed during activation)"
