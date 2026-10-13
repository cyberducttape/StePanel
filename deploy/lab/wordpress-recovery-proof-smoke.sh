#!/usr/bin/env bash
# Verify the installed host can restore a genuine WordPress backup and serve
# the restored, database-backed application through its managed web stack.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'WordPress recovery proof smoke must run as root' >&2; exit 1; }
[[ ${STEPANEL_UNSAFE_LAB:-0} == 1 ]] || { echo 'WordPress recovery proof smoke is restricted to the disposable install lab' >&2; exit 1; }
[[ ${STEPANEL_WEBSERVER:-caddy} == caddy || ${STEPANEL_WEBSERVER:-caddy} == apache ]] || {
  echo "unsupported web server for WordPress recovery proof smoke: ${STEPANEL_WEBSERVER:-unset}" >&2
  exit 77
}
[[ ${STEPANEL_DB_ENGINE:-mariadb} == mariadb || ${STEPANEL_DB_ENGINE:-mariadb} == mysql ]] || {
  echo "WordPress recovery proof smoke requires MySQL or MariaDB, got ${STEPANEL_DB_ENGINE:-unset}" >&2
  exit 77
}

for command in curl php runuser tar sha256sum python3; do
  command -v "$command" >/dev/null || { echo "WordPress recovery proof smoke requires $command" >&2; exit 77; }
done

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${WORDPRESS_PROOF_SMOKE_SITE:=ci-wp-proof}"
# Caddy cannot obtain a public certificate for .example.test. Use .localhost
# so its installed local CA serves the HTTPS redirect with a trusted cert.
: "${WORDPRESS_PROOF_SMOKE_DOMAIN:=ci-wp-proof.localhost}"

site=$WORDPRESS_PROOF_SMOKE_SITE
domain=$WORDPRESS_PROOF_SMOKE_DOMAIN
database=ci_wp_proof
database_user=ci_wp_proof
database_password='ci-WP-proof-DB-password-2026'
admin_password='ci-WP-proof-admin-password-2026'
site_root="/var/www/sites/$site"
public_root="$site_root/public"
work=$(mktemp -d /tmp/stepanel-wordpress-proof.XXXXXX)
site_user=''
site_prepared=0
wp_cli_site=''
database_created=0
route_created=0

cleanup() {
  local status=$?
  if (( route_created )); then
    if [[ ${STEPANEL_WEBSERVER:-caddy} == caddy ]]; then
      /usr/local/sbin/stepanel-vhostctl delete "site-${site}-${domain//./_}.caddy" >/dev/null 2>&1 || true
    else
      /usr/local/sbin/stepanel-vhostctl delete "site-${site}-${domain//./_}.conf" >/dev/null 2>&1 || true
    fi
  fi
  if (( database_created )); then
    /usr/local/sbin/stepanel-dbctl cleanup-wordpress "$database" "$database_user" >/dev/null 2>&1 || true
  fi
  [[ -z $wp_cli_site ]] || rm -f -- "$wp_cli_site"
  if (( site_prepared )) && [[ -n $site_user ]]; then
    /usr/local/sbin/stepanel-sitectl delete "$site" "$site_user" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$work"
  if (( status != 0 )); then
    echo 'installed-host WordPress recovery proof failed; disposable host diagnostics follow' >&2
    systemctl --no-pager --full status caddy apache2 httpd php-fpm 2>&1 | tail -80 >&2 || true
    journalctl -u stepanel.service -u stepanel-worker.service --no-pager -n 100 >&2 || true
  fi
  return "$status"
}
trap cleanup EXIT

[[ ! -e $site_root ]] || { echo "refusing to replace existing smoke site $site" >&2; exit 1; }

# Pin both test inputs by digest so the smoke remains reproducible and cannot
# silently test a different CMS/CLI release after an upstream update.
wordpress_archive="$work/wordpress-6.8.8.tar.gz"
wp_cli="$work/wp-cli-2.12.0.phar"
curl --fail --silent --show-error --location --max-time 120 \
  https://wordpress.org/wordpress-6.8.8.tar.gz -o "$wordpress_archive"
printf '%s  %s\n' '5388e7a8fa462c32e41ce5cabb796e2228cd187b6b80d15d069a8d5e6ecd3e05' "$wordpress_archive" \
  | sha256sum --check --status
curl --fail --silent --show-error --location --max-time 120 \
  https://github.com/wp-cli/wp-cli/releases/download/v2.12.0/wp-cli-2.12.0.phar -o "$wp_cli"
printf '%s  %s\n' 'ce34ddd838f7351d6759068d09793f26755463b4a4610a5a5c0a97b68220d85c' "$wp_cli" \
  | sha256sum --check --status

