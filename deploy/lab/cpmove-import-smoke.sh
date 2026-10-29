#!/usr/bin/env bash
# Installed-host cpmove import smoke test.
set -Eeuo pipefail

trap 'status=$?; echo "cpmove import smoke failed at line ${BASH_LINENO[0]:-unknown} (status $status)" >&2; exit "$status"' ERR

[[ $EUID -eq 0 ]] || { echo 'cpmove import smoke must run as root' >&2; exit 1; }
command -v curl >/dev/null || { echo 'cpmove import smoke requires curl' >&2; exit 77; }
command -v python3 >/dev/null || { echo 'cpmove import smoke requires python3' >&2; exit 77; }

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${CPMOVE_SMOKE_SITE:=ci-import}"

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
login_headers="$work/login.headers"

# site_doc_root SITE prints the site's document root: /home/USER/DIRECTORY
# for a site placed in a hosting account (ADR-0007), else the legacy tree.
site_doc_root() {
  local record="/etc/stepanel/sites/$1/location"
  if [[ -f $record ]]; then
    printf '/home/%s/%s\n' "$(sed -n 's/^user=//p' "$record")" "$(sed -n 's/^directory=//p' "$record")"
  else
    printf '/var/www/sites/%s/public\n' "$1"
  fi
}

generate_totp() {
python3 - "$STEPANEL_ADMIN_TOTP_SECRET" <<'PY'
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
}

login_status=''
for attempt in 1 2; do
  if (( attempt == 2 )); then
    # A previous smoke may have consumed this TOTP counter, or the request may
    # have crossed a counter boundary after code generation. Retry only a 401,
    # and only with a fresh code; throttling and transport failures stay fatal.
    sleep $((31 - $(date +%s) % 30))
    : > "$login_headers"
  fi
  totp=$(generate_totp)
  login_status=$(curl -q --silent --show-error --max-time 10 -D "$login_headers" \
    --output "$work/login-response.html" --write-out '%{http_code}' \
    --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" \
    --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
    --data-urlencode "totp=$totp" \
    "$PANEL/login") || { echo 'installed panel login request failed' >&2; exit 1; }
  if [[ $login_status == 401 && $attempt == 1 ]]; then
    continue
  fi
  [[ $login_status == 303 ]] || {
    echo "installed panel login returned HTTP ${login_status:-unknown}" >&2
    exit 1
  }
  break
done

mapfile -t issued_cookies < <(python3 - "$login_headers" <<'PY'
import sys

cookies = {}
with open(sys.argv[1], encoding="latin-1") as headers:
    for line in headers:
        if line.lower().startswith("set-cookie:"):
            pair = line.split(":", 1)[1].strip().split(";", 1)[0]
            name, value = pair.split("=", 1)
            if name in {"stepanel_session", "stepanel_csrf"}:
                cookies[name] = value
print(cookies.get("stepanel_session", ""))
print(cookies.get("stepanel_csrf", ""))
PY
)
session=${issued_cookies[0]:-}
csrf=${issued_cookies[1]:-}
if [[ -z $session || -z $csrf ]]; then
  cookie_names=$(sed -nE 's/^[Ss]et-[Cc]ookie:[[:space:]]*([^=;]+)=.*/\1/p' "$login_headers" | paste -sd, -)
  echo "installed panel login did not issue session and CSRF cookies (received: ${cookie_names:-none})" >&2
  exit 1
fi
cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"

mkdir -p "$work/cpmove-$CPMOVE_SMOKE_SITE/homedir/public_html"
printf '%s\n' 'stepanel cpmove import smoke' > "$work/cpmove-$CPMOVE_SMOKE_SITE/homedir/public_html/index.html"
tar -C "$work" -czf "$work/cpmove-$CPMOVE_SMOKE_SITE.tar.gz" "cpmove-$CPMOVE_SMOKE_SITE"

inspect_response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" \
  -H "X-CSRF-Token: $csrf" \
  -F "backup=@$work/cpmove-$CPMOVE_SMOKE_SITE.tar.gz;filename=cpmove-$CPMOVE_SMOKE_SITE.tar.gz" \
  "$PANEL/api/cpmove/inspect")
upload_id=$(printf '%s' "$inspect_response" | sed -n 's/.*"upload_id":"\([^"]*\)".*/\1/p')
[[ -n $upload_id ]] || { echo "inspection did not return a durable upload: $inspect_response" >&2; exit 1; }

response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" \
  -H "X-CSRF-Token: $csrf" \
  -F 'confirm=IMPORT' \
  -F "username=$CPMOVE_SMOKE_SITE" \
  -F "upload_id=$upload_id" \
  "$PANEL/api/cpmove/import")
job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
[[ -n $job_id ]] || { echo "upload did not return a durable job: $response" >&2; exit 1; }

for _ in $(seq 1 240); do
  status=$(curl --fail --silent --show-error --max-time 10 \
    -H "Cookie: $cookie_header" "$PANEL/api/jobs/$job_id")
  state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
  case "$state" in
    completed) break ;;
    failed|dead-letter|cancelled)
      echo "cpmove import job $job_id ended in $state: $status" >&2
      exit 1
      ;;
  esac
  sleep 1
done

[[ ${state:-} == completed ]] || { echo "cpmove import job $job_id did not complete: ${status:-}" >&2; exit 1; }
doc_root=$(site_doc_root "$CPMOVE_SMOKE_SITE")
test -f "$doc_root/index.html"
grep -Fx 'stepanel cpmove import smoke' "$doc_root/index.html" >/dev/null

echo "cpmove import smoke passed (job $job_id, site $CPMOVE_SMOKE_SITE)"
