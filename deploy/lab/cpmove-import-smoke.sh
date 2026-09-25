#!/usr/bin/env bash
# Installed-host cpmove import smoke test.
set -Eeuo pipefail

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
cookies="$work/cookies.txt"

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
[[ -n $session && -n $csrf ]] || { echo 'installed panel login did not issue session and CSRF cookies' >&2; exit 1; }
cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"

mkdir -p "$work/cpmove-$CPMOVE_SMOKE_SITE/homedir/public_html"
printf '%s\n' 'stepanel cpmove import smoke' > "$work/cpmove-$CPMOVE_SMOKE_SITE/homedir/public_html/index.html"
tar -C "$work" -czf "$work/cpmove-$CPMOVE_SMOKE_SITE.tar.gz" "cpmove-$CPMOVE_SMOKE_SITE"

response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" \
  -H "X-CSRF-Token: $csrf" \
  -F 'confirm=IMPORT' \
  -F "username=$CPMOVE_SMOKE_SITE" \
  -F "backup=@$work/cpmove-$CPMOVE_SMOKE_SITE.tar.gz;filename=cpmove-$CPMOVE_SMOKE_SITE.tar.gz" \
  "$PANEL/api/cpmove/import")
job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
[[ -n $job_id ]] || { echo "upload did not return a durable job: $response" >&2; exit 1; }

for _ in $(seq 1 120); do
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
test -f "/var/www/sites/$CPMOVE_SMOKE_SITE/public/index.html"
grep -Fx 'stepanel cpmove import smoke' "/var/www/sites/$CPMOVE_SMOKE_SITE/public/index.html" >/dev/null

echo "cpmove import smoke passed (job $job_id, site $CPMOVE_SMOKE_SITE)"