/usr/local/sbin/stepanel-sitectl prepare "$site"
site_prepared=1
site_user=$(getent passwd | awk -F: -v home="$site_root" '$6 == home { print $1; exit }')
if [[ -z $site_user ]]; then
  digest=$(printf '%s' "$site" | sha256sum)
  digest=${digest%% *}
  prefix=${site:0:18}
  prefix=${prefix//_/-}
  site_user="sp-${prefix}-${digest:0:8}"
fi
wp_cli_site="$site_root/.php/wp-cli-smoke.phar"
install -o "$site_user" -g "$site_user" -m 0600 -- "$wp_cli" "$wp_cli_site"
[[ $(stat -c '%U:%G:%a' -- "$wp_cli_site") == "$site_user:$site_user:600" ]] || {
  echo 'WP-CLI fixture is not private and owned by the site account' >&2
  exit 1
}

printf '%s\n' "$database_password" | \
  /usr/local/sbin/stepanel-dbctl restore-wordpress "$database" "$database_user" "$site"
database_created=1

tar -xzf "$wordpress_archive" --strip-components=1 -C "$public_root"
chown -R "$site_user:$site_user" "$public_root"
chmod 0600 "$public_root/wp-config.php" 2>/dev/null || true
runuser -u "$site_user" -- php "$wp_cli_site" --path="$public_root" config create \
  --dbname="$database" --dbuser="$database_user" --dbpass="$database_password" \
  --dbhost=localhost --skip-check --force
runuser -u "$site_user" -- php "$wp_cli_site" --path="$public_root" core install \
  --url="http://$domain" --title='StePanel Recovery Proof Fixture' \
  --admin_user=ciwpadmin --admin_password="$admin_password" \
  --admin_email=ci-wp-proof@example.test --skip-email
runuser -u "$site_user" -- php "$wp_cli_site" --path="$public_root" option update blogdescription \
  'Database-backed installed-host recovery fixture'
runuser -u "$site_user" -- php "$wp_cli_site" --path="$public_root" post create \
  --post_title='StePanel recovery origin probe' \
  --post_content='STEPANEL_WORDPRESS_ORIGIN_PROBE_6f1d0f3a' --post_status=publish --porcelain >/dev/null

/usr/local/sbin/stepanel-sitectl seal "$site" "$site_user"
/usr/local/sbin/stepanel-vhostctl apply "$site" "$domain"
route_created=1
origin=$(curl --fail --silent --show-error --location --max-time 30 \
  --resolve "$domain:80:127.0.0.1" --resolve "$domain:443:127.0.0.1" "http://$domain/")
grep -Fq 'STEPANEL_WORDPRESS_ORIGIN_PROBE_6f1d0f3a' <<<"$origin" || {
  echo 'the freshly installed WordPress site did not render its database-backed probe post' >&2
  exit 1
}

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
cookies="$work/cookies.txt"
curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
curl --fail --silent --show-error --max-time 10 -L -b "$cookies" -c "$cookies" \
  --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" \
  --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
  --data-urlencode "totp=$totp" "$PANEL/login" >/dev/null
session=$(awk '$6 == "stepanel_session" { print $7 }' "$cookies")
csrf=$(awk '$6 == "stepanel_csrf" { print $7 }' "$cookies")
[[ -n $session && -n $csrf ]] || { echo 'installed panel login did not issue session and CSRF cookies' >&2; exit 1; }
cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"

response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' \
  --data "$(python3 - "$site" <<'PY'
import json, sys
print(json.dumps({"site": sys.argv[1], "include_databases": True}))
PY
)" "$PANEL/api/backups")
backup_job=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["job_id"])' <<<"$response")
[[ -n $backup_job ]] || { echo "database-inclusive backup was not queued: $response" >&2; exit 1; }

wait_job() {
  local id=$1 status state
  for _ in $(seq 1 300); do
    status=$(curl --fail --silent --show-error --max-time 10 -H "Cookie: $cookie_header" "$PANEL/api/jobs/$id")
    state=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("state", ""))' <<<"$status")
    case "$state" in
      completed) return 0 ;;
      failed|dead-letter|cancelled)
        echo "job $id ended in $state: $status" >&2
        return 1
        ;;
    esac
    sleep 1
  done
  echo "job $id did not complete before timeout" >&2
  return 1
}
wait_job "$backup_job"

backup_listing=$(curl --fail --silent --show-error --max-time 20 \
  -H "Cookie: $cookie_header" "$PANEL/api/backups?site=$site")
backup_name=$(python3 - "$database" "$backup_listing" <<'PY'
import json, os, sys
database = sys.argv[1]
items = json.loads(sys.argv[2]).get("backups", [])
matches = [os.path.basename(item["path"].rstrip("/")) for item in items if database in item.get("databases", [])]
print(matches[0] if matches else "")
PY
)
[[ -n $backup_name ]] || { echo "no database-inclusive WordPress backup was listed: $backup_listing" >&2; exit 1; }

response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' \
  --data "$(python3 - "$site" "$backup_name" <<'PY'
import json, sys
print(json.dumps({"site": sys.argv[1], "backup": sys.argv[2]}))
PY
)" "$PANEL/api/backups/rehearse")
rehearsal_job=$(python3 -c 'import json,sys; print(json.load(sys.stdin)["job_id"])' <<<"$response")
[[ -n $rehearsal_job ]] || { echo "WordPress recovery rehearsal was not queued: $response" >&2; exit 1; }
wait_job "$rehearsal_job"

recovery=$(curl --fail --silent --show-error --max-time 20 \
  -H "Cookie: $cookie_header" "$PANEL/api/sites/recovery/$site")
python3 - "$backup_name" "$recovery" <<'PY'
import json, sys
data = json.loads(sys.argv[2])
expected_backup = sys.argv[1]
passed = data.get("last_passed_rehearsal") or {}
assert passed.get("backup") == expected_backup, f"latest passing rehearsal is for {passed.get('backup')!r}"
assert passed.get("level") == "application", f"recovery level is {passed.get('level')!r}, not application"
checks = {check.get("name"): check.get("status") for check in data.get("checks", [])}
for name in ("archive", "database", "application"):
    assert checks.get(name) == "pass", f"{name} recovery check is {checks.get(name)!r}"
assert data.get("measured_recovery_ms", 0) > 0, "application recovery duration was not recorded"
print(f"installed WordPress recovery proof passed: backup={expected_backup}, rto_ms={data['measured_recovery_ms']}")
PY
