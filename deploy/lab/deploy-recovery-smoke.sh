#!/usr/bin/env bash
# Exercise a real Git deployment across a panel SIGKILL during activation.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'deploy recovery smoke must run as root' >&2; exit 1; }
command -v curl >/dev/null || { echo 'deploy recovery smoke requires curl' >&2; exit 77; }
command -v python3 >/dev/null || { echo 'deploy recovery smoke requires python3' >&2; exit 77; }
command -v systemctl >/dev/null || { echo 'deploy recovery smoke requires systemd' >&2; exit 77; }

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${DEPLOY_RECOVERY_SMOKE_SITE:=ci-smoke}"

dropin_dir=/run/systemd/system/stepanel.service.d
dropin="$dropin_dir/recovery-smoke.conf"
mkdir -p "$dropin_dir"
work=$(mktemp -d)
cookies="$work/cookies.txt"
public="/var/www/sites/$DEPLOY_RECOVERY_SMOKE_SITE/public"
journal_root=/var/www/sites/.stepanel-recovery

cleanup() {
  local status=$?
  rm -f -- "$dropin"
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl restart stepanel.service >/dev/null 2>&1 || true
  rm -rf -- "$work"
  if (( status != 0 )); then
    echo "deploy recovery smoke failed (status $status)" >&2
  fi
  return "$status"
}
trap cleanup EXIT

[[ -d $public ]] || { echo "deploy smoke site does not exist: $public" >&2; exit 1; }
printf '%s\n' 'original release survives interrupted deployment' > "$public/deploy-recovery-marker.txt"
/usr/local/sbin/stepanel-sitectl seal "$DEPLOY_RECOVERY_SMOKE_SITE"

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
curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
curl --fail --silent --show-error --max-time 10 -L \
  -b "$cookies" -c "$cookies" \
  --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" \
  --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
  --data-urlencode "totp=$totp" \
  "$PANEL/login" >/dev/null

session=$(awk '$6 == "stepanel_session" {print $7}' "$cookies")
csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
[[ -n $session && -n $csrf ]] || { echo 'deploy recovery login did not issue session and CSRF cookies' >&2; exit 1; }
cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"

# Avoid sharing the administrator TOTP counter with the preceding suspension
# drill when both run near a 30-second boundary.
sleep $((31 - $(date +%s) % 30))
printf '%s\n' '[Service]' 'Environment=STEPANEL_KILL_AT=deploy:activate' > "$dropin"
systemctl daemon-reload
systemctl restart stepanel.service
systemctl is-active --quiet stepanel.service
before=$(systemctl show stepanel.service -p MainPID --value)
[[ "$before" =~ ^[1-9][0-9]*$ ]] || { echo "could not determine panel PID: $before" >&2; exit 1; }

set +e
curl --silent --show-error --max-time 120 \
  -H "Cookie: $cookie_header" \
  -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' \
  --data "$(python3 - "$DEPLOY_RECOVERY_SMOKE_SITE" <<'PY'
import json, sys
print(json.dumps({"site": sys.argv[1], "repository": "https://github.com/octocat/Hello-World.git", "ref": "master"}))
PY
)" \
  "$PANEL/api/sites/git-deploy" >/dev/null
set -e

killed=0
for _ in $(seq 1 120); do
  current=$(systemctl show stepanel.service -p MainPID --value)
  if [[ "$current" =~ ^[1-9][0-9]*$ && "$current" != "$before" ]]; then
    killed=1
    break
  fi
  sleep 1
done
(( killed )) || { echo 'panel PID never changed; deploy activation kill boundary was not observed' >&2; exit 1; }

rm -f -- "$dropin"
systemctl daemon-reload
systemctl restart stepanel.service
for _ in $(seq 1 120); do
  if systemctl is-active --quiet stepanel.service && \
     curl --fail --silent --max-time 2 "$PANEL/readyz" >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent --show-error --max-time 10 "$PANEL/readyz" >/dev/null
grep -Fx 'original release survives interrupted deployment' "$public/deploy-recovery-marker.txt" >/dev/null
if find "$journal_root" -maxdepth 1 -type f -name 'release-activation-*.json' -print -quit 2>/dev/null | grep -q .; then
  echo 'release activation journal remains after startup recovery' >&2
  exit 1
fi

echo "deploy recovery smoke passed (panel was killed during activation and original site content was restored)"
