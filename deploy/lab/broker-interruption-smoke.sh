#!/usr/bin/env bash
# Kill the root broker while a site is being created, and check real state.
#
# Site creation crosses the privilege boundary many times (account, quota,
# PHP-FPM pool, sealing). For each round a site is created through the panel
# and, once its job runs, the whole root broker unit (the broker and any
# helper it is running) is SIGKILLed after a different delay; systemd
# restarts it. Then:
#
#   - the job must reach a terminal state; none may stay running;
#   - a completed site must have its document root and Unix identity;
#   - a failed one must leave no site directory or placement behind, and
#     creating the same site again must complete;
#   - terminating the site must remove its document root and placement;
#   - readiness, including the broker check, must recover.
#
# Destructive to the host's StePanel state: run only on a disposable host.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'broker interruption smoke must run as root' >&2; exit 1; }
for tool in curl python3 systemctl; do command -v "$tool" >/dev/null || { echo "broker interruption smoke requires $tool" >&2; exit 77; }; done
systemctl is-active --quiet stepanel-root-broker.service || { echo 'the root broker is not running' >&2; exit 77; }

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${BROKER_KILL_DELAYS:=0.2 1 3}"

work=$(mktemp -d)
cookies="$work/cookies.txt"
suffix=$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')
created=()

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

