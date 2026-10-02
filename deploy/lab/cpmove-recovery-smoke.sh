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

: "${CPMOVE_KILL_AT:=cpmove:activate}"

wait_for_panel_ready() {
  for _ in $(seq 1 120); do
    if systemctl is-active --quiet stepanel.service && \
       curl --fail --silent --max-time 2 http://127.0.0.1:8090/readyz >/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo 'panel did not become ready after worker restart' >&2
  systemctl status stepanel.service stepanel-worker.service --no-pager >&2 || true
  return 1
}

dropin_dir=/run/systemd/system/stepanel-worker.service.d
dropin="$dropin_dir/recovery-smoke.conf"
mkdir -p "$dropin_dir"
cleanup() {
  systemctl stop stepanel-worker.service >/dev/null 2>&1 || true
  rm -f -- "$dropin"
  systemctl daemon-reload >/dev/null 2>&1 || true
  if timeout --foreground 30s systemctl start stepanel-worker.service >/dev/null 2>&1; then
    wait_for_panel_ready || true
  fi
}
trap cleanup EXIT

printf '%s\n' '[Service]' "Environment=STEPANEL_KILL_AT=$CPMOVE_KILL_AT" > "$dropin"
systemctl daemon-reload
systemctl restart stepanel-worker.service
systemctl is-active --quiet stepanel-worker.service
wait_for_panel_ready
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
    # Stop systemd's replacement before clearing the kill injection; otherwise
    # Restart=on-failure can kill the replacement a second time.
    systemctl stop stepanel-worker.service || true
    rm -f -- "$dropin"
    systemctl daemon-reload
    systemctl start stepanel-worker.service
    wait_for_panel_ready
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
echo "cpmove recovery smoke passed (worker $before was killed at $CPMOVE_KILL_AT)"
