#!/usr/bin/env bash
# Exercise Node application deploy/stop/start/restart through the panel API,
# the typed root broker and stepanel-appctl, checking systemd state, HTTP
# reachability and the persisted manifest state after every step.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'app lifecycle smoke must run as root' >&2; exit 1; }
command -v curl >/dev/null || { echo 'app lifecycle smoke requires curl' >&2; exit 77; }
command -v python3 >/dev/null || { echo 'app lifecycle smoke requires python3' >&2; exit 77; }
command -v systemctl >/dev/null || { echo 'app lifecycle smoke requires systemd' >&2; exit 77; }

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${APP_LIFECYCLE_SMOKE_SITE:=ci-smoke}"
: "${APP_LIFECYCLE_SMOKE_PORT:=38123}"
: "${STEPANEL_DATA_DIR:=/var/lib/ste-panel}"

site=$APP_LIFECYCLE_SMOKE_SITE
port=$APP_LIFECYCLE_SMOKE_PORT
appctl=/usr/local/sbin/stepanel-appctl
unit="stepanel-app-$site.service"
public="/var/www/sites/$site/public"
nvm_dir=/opt/stepanel/.nvm
manifest="$STEPANEL_DATA_DIR/apps/$site.json"
work=$(mktemp -d)
cookies="$work/cookies.txt"
stub_runtime=0

cleanup() {
  local status=$?
  "$appctl" delete "$site" >/dev/null 2>&1 || true
  rm -f -- "$manifest" "$manifest.bak" "$public/package.json" "$public/server.js"
  if (( stub_runtime )); then rm -rf -- "$nvm_dir"; fi
  rm -rf -- "$work"
  if (( status != 0 )); then echo "app lifecycle smoke failed (status $status)" >&2; fi
  return "$status"
}
diagnose() {
  local status=$?
  systemctl status "$unit" --no-pager >&2 || true
  journalctl -u "$unit" --no-pager -n 100 >&2 || true
  cat "$work/response" >&2 2>/dev/null || true
  return "$status"
}
trap cleanup EXIT
trap diagnose ERR

[[ -d $public ]] || { echo "app smoke site does not exist: $public" >&2; exit 1; }

