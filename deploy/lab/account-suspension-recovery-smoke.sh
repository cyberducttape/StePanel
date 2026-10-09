#!/usr/bin/env bash
# Exercise durable account suspension across a panel SIGKILL and restart.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'account suspension recovery smoke must run as root' >&2; exit 1; }
command -v curl >/dev/null || { echo 'account suspension recovery smoke requires curl' >&2; exit 77; }
command -v python3 >/dev/null || { echo 'account suspension recovery smoke requires python3' >&2; exit 77; }
command -v systemctl >/dev/null || { echo 'account suspension recovery smoke requires systemd' >&2; exit 77; }

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${SUSPENSION_SMOKE_ACCOUNT:=ci-suspension-recovery}"
: "${SUSPENSION_KILL_AT:=suspend:persisted}"

dropin_dir=/run/systemd/system/stepanel.service.d
dropin="$dropin_dir/recovery-smoke.conf"
work=$(mktemp -d)
cookies="$work/cookies.txt"
mkdir -p "$dropin_dir"

cleanup() {
  local status=$?
  systemctl stop stepanel.service >/dev/null 2>&1 || true
  rm -f -- "$dropin"
  systemctl daemon-reload >/dev/null 2>&1 || true
  timeout --foreground 30s systemctl start stepanel.service >/dev/null 2>&1 || true
  rm -rf -- "$work"
  if (( status != 0 )); then
    echo "account suspension recovery smoke failed (status $status)" >&2
  fi
  return "$status"
}
trap cleanup EXIT

login() {
  local totp session csrf
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
  : > "$cookies"
  curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
  curl --fail --silent --show-error --max-time 10 -L \
    -b "$cookies" -c "$cookies" \
    --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" \
    --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
    --data-urlencode "totp=$totp" \
    "$PANEL/login" >/dev/null
  session=$(awk '$6 == "stepanel_session" {print $7}' "$cookies")
  csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
  [[ -n $session && -n $csrf ]] || { echo 'panel login did not issue session and CSRF cookies' >&2; return 1; }
  COOKIE_HEADER="stepanel_session=$session; stepanel_csrf=$csrf"
  CSRF_TOKEN=$csrf
}

# The preceding backup recovery drill authenticates with the same disposable
# TOTP identity. Wait for the next counter so replay protection cannot make
# this independent suspension drill fail nondeterministically.
sleep $((31 - $(date +%s) % 30))
login

curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $COOKIE_HEADER" \
  -H "X-CSRF-Token: $CSRF_TOKEN" \
  -H 'Content-Type: application/json' \
  --data "$(python3 - "$SUSPENSION_SMOKE_ACCOUNT" <<'PY'
import json, sys
print(json.dumps({
    "username": sys.argv[1],
    "password": "ci-suspension-password-0123456789",
    "totp_secret": "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP",
    "plan": "starter",
    "sites": [],
}))
PY
)" \
  "$PANEL/api/accounts" >/dev/null

printf '%s\n' '[Service]' "Environment=STEPANEL_KILL_AT=$SUSPENSION_KILL_AT" > "$dropin"
systemctl daemon-reload
systemctl restart stepanel.service
for _ in $(seq 1 60); do
  if systemctl is-active --quiet stepanel.service && \
     curl --fail --silent --max-time 2 "$PANEL/readyz" >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent --show-error --max-time 10 "$PANEL/readyz" >/dev/null
before=$(systemctl show stepanel.service -p MainPID --value)
[[ "$before" =~ ^[1-9][0-9]*$ ]] || { echo "could not determine panel PID: $before" >&2; exit 1; }

set +e
curl --silent --show-error --max-time 15 \
  -H "Cookie: $COOKIE_HEADER" \
  -H "X-CSRF-Token: $CSRF_TOKEN" \
  -H 'Content-Type: application/json' \
  --data "$(python3 - "$SUSPENSION_SMOKE_ACCOUNT" <<'PY'
import json, sys
print(json.dumps({"username": sys.argv[1], "reason": "process-kill recovery smoke", "permanent": True}))
PY
)" \
  "$PANEL/api/admin/suspend" >/dev/null
set -e

killed=0
for _ in $(seq 1 60); do
  current=$(systemctl show stepanel.service -p MainPID --value)
  if [[ "$current" =~ ^[1-9][0-9]*$ && "$current" != "$before" ]]; then
    killed=1
    break
  fi
  sleep 1
done
(( killed )) || { echo 'panel PID never changed; suspension kill boundary was not observed' >&2; exit 1; }

systemctl stop stepanel.service || true
rm -f -- "$dropin"
systemctl daemon-reload
systemctl start stepanel.service
for _ in $(seq 1 60); do
  if systemctl is-active --quiet stepanel.service && \
     curl --fail --silent --max-time 2 "$PANEL/readyz" >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent --show-error --max-time 10 "$PANEL/readyz" >/dev/null
accounts=$(curl --fail --silent --show-error --max-time 10 \
  -H "Cookie: $COOKIE_HEADER" "$PANEL/api/accounts")
# A kill after the suspension is persisted must leave the account suspended.
# A kill before it is persisted happens before the request is acknowledged
# (the administrator sees a failed request and retries); the account must be
# unchanged and consistent, never half-suspended.
want_suspended=False
[[ $SUSPENSION_KILL_AT == suspend:persisted ]] && want_suspended=True
SUSPENSION_SMOKE_ACCOUNT="$SUSPENSION_SMOKE_ACCOUNT" ACCOUNTS_JSON="$accounts" WANT_SUSPENDED="$want_suspended" python3 <<'PY'
import json, os, sys
target = os.environ["SUSPENSION_SMOKE_ACCOUNT"]
want = os.environ["WANT_SUSPENDED"] == "True"
accounts = json.loads(os.environ["ACCOUNTS_JSON"])["accounts"]
match = next((account for account in accounts if account.get("username") == target), None)
if not match or match.get("suspended") is not want:
    print(f"recovered account state = {match!r}; want suspended={want}", file=sys.stderr)
    sys.exit(1)
PY

echo "account suspension recovery smoke passed (panel process was killed at $SUSPENSION_KILL_AT)"
