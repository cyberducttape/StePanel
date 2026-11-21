#!/usr/bin/env bash
# Reversible site suspension on an installed host (ADR-0008):
#
#   1. A live PHP site with a route and a managed database is suspended
#      through the API. Its route must answer 503 with Retry-After and the
#      generic page (no PHP output), its PHP-FPM pool must be gone behind the
#      root-only marker, its units must be recorded, its database account
#      must refuse logins, and a deployment must be refused with 409.
#   2. The site is resumed. The page, pool, and database login must return
#      and the suspension record must be gone.
#
# Run after install-smoke.sh on a disposable --unsafe-lab host.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'site suspension smoke must run as root' >&2; exit 1; }
grep -q '^STEPANEL_UNSAFE_LAB="\?1"\?$' /etc/ste-panel.env || {
  echo 'refusing to run: /etc/ste-panel.env is not an --unsafe-lab install' >&2
  exit 1
}
: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"

suffix=$(date -u +%H%M%S)
site=ci-suspend-$suffix
domain=$site.localhost
db_name=suspend_$suffix
db_user=suspend_$suffix
db_password=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
webserver=$(sed -n 's/^STEPANEL_WEBSERVER="\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' /etc/ste-panel.env)
db_client=$(command -v mariadb || command -v mysql) || { echo 'no MySQL/MariaDB client' >&2; exit 1; }
# shellcheck source=../integrations/stepanel-site-location.sh
. /usr/local/lib/stepanel/site-location.sh

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
cookies=$work/cookies.txt
fail() { echo "site suspension smoke: $*" >&2; exit 1; }

login() {
  sleep $((31 - $(date +%s) % 30))
  local totp
  totp=$(python3 - "$STEPANEL_ADMIN_TOTP_SECRET" <<'PY'
import base64, hashlib, hmac, struct, sys, time
secret = sys.argv[1].strip().upper()
secret += "=" * ((8 - len(secret) % 8) % 8)
digest = hmac.new(base64.b32decode(secret), struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
offset = digest[-1] & 0x0f
print(f"{(struct.unpack('>I', digest[offset:offset + 4])[0] & 0x7fffffff) % 1000000:06d}")
PY
)
  curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
  curl --fail --silent --show-error --max-time 10 -L -b "$cookies" -c "$cookies" \
    --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
    --data-urlencode "totp=$totp" "$PANEL/login" >/dev/null
  session=$(awk '$6 == "stepanel_session" {print $7}' "$cookies")
  csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
  [[ -n $session && -n $csrf ]] || fail 'login did not issue session and CSRF cookies'
  cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"
}
# api METHOD PATH [BODY] prints the body; the HTTP status is in $work/status.
api() {
  local method=$1 path=$2 body=${3:-} args=()
  [[ -z $body ]] || args=(-H 'Content-Type: application/json' --data "$body")
  curl --silent --show-error --max-time 60 -o "$work/body" -w '%{http_code}' -X "$method" \
    -H "Cookie: $cookie_header" -H "X-CSRF-Token: $csrf" "${args[@]}" "$PANEL$path" > "$work/status"
  cat "$work/body"
}
json() { python3 -c "import json, sys; print(json.dumps(dict(zip(sys.argv[1::2], sys.argv[2::2]))))" "$@"; }
field() { python3 -c 'import json, sys; value = json.loads(sys.argv[1]).get(sys.argv[2], ""); print(value if isinstance(value, str) else json.dumps(value))' "$1" "$2" 2>/dev/null || true; }
require_job() {
  local response=$1 what=$2 job state=''
  job=$(field "$response" job_id)
  [[ -n $job ]] || fail "$what was not queued: $(cat "$work/status") $response"
  for _ in $(seq 1 600); do
    state=$(field "$(api GET "/api/jobs/$job")" state)
    case $state in completed) return 0 ;; failed|dead-letter|cancelled) fail "$what job $job ended $state: $(field "$(api GET "/api/jobs/$job")" error)" ;; esac
    sleep 1
  done
  fail "$what job $job did not finish ($state)"
}
# fetch PATH prints "STATUS RETRY_AFTER" and leaves the body in $work/page.
fetch() {
  local scheme=http port=80
  if [[ $webserver == caddy ]]; then scheme=https port=443; fi
  curl --silent --insecure --max-time 10 -D "$work/headers" -o "$work/page" -w '%{http_code}' \
    --resolve "$domain:$port:127.0.0.1" "$scheme://$domain$1" > "$work/code" || true
  printf '%s %s\n' "$(cat "$work/code")" "$(awk -F': ' 'tolower($1) == "retry-after" { gsub(/\r/, "", $2); print $2 }' "$work/headers")"
}
wait_fetch() {
  local want=$1 path=$2 got=''
  for _ in $(seq 1 30); do
    got=$(fetch "$path")
    [[ ${got%% *} == "$want" ]] && return 0
    sleep 1
  done
  fail "GET $path returned $got, want $want"
}
db_login() { "$db_client" --protocol=socket -u "$db_user" -p"$db_password" -N -e 'SELECT 1' "$db_name" >/dev/null 2>&1; }
pool_files() { compgen -G "/etc/php-fpm.d/stepanel-$site.conf" || compgen -G "/etc/php/*/fpm/pool.d/stepanel-$site.conf" || true; }