# The application must answer on $PORT. Use the installed Node runtime when the
# lab has one; otherwise provide a stand-in nvm whose npm serves the site root,
# so the lifecycle is exercised even with STEPANEL_INSTALL_NODE=0.
cat > "$public/package.json" <<'EOF'
{"private": true, "scripts": {"start": "node server.js"}}
EOF
cat > "$public/server.js" <<'EOF'
require('http').createServer((req, res) => res.end('ok\n')).listen(Number(process.env.PORT), '127.0.0.1');
EOF
if [[ -f $nvm_dir/nvm.sh ]]; then
  version=$(find "$nvm_dir/versions/node" -mindepth 1 -maxdepth 1 -type d -name 'v*' -printf '%f\n' 2>/dev/null | sort -V | tail -n 1)
  version=${version#v}
  [[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "no installed Node version under $nvm_dir" >&2; exit 1; }
else
  stub_runtime=1
  version=22.0.0
  install -d -m 0755 "$nvm_dir" "$nvm_dir/smoke-bin"
  cat > "$nvm_dir/nvm.sh" <<EOF
nvm() { return 0; }
export PATH="$nvm_dir/smoke-bin:\$PATH"
EOF
  cat > "$nvm_dir/smoke-bin/npm" <<'EOF'
#!/bin/sh
exec python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$PWD"
EOF
  chmod 0644 "$nvm_dir/nvm.sh"
  chmod 0755 "$nvm_dir/smoke-bin/npm"
fi
/usr/local/sbin/stepanel-sitectl seal "$site"

# Earlier lab drills log in with the same TOTP identity. Wait for the next
# counter so replay protection does not reject this login.
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
curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
curl --fail --silent --show-error --max-time 10 -L \
  -b "$cookies" -c "$cookies" \
  --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" \
  --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
  --data-urlencode "totp=$totp" \
  "$PANEL/login" >/dev/null
session=$(awk '$6 == "stepanel_session" {print $7}' "$cookies")
csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
[[ -n $session && -n $csrf ]] || { echo 'app lifecycle login did not issue session and CSRF cookies' >&2; exit 1; }
cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"

# api METHOD PATH [JSON] prints the HTTP status; the body lands in $work/response.
api() {
  local args=(--silent --show-error --max-time 120 -o "$work/response" -w '%{http_code}'
    -H "Cookie: $cookie_header" -H "X-CSRF-Token: $csrf" -X "$1")
  if [[ $# -ge 3 ]]; then args+=(-H 'Content-Type: application/json' --data "$3"); fi
  curl "${args[@]}" "$PANEL$2"
}
expect_status() {
  local want=$1 method=$2 path=$3 got
  shift
  got=$(api "$@")
  [[ $got == "$want" ]] || { echo "$method $path returned HTTP $got, want $want" >&2; exit 1; }
}
manifest_state() {
  expect_status 200 GET /api/apps
  python3 - "$site" "$work/response" <<'PY'
import json, sys
apps = json.load(open(sys.argv[2]))["apps"] or []
print(next((app["state"] for app in apps if app["site"] == sys.argv[1]), "missing"))
PY
}
serving() { curl --fail --silent --max-time 2 "http://127.0.0.1:$port/" >/dev/null; }
wait_serving() {
  for _ in $(seq 1 30); do serving && return 0; sleep 1; done
  echo "application did not answer on port $port" >&2
  return 1
}
# expect_app ACTIVE ENABLED STATE checks systemd, reachability and the manifest.
expect_app() {
  local active enabled state
  active=$(systemctl is-active "$unit" || true)
  enabled=$(systemctl is-enabled "$unit" || true)
  state=$(manifest_state)
  [[ $active == "$1" && $enabled == "$2" && $state == "$3" ]] || {
    echo "unit is $active/$enabled with manifest state $state; want $1/$2/$3" >&2
    exit 1
  }
  if [[ $1 == active ]]; then
    wait_serving
  elif serving; then
    echo "stopped application still answers on port $port" >&2
    exit 1
  fi
}

deploy_body=$(python3 - "$site" "$version" "$port" <<'PY'
import json, sys
print(json.dumps({"site": sys.argv[1], "domain": sys.argv[1] + ".example.test", "node_version": sys.argv[2], "port": int(sys.argv[3])}))
PY
)
expect_status 202 POST /api/apps/deploy "$deploy_body"
expect_app active enabled running

# Stop must also disable the unit so the application stays down after a reboot.
expect_status 202 POST "/api/apps/$site/stop"
expect_app inactive disabled stopped

expect_status 202 POST "/api/apps/$site/start"
expect_app active enabled running

# Restarting a stopped application re-enables it as well.
expect_status 202 POST "/api/apps/$site/stop"
expect_app inactive disabled stopped
expect_status 202 POST "/api/apps/$site/restart"
expect_app active enabled running

# Redeploying a stopped application brings it back enabled and running.
expect_status 202 POST "/api/apps/$site/stop"
expect_app inactive disabled stopped
expect_status 202 POST /api/apps/deploy "$deploy_body"
expect_app active enabled running

# Once the unit is gone the helper must refuse lifecycle actions.
"$appctl" delete "$site"
[[ ! -e /etc/systemd/system/$unit ]] || { echo "application unit survived delete" >&2; exit 1; }
if output=$("$appctl" start "$site" 2>&1); then
  echo 'start succeeded for an application without a unit' >&2
  exit 1
fi
[[ $output == *'application is not configured'* ]] || { echo "unexpected start failure: $output" >&2; exit 1; }

echo 'app lifecycle smoke passed'
