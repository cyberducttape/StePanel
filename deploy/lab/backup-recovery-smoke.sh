#!/usr/bin/env bash
# Exercise a real durable backup job across a worker SIGKILL and restart.
set -Eeuo pipefail

on_error() {
  local status=$?
  echo "backup recovery smoke failed at line ${BASH_LINENO[0]:-unknown} (status $status)" >&2
  exit "$status"
}
trap on_error ERR

[[ $EUID -eq 0 ]] || { echo 'backup recovery smoke must run as root' >&2; exit 1; }
command -v curl >/dev/null || { echo 'backup recovery smoke requires curl' >&2; exit 77; }
command -v python3 >/dev/null || { echo 'backup recovery smoke requires python3' >&2; exit 77; }
command -v systemctl >/dev/null || { echo 'backup recovery smoke requires systemd' >&2; exit 77; }

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${BACKUP_RECOVERY_SMOKE_SITE:=ci-import-recovery}"

dropin_dir=/run/systemd/system/stepanel-worker.service.d
dropin="$dropin_dir/recovery-smoke.conf"
work=''
mkdir -p "$dropin_dir"
cleanup() {
  rm -f -- "$dropin"
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl restart stepanel-worker.service >/dev/null 2>&1 || true
  if [[ -n $work ]]; then
    rm -rf -- "$work"
  fi
}
trap cleanup EXIT

# The cpmove recovery smoke immediately before this test already created the
# disposable site. Wait for a fresh TOTP counter so the two drills cannot
# share a boundary-window code.
sleep $((31 - $(date +%s) % 30))

totp=$(python3 - "$STEPANEL_ADMIN_TOTP_SECRET" <<'PY'
import base64, hashlib, hmac, struct, sys, time

secret = sys.argv[1].strip().upper()
secret += "=" * ((8 - len(secret) % 8) % 8)
key = base64.b32decode(secret)
counter = int(time.time()) // 30
digest = hmac.new(key, struct.pack(">Q", counter), hashlib.sha1).digest()
offset = digest[-1] & 0x0f
code = (struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7fffffff) % 1000000
print(f"{code:06d}")
PY
)

work=$(mktemp -d)
cookies="$work/cookies.txt"
curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
curl --fail --silent --show-error --max-time 10 -L \
  -b "$cookies" -c "$cookies" \
  --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" \
  --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
  --data-urlencode "totp=$totp" \
  "$PANEL/login" >/dev/null

session=$(awk '$6 == "stepanel_session" {print $7}' "$cookies")
csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
[[ -n $session && -n $csrf ]] || { echo 'backup recovery login did not issue session and CSRF cookies' >&2; exit 1; }
cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"

printf '%s\n' '[Service]' 'Environment=STEPANEL_KILL_AT=backup:archive' > "$dropin"
systemctl daemon-reload
systemctl restart stepanel-worker.service
systemctl is-active --quiet stepanel-worker.service
before=$(systemctl show stepanel-worker.service -p MainPID --value)
[[ "$before" =~ ^[1-9][0-9]*$ ]] || { echo "could not determine worker PID: $before" >&2; exit 1; }

response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" \
  -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' \
  --data "{\"site\":\"$BACKUP_RECOVERY_SMOKE_SITE\",\"include_databases\":false}" \
  "$PANEL/api/backups")
job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
[[ -n $job_id ]] || { echo "backup request did not return a durable job: $response" >&2; exit 1; }

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
  sleep 1
done

if (( ! killed )); then
  echo 'worker PID never changed; the injected backup process-kill boundary was not observed' >&2
  exit 1
fi

state=''
status=''
for _ in $(seq 1 240); do
  status=$(curl --fail --silent --show-error --max-time 10 \
    -H "Cookie: $cookie_header" "$PANEL/api/jobs/$job_id")
  state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
  case "$state" in
    completed) break ;;
    failed|dead-letter|cancelled)
      echo "backup recovery job $job_id ended in $state: $status" >&2
      exit 1
      ;;
  esac
  sleep 1
done
[[ "$state" == completed ]] || { echo "backup recovery job $job_id did not complete: ${status:-}" >&2; exit 1; }

backups=$(curl --fail --silent --show-error --max-time 10 \
  -H "Cookie: $cookie_header" "$PANEL/api/backups?site=$BACKUP_RECOVERY_SMOKE_SITE")
