#!/usr/bin/env bash
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'task safeguards smoke must run as root' >&2; exit 1; }
command -v systemd-analyze >/dev/null
command -v python3 >/dev/null

site=${1:-ci-smoke}
appctl=/usr/local/sbin/stepanel-appctl
name=smoke-task
unit="stepanel-task-$site-$name.service"
timer="stepanel-task-$site-$name.timer"
cleanup() { "$appctl" task-delete "$site" "$name" >/dev/null 2>&1 || true; }
diagnose() {
  local status=$?
  echo "scheduled task smoke failed (exit $status)" >&2
  systemctl status "$unit" "$timer" --no-pager >&2 || true
  journalctl -u "$unit" -u "$timer" --no-pager -n 100 >&2 || true
  return "$status"
}
trap cleanup EXIT
trap diagnose ERR

apply_task() {
  "$appctl" task-apply "$site" "$name" shell '*-*-* 00:00:00' 0 30 "$1" 60 "${2:-run_once}" 100 128 64 ''
}

apply_task dHJ1ZQ==
systemd-analyze verify "/etc/systemd/system/$unit" "/etc/systemd/system/$timer"
grep -Fxq 'Persistent=true' "/etc/systemd/system/$timer"
apply_task dHJ1ZQ== skip
grep -Fxq 'Persistent=false' "/etc/systemd/system/$timer"
apply_task dHJ1ZQ==

if "$appctl" task-validate "$site" '*-*-* *:00/1:00' 120 >/dev/null 2>&1; then
  echo 'task schedule below the minimum interval was accepted' >&2
  exit 1
fi

systemctl start "$unit"
history=$("$appctl" task-history "$site" "$name")
[[ $(python3 -c 'import json,sys; print(json.load(sys.stdin)["executions"][0]["result"])' <<< "$history") == success ]]

apply_task ZmFsc2U=
for _ in $(seq 1 10); do
  systemctl reset-failed "$unit" || true
  systemctl start "$unit" >/dev/null 2>&1 || true
done
history=$("$appctl" task-history "$site" "$name")
[[ $(python3 -c 'import json,sys; print(json.load(sys.stdin)["consecutive_failures"])' <<< "$history") == 10 ]]
[[ $(python3 -c 'import json,sys; print(json.load(sys.stdin)["auto_disabled_at"] > 0)' <<< "$history") == True ]]
[[ $(python3 -c 'import json,sys; print(json.load(sys.stdin)["enabled"])' <<< "$history") == False ]]

apply_task dHJ1ZQ==
for _ in $(seq 1 22); do systemctl reset-failed "$unit" || true; systemctl start "$unit"; done
history=$("$appctl" task-history "$site" "$name")
[[ $(python3 -c 'import json,sys; print(len(json.load(sys.stdin)["executions"]))' <<< "$history") == 20 ]]
[[ $(python3 -c 'import json,sys; print(json.load(sys.stdin)["consecutive_failures"])' <<< "$history") == 0 ]]

echo 'scheduled task safeguards smoke passed'
