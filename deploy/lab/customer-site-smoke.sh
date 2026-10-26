#!/usr/bin/env bash
# Exercise the customer path of hosting accounts (ADR-0007) on an installed
# host with user quotas on /home: an administrator provisions a customer, the
# customer signs in with MFA and creates a site, which must land in the
# customer's own account at /home/USER/public_html, owned by the account
# user, under the plan's account quota. The customer then lists the account
# and writes a file through the file manager, which runs as the account user.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'customer site smoke must run as root' >&2; exit 1; }
command -v curl >/dev/null || { echo 'customer site smoke requires curl' >&2; exit 77; }
command -v python3 >/dev/null || { echo 'customer site smoke requires python3' >&2; exit 77; }
command -v quota >/dev/null || { echo 'customer site smoke requires quota tools' >&2; exit 77; }

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
customer=cismokecust
customer_password='ci-customer-only-password-1234'
customer_secret=KRSXG5CTMVRXEZLUKRSXG5CTMVRXEZLU
site=ci-cust-main
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
fail() { echo "customer site smoke: $*" >&2; cat "$work/response" >&2 2>/dev/null || true; exit 1; }

totp() {
  python3 - "$1" <<'PY'
import base64, hashlib, hmac, struct, sys, time
secret = sys.argv[1].strip().upper()
secret += "=" * ((8 - len(secret) % 8) % 8)
key = base64.b32decode(secret)
digest = hmac.new(key, struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
offset = digest[-1] & 0x0f
print(f"{(struct.unpack('>I', digest[offset:offset + 4])[0] & 0x7fffffff) % 1000000:06d}")
PY
}

# login USER PASSWORD SECRET JAR prints "session; csrf" cookie header and csrf.
login() {
  local jar=$4
  curl --fail --silent --show-error --max-time 10 -c "$jar" "$PANEL/login" >/dev/null
  curl --fail --silent --show-error --max-time 10 -L -b "$jar" -c "$jar" \
    --data-urlencode "username=$1" --data-urlencode "password=$2" \
    --data-urlencode "totp=$(totp "$3")" "$PANEL/login" >/dev/null
  awk '$6 == "stepanel_session" {s=$7} $6 == "stepanel_csrf" {c=$7} END {if (s && c) print "stepanel_session=" s "; stepanel_csrf=" c "\t" c}' "$jar"
}

# api COOKIE CSRF METHOD PATH [BODY] prints the status; the body is in $work/response.
api() {
  local args=(--silent --show-error --max-time 60 -o "$work/response" -w '%{http_code}' -H "Cookie: $1" -H "X-CSRF-Token: $2" -X "$3")
  if [[ $# -ge 5 ]]; then args+=(-H 'Content-Type: application/json' --data "$5"); fi
  curl "${args[@]}" "$PANEL$4"
}
# json KEY... prints a value from the last response, indexing lists by number.
json() {
  python3 - "$work/response" "$@" <<'PY'
import json, sys
value = json.load(open(sys.argv[1]))
for key in sys.argv[2:]:
    value = value[int(key)] if isinstance(value, list) else value[key]
print(value)
PY
}

# Earlier drills log in as the administrator; wait for an unused TOTP counter.
sleep $((31 - $(date +%s) % 30))
IFS=$'\t' read -r admin_cookie admin_csrf < <(login "$STEPANEL_ADMIN_USERNAME" "$STEPANEL_ADMIN_PASSWORD" "$STEPANEL_ADMIN_TOTP_SECRET" "$work/admin.jar")
[[ -n ${admin_cookie:-} ]] || fail 'administrator login did not issue session cookies'

status=$(api "$admin_cookie" "$admin_csrf" POST /api/accounts "{\"username\":\"$customer\",\"password\":\"$customer_password\",\"totp_secret\":\"$customer_secret\",\"plan\":\"starter\",\"sites\":[]}")
[[ $status == 201 ]] || fail "customer provisioning returned HTTP $status"

IFS=$'\t' read -r cookie csrf < <(login "$customer" "$customer_password" "$customer_secret" "$work/customer.jar")
[[ -n ${cookie:-} ]] || fail 'customer login did not issue session cookies'

status=$(api "$cookie" "$csrf" POST /api/sites "{\"site\":\"$site\",\"template\":\"php\"}")
[[ $status == 202 ]] || fail "customer site creation returned HTTP $status"
job=$(json job_id)
state=
for _ in $(seq 1 180); do
  status=$(api "$cookie" "$csrf" GET "/api/jobs/$job")
  [[ $status == 200 ]] && state=$(json state)
  case $state in
    completed) break ;;
    failed|dead-letter|cancelled) fail "customer site creation ended in $state" ;;
  esac
  sleep 1
done
[[ $state == completed ]] || fail 'customer site creation did not complete'

# The site is the main site of the customer's own account.
location="/etc/stepanel/sites/$site/location"
[[ -f $location ]] || fail 'customer site has no root-owned location record'
[[ $(sed -n 's/^user=//p' "$location") == "$customer" && $(sed -n 's/^directory=//p' "$location") == public_html ]] || fail "customer site is placed elsewhere: $(tr '\n' ' ' < "$location")"
docroot="/home/$customer/public_html"
[[ -d $docroot && $(stat -c %U "$docroot") == "$customer" ]] || fail "$docroot is missing or not owned by $customer"
[[ ! -e /var/www/sites/$site ]] || fail 'customer site was also created in the legacy layout'

# The starter plan's disk limit is the account's user quota on /home.
want_blocks=$(( 10240 * 1024 ))
quota -w -u "$customer" 2>/dev/null | grep -q " $want_blocks " || fail "account quota is not the starter plan limit: $(quota -w -u "$customer" 2>&1)"

status=$(api "$cookie" "$csrf" GET /api/account/hosting)
[[ $status == 200 && $(json unix_user) == "$customer" && $(json sites 0 doc_root) == "$docroot" ]] || fail 'account summary does not report the placed site'

status=$(curl --silent --show-error --max-time 30 -o "$work/response" -w '%{http_code}' -X PUT \
  -H "Cookie: $cookie" -H "X-CSRF-Token: $csrf" --data-binary 'customer smoke' \
  "$PANEL/api/sites/files/$site/content?path=smoke.txt")
if [[ $status != 200 ]]; then
  echo 'customer file-manager broker diagnostics:' >&2
  getent passwd "$customer" >&2 || true
  id "$customer" >&2 || true
  broker_pid=$(systemctl show --property=MainPID --value stepanel-root-broker.service 2>/dev/null || true)
  systemctl show stepanel-root-broker.service \
    --property=User --property=Group --property=CapabilityBoundingSet \
    --property=NoNewPrivileges --property=RestrictNamespaces \
    --property=SystemCallFilter >&2 || true
  if [[ $broker_pid =~ ^[1-9][0-9]*$ && -r /proc/$broker_pid/status ]]; then
    grep -E '^(Uid|Gid|Cap(Inh|Prm|Eff|Bnd)|NoNewPrivs|Seccomp):' \
      "/proc/$broker_pid/status" >&2 || true
  fi
  echo 'direct root runuser probe:' >&2
  runuser -u "$customer" -- id >&2 || true
  fail "file manager write returned HTTP $status"
fi
[[ $(cat "$docroot/smoke.txt") == 'customer smoke' && $(stat -c %U "$docroot/smoke.txt") == "$customer" ]] || fail 'file manager write did not run as the account user'

echo "customer site smoke passed (site $site in /home/$customer, starter quota enforced)"