printf '%s' "$backups" | grep -Fq "\"site\":\"$BACKUP_RECOVERY_SMOKE_SITE\""
echo "backup recovery smoke passed (worker $before was killed during archive finalization)"

# Reuse the verified artifact for a durable file restore and kill the worker
# after activation has begun. Recovery must replay the job without leaving a
# partially published site.
backup_name=$(printf '%s' "$backups" | python3 -c 'import json, sys; items=json.load(sys.stdin)["backups"]; print(items[0]["path"].rstrip("/").rsplit("/", 1)[-1] if items else "")')
[[ -n $backup_name ]] || { echo 'backup listing did not expose a restore artifact' >&2; exit 1; }

printf '%s\n' '[Service]' 'Environment=STEPANEL_KILL_AT=restore:activate' > "$dropin"
systemctl daemon-reload
systemctl restart stepanel-worker.service
systemctl is-active --quiet stepanel-worker.service
before=$(systemctl show stepanel-worker.service -p MainPID --value)

response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" \
  -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' \
  --data "$(python3 - "$backup_name" "$BACKUP_RECOVERY_SMOKE_SITE" <<'PY'
import json, sys
print(json.dumps({"site": sys.argv[2], "backup": sys.argv[1], "confirm": "RESTORE_FILES"}))
PY
)" \
  "$PANEL/api/backups/restore-files")
restore_job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
[[ -n $restore_job_id ]] || { echo "restore request did not return a durable job: $response" >&2; exit 1; }

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
  sleep 1
done
(( killed )) || { echo 'worker PID never changed; injected restore process-kill boundary was not observed' >&2; exit 1; }

state=''
status=''
for _ in $(seq 1 240); do
  status=$(curl --fail --silent --show-error --max-time 10 \
    -H "Cookie: $cookie_header" "$PANEL/api/jobs/$restore_job_id")
  state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
  case "$state" in
    completed) break ;;
    failed|dead-letter|cancelled)
      echo "restore recovery job $restore_job_id ended in $state: $status" >&2
      exit 1
      ;;
  esac
  sleep 1
done
[[ "$state" == completed ]] || { echo "restore recovery job $restore_job_id did not complete: ${status:-}" >&2; exit 1; }
echo "restore recovery smoke passed (worker $before was killed during restore activation)"

# Finally exercise the irreversible lifecycle journal. The worker dies after
# the site-state step starts; the restarted worker must roll the journal
# forward and leave the site absent, with the verified backup retained.
printf '%s\n' '[Service]' 'Environment=STEPANEL_KILL_AT=terminate:site-state' > "$dropin"
systemctl daemon-reload
systemctl restart stepanel-worker.service
systemctl is-active --quiet stepanel-worker.service
before=$(systemctl show stepanel-worker.service -p MainPID --value)

response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" \
  -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' \
  --data "$(python3 - "$BACKUP_RECOVERY_SMOKE_SITE" <<'PY'
import json, sys
site = sys.argv[1]
print(json.dumps({"site": site, "confirmation": "DELETE " + site}))
PY
)" \
  "$PANEL/api/sites/terminate")
terminate_job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
[[ -n $terminate_job_id ]] || { echo "termination request did not return a durable job: $response" >&2; exit 1; }

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
  sleep 1
done
(( killed )) || { echo 'worker PID never changed; injected termination process-kill boundary was not observed' >&2; exit 1; }

state=''
status=''
for _ in $(seq 1 240); do
  status=$(curl --fail --silent --show-error --max-time 10 \
    -H "Cookie: $cookie_header" "$PANEL/api/jobs/$terminate_job_id")
  state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
  case "$state" in
    completed) break ;;
    failed|dead-letter|cancelled)
      echo "termination recovery job $terminate_job_id ended in $state: $status" >&2
      exit 1
      ;;
  esac
  sleep 1
done
[[ "$state" == completed ]] || { echo "termination recovery job $terminate_job_id did not complete: ${status:-}" >&2; exit 1; }
[[ ! -d "/var/www/sites/$BACKUP_RECOVERY_SMOKE_SITE" ]] || { echo 'terminated site directory still exists' >&2; exit 1; }
echo "termination recovery smoke passed (worker $before was killed during site-state removal)"