login
response=$(api POST /api/sites "$(json site "$site" template php)")
require_job "$response" "site creation"
stepanel_site_location "$site" || fail "could not resolve $site"
owner=$(stat -c %U:%G "$public_root")
printf '<?php echo "SUSPEND_SMOKE_LIVE";\n' > "$public_root/index.php"
chown "$owner" "$public_root/index.php"
STEPANEL_UNSAFE_LAB=1 /usr/local/sbin/stepanel-sitectl seal "$site" >/dev/null
/usr/local/sbin/stepanel-vhostctl apply "$site" "$domain" >/dev/null
api POST /api/databases "$(json name "$db_name" user "$db_user" site "$site" password "$db_password" encoding utf8mb4)" >/dev/null
[[ $(cat "$work/status") == 2?? ]] || fail "database provisioning returned $(cat "$work/status"): $(cat "$work/body")"
wait_fetch 200 /
grep -q SUSPEND_SMOKE_LIVE "$work/page" || fail 'the live site does not serve its PHP page'
db_login || fail 'the managed database account cannot log in before suspension'
[[ -n $(pool_files) ]] || fail 'the live site has no PHP-FPM pool'

# --- Suspend -----------------------------------------------------------------
response=$(api POST /api/sites/suspend "$(json site "$site" reason "suspension smoke")")
[[ $(cat "$work/status") == 202 ]] || fail "suspend returned $(cat "$work/status"): $response"
require_job "$response" "site suspension"
for path in / /index.php; do
  wait_fetch 503 "$path"
  [[ $(fetch "$path") == "503 3600" ]] || fail "GET $path did not send Retry-After: 3600"
  grep -q 'temporarily unavailable' "$work/page" || fail "GET $path did not serve the suspension page"
  ! grep -q SUSPEND_SMOKE_LIVE "$work/page" || fail "GET $path ran PHP while suspended"
done
[[ -f /etc/stepanel/suspended/$site && -z $(pool_files) ]] || fail 'the PHP-FPM pool is not disabled behind the suspension marker'
[[ -f /etc/stepanel/suspended/$site.units ]] || fail 'the site units were not recorded for resume'
! db_login || fail 'the managed database account still logs in while suspended'
api POST /api/sites/git-deploy "$(json site "$site" repository https://github.com/octocat/Hello-World.git ref master)" >/dev/null
[[ $(cat "$work/status") == 409 ]] && grep -q suspended "$work/body" || fail "a deployment to the suspended site returned $(cat "$work/status"): $(cat "$work/body")"
listed=$(api GET /api/sites/suspensions)
python3 -c 'import json, sys; s = [x for x in json.loads(sys.argv[1])["suspensions"] if x["site"] == sys.argv[2]]; sys.exit(0 if s and s[0]["state"] == "suspended" and s[0]["reason"] == "suspension smoke" else 1)' "$listed" "$site" \
  || fail "the suspension list does not show $site as suspended: $listed"
if [[ $webserver == apache ]]; then
  compgen -G "/etc/*/stepanel-sites/.stepanel-suspended-$site.conf" >/dev/null || compgen -G "/etc/httpd/conf.d/stepanel-sites/.stepanel-suspended-$site.conf" >/dev/null \
    || fail 'the Apache suspension include was not written'
fi
echo "suspended: $site serves 503 with Retry-After, runs no PHP, has no pool, refuses database logins and deployments"

# --- Resume ------------------------------------------------------------------
response=$(api POST /api/sites/resume "$(json site "$site" reason "suspension smoke done")")
[[ $(cat "$work/status") == 202 ]] || fail "resume returned $(cat "$work/status"): $response"
require_job "$response" "site resume"
wait_fetch 200 /
grep -q SUSPEND_SMOKE_LIVE "$work/page" || fail 'the resumed site does not serve its PHP page'
[[ ! -e /etc/stepanel/suspended/$site && ! -e /etc/stepanel/suspended/$site.units && -n $(pool_files) ]] || fail 'resume did not restore the PHP-FPM pool and clear the suspension state'
db_login || fail 'the managed database account cannot log in after resume'
listed=$(api GET /api/sites/suspensions)
python3 -c 'import json, sys; sys.exit(1 if any(x["site"] == sys.argv[2] for x in json.loads(sys.argv[1])["suspensions"]) else 0)' "$listed" "$site" \
  || fail "the suspension record of $site remains after resume"
echo "site suspension smoke passed: suspend served 503 and stopped PHP, units, and database logins; resume restored them"