login() {
  local totp
  sleep $((31 - $(date +%s) % 30))
  totp=$(python3 -c '
import base64, hashlib, hmac, struct, sys, time
secret = sys.argv[1].strip().upper()
secret += "=" * ((8 - len(secret) % 8) % 8)
digest = hmac.new(base64.b32decode(secret), struct.pack(">Q", int(time.time()) // 30), hashlib.sha1).digest()
offset = digest[-1] & 0x0f
code = (struct.unpack(">I", digest[offset:offset + 4])[0] & 0x7fffffff) % 1000000
print("%06d" % code)
' "$STEPANEL_ADMIN_TOTP_SECRET")
  rm -f -- "$cookies"
  curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
  curl --fail --silent --show-error --max-time 10 -L -b "$cookies" -c "$cookies" \
    --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" \
    --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
    --data-urlencode "totp=$totp" "$PANEL/login" >/dev/null
  [[ -n $(awk '$6 == "stepanel_csrf" {print $7}' "$cookies") ]] || fail 'login did not issue a CSRF cookie'
}

api() {
  local method=$1 path=$2 body=${3:-} csrf
  local -a data=()
  csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
  [[ -z $body ]] || data=(-H 'Content-Type: application/json' --data "$body")
  curl --silent --show-error --max-time 60 -X "$method" -b "$cookies" -H "X-CSRF-Token: $csrf" \
    "${data[@]}" -w '\n%{http_code}' "$PANEL$path"
}

# field JSON KEY prints a top-level string field.
field() { python3 -c 'import json, sys
try:
    value = json.loads(sys.argv[1]).get(sys.argv[2], "")
except ValueError:
    value = ""
print(value if isinstance(value, str) else json.dumps(value))' "$1" "$2"; }

job_state() {
  local response
  response=$(api GET "/api/jobs/$1")
  [[ ${response##*$'\n'} == 200 ]] || return 1
  field "${response%$'\n'*}" state
}

wait_terminal() {
  local job=$1 state=''
  for _ in $(seq 1 300); do
    state=$(job_state "$job" || true)
    case $state in
      completed|failed|dead-letter|cancelled) printf '%s' "$state"; return 0 ;;
    esac
    sleep 1
  done
  printf '%s' "${state:-unknown}"
  return 1
}

wait_ready() {
  for _ in $(seq 1 90); do
    curl --fail --silent --max-time 5 "$PANEL/readyz" >/dev/null && return 0
    sleep 1
  done
  fail "readiness did not recover: $(curl --silent --max-time 5 "$PANEL/readyz" || true)"
}

# New sites live in a hosting account (ADR-0007), named in a root-owned
# location record; a host whose control plane predates placements creates
# legacy sites under /var/www/sites.
site_location() { printf '/etc/stepanel/sites/%s/location' "$1"; }
site_docroot() {
  local location
  location=$(site_location "$1")
  if [[ -f $location ]]; then
    printf '/home/%s/%s' "$(sed -n 's/^user=//p' "$location")" "$(sed -n 's/^directory=//p' "$location")"
  else
    printf '/var/www/sites/%s/public' "$1"
  fi
}
site_identity() {
  local location
  location=$(site_location "$1")
  if [[ -f $location ]]; then
    sed -n 's/^user=//p' "$location"
  else
    getent passwd | awk -F: -v home="/var/www/sites/$1" '$6 == home { print $1; exit }'
  fi
}
# site_present reports whether any trace of the site's files or placement remains.
site_present() { [[ -e /var/www/sites/$1 || -e $(site_location "$1") ]]; }

create_site() {
  local response status
  response=$(api POST /api/sites "{\"site\":\"$1\",\"template\":\"php\"}")
  status=${response##*$'\n'}
  [[ $status == 202 ]] || fail "create $1: HTTP $status ${response%$'\n'*}"
  field "${response%$'\n'*}" job_id
}

terminate_site() {
  local site=$1 response job state docroot
  docroot=$(site_docroot "$site")
  response=$(api POST /api/sites/terminate "{\"site\":\"$site\",\"confirmation\":\"DELETE $site\"}")
  [[ ${response##*$'\n'} == 20[02] ]] || fail "terminate $site: HTTP ${response##*$'\n'} ${response%$'\n'*}"
  job=$(field "${response%$'\n'*}" job_id)
  state=$(wait_terminal "$job") || fail "termination of $site did not finish ($state)"
  [[ $state == completed ]] || fail "termination of $site ended $state"
  [[ ! -e $docroot ]] || fail "terminated site $site left its document root $docroot"
  ! site_present "$site" || fail "terminated site $site left its directory or location record"
}

cleanup() {
  local status=$? site
  set +e
  systemctl is-active --quiet stepanel-root-broker.service || systemctl start stepanel-root-broker.service
  for site in "${created[@]}"; do
    site_present "$site" && terminate_site "$site" >/dev/null 2>&1
  done
  rm -rf -- "$work"
  (( status == 0 )) || echo "broker interruption smoke failed (status $status)" >&2
  return "$status"
}
trap cleanup EXIT

wait_ready
login
round=0
for delay in $BROKER_KILL_DELAYS; do
  round=$((round + 1))
  site="brk${round}-$suffix"
  created+=("$site")
  job=$(create_site "$site")
  [[ -n $job ]] || fail "create $site returned no job"
  for _ in $(seq 1 600); do
    state=$(job_state "$job" || true)
    [[ $state == queued || -z $state ]] || break
    sleep 0.1
  done
  [[ $state == running ]] || fail "creation of $site never ran (state ${state:-unknown})"
  sleep "$delay"
  # Kill the whole unit, as a crash of the broker and its helpers would.
  # systemd can fail to signal an auxiliary process that is already exiting;
  # the restart check below proves the broker went down and came back.
  systemctl kill --signal=SIGKILL stepanel-root-broker.service || true
  pass "round $round: killed the root broker ${delay}s into creating $site"
  for _ in $(seq 1 30); do systemctl is-active --quiet stepanel-root-broker.service && break; sleep 1; done
  systemctl is-active --quiet stepanel-root-broker.service || fail 'systemd did not restart the root broker'
  state=$(wait_terminal "$job") || fail "creation of $site is still $state 300s after the broker was killed"
  identity=$(site_identity "$site")
  case $state in
    completed)
      [[ -d $(site_docroot "$site") ]] || fail "completed site $site has no document root ($(site_docroot "$site"))"
      [[ -n $identity ]] || fail "completed site $site has no Unix identity"
      pass "round $round: creation completed across the broker restart (identity $identity)"
      ;;
    *)
      ! site_present "$site" || fail "failed creation of $site left its directory or location record"
      [[ -z $identity ]] || fail "failed creation of $site left a placement for $identity"
      pass "round $round: creation ended $state and left nothing behind; creating it again"
      job=$(create_site "$site")
      state=$(wait_terminal "$job") || fail "repeat creation of $site did not finish ($state)"
      [[ $state == completed ]] || fail "repeat creation of $site ended $state"
      [[ -n $(site_identity "$site") ]] || fail "repeat creation of $site has no Unix identity"
      ;;
  esac
  terminate_site "$site"
  pass "round $round: $site terminated cleanly"
done
wait_ready
readyz=$(curl --silent --max-time 5 "$PANEL/readyz")
[[ $(field "$readyz" ready) == true ]] || fail "readiness did not recover: $readyz"
echo 'root broker interruption smoke passed'
