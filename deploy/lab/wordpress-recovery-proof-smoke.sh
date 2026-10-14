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
database_outage_injected=0
broker_outage_injected=0
route_file="/etc/caddy/stepanel.d/site-${site}-${domain//./_}.caddy"

cleanup() {
  local status=$?
  if (( broker_outage_injected )); then
    systemctl start stepanel-root-broker.service || true
  fi
  if (( database_outage_injected )); then
    db_unit=${STEPANEL_DB_ENGINE:-mariadb}
    [[ $db_unit == mysql ]] || db_unit=mariadb
    systemctl start "$db_unit.service" || true
  fi
  if (( status != 0 && route_created )) && [[ ${STEPANEL_WEBSERVER:-caddy} == caddy ]]; then
    echo "Caddy recovery-proof route before cleanup: $route_file" >&2
    if [[ -f /etc/caddy/Caddyfile && ! -L /etc/caddy/Caddyfile ]]; then
      echo 'Caddyfile imports:' >&2
      sed -n '1,120p' /etc/caddy/Caddyfile >&2
    fi
    if [[ -f $route_file && ! -L $route_file ]]; then
      sed -n '1,160p' "$route_file" >&2
    fi
    echo 'Caddy runtime identity and document-root access:' >&2
    id caddy >&2 || true
    systemctl show caddy.service -p User -p Group -p SupplementaryGroups >&2 || true
    for path in "/var/www/.stepanel-caddy-views/$site" "/var/www/.stepanel-caddy-views/$site/public" "$public_root/index.php"; do
      if [[ -e $path && ! -L $path ]]; then
        stat -c '%A %U:%G %n' "$path" >&2 || true
        getfacl -cp "$path" >&2 || true
      fi
    done
    if command -v runuser >/dev/null 2>&1; then
      runuser -u caddy -- test -r "$public_root/index.php" \
        && echo 'Caddy account can read the canonical index.php path' >&2 \
        || echo 'Caddy account cannot read the canonical index.php path' >&2
      for path in /index.php /wp-login.php; do
        probe_body="$work/caddy-probe-${path##*/}.html"
        probe_status=$(curl --silent --show-error --max-time 10 \
          --resolve "$domain:443:127.0.0.1" --output "$probe_body" --write-out '%{http_code}' \
          "https://$domain$path" || true)
        echo "Caddy direct HTTPS $path: HTTP ${probe_status:-unknown}" >&2
        [[ ! -f $probe_body ]] || sed -n '1,40p' "$probe_body" >&2
      done
    fi
    echo 'Caddy adapted host/handler routes:' >&2
    if adapted_caddy=$(caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile --pretty 2>&1); then
      if command -v jq >/dev/null 2>&1; then
        jq -c '[.apps.http.servers[]?.routes[]? | {hosts: [.match[]?.host[]?], handlers: [.handle[]?.handler]}] | .[]' \
          <<<"$adapted_caddy" >&2 || printf '%s\n' "$adapted_caddy" | head -c 32768 >&2
      else
        printf '%s\n' "$adapted_caddy" | head -c 32768 >&2
      fi
    else
      printf '%s\n' "$adapted_caddy" | head -c 32768 >&2
    fi
  fi
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
application_scheme=http
if [[ ${STEPANEL_WEBSERVER:-caddy} == caddy ]]; then
  application_scheme=https
fi
application_url="$application_scheme://$domain"
runuser -u "$site_user" -- php "$wp_cli_site" --path="$public_root" core install \
  --url="$application_url" --title='StePanel Recovery Proof Fixture' \
  --admin_user=ciwpadmin --admin_password="$admin_password" \
  --admin_email=ci-wp-proof@example.test --skip-email
runuser -u "$site_user" -- php "$wp_cli_site" --path="$public_root" option update blogdescription \
  'Database-backed installed-host recovery fixture'
origin_post_id=$(runuser -u "$site_user" -- php "$wp_cli_site" --path="$public_root" post create \
  --post_title='StePanel recovery origin probe' \
  --post_content='STEPANEL_WORDPRESS_ORIGIN_PROBE_6f1d0f3a' --post_status=publish --porcelain)
[[ $origin_post_id =~ ^[1-9][0-9]*$ ]] || { echo "WP-CLI returned an invalid recovery probe post ID: $origin_post_id" >&2; exit 1; }
origin_post_status=$(runuser -u "$site_user" -- php "$wp_cli_site" --path="$public_root" post get "$origin_post_id" --field=post_status)
[[ $origin_post_status == publish ]] || { echo "WordPress recovery probe post has status $origin_post_status, want publish" >&2; exit 1; }
printf 'WordPress recovery probe post %s is %s; home=%s siteurl=%s\n' \
  "$origin_post_id" "$origin_post_status" \
  "$(runuser -u "$site_user" -- php "$wp_cli_site" --path="$public_root" option get home)" \
  "$(runuser -u "$site_user" -- php "$wp_cli_site" --path="$public_root" option get siteurl)"

/usr/local/sbin/stepanel-sitectl seal "$site" "$site_user"
/usr/local/sbin/stepanel-vhostctl apply "$site" "$domain"
route_created=1
origin_body="$work/origin-response.html"
origin_headers="$work/origin-response.headers"
origin_curl_resolve=(--resolve "$domain:80:127.0.0.1")
if [[ $application_scheme == https ]]; then
  origin_curl_resolve=(--resolve "$domain:443:127.0.0.1")
fi
origin_status=$(curl --silent --show-error --location --max-time 30 \
  "${origin_curl_resolve[@]}" \
  --dump-header "$origin_headers" --output "$origin_body" --write-out '%{http_code}' \
  "$application_url/?p=$origin_post_id" || true)
if [[ $origin_status != 200 ]] || ! grep -Fq 'STEPANEL_WORDPRESS_ORIGIN_PROBE_6f1d0f3a' "$origin_body"; then
  direct_body="$work/direct-index-response.html"
  direct_headers="$work/direct-index-response.headers"
  direct_status=$(curl --silent --show-error --location --max-time 30 \
    "${origin_curl_resolve[@]}" \
    --dump-header "$direct_headers" --output "$direct_body" --write-out '%{http_code}' \
    "$application_url/index.php?p=$origin_post_id" || true)
  echo "direct index.php recovery probe: HTTP ${direct_status:-unknown}" >&2
  [[ ! -f $direct_headers ]] || sed -n '1,40p' "$direct_headers" >&2
  [[ ! -f $direct_body ]] || sed -n '1,80p' "$direct_body" >&2
  echo "database-backed WordPress post probe failed (HTTP ${origin_status:-unknown}, post $origin_post_id)" >&2
  [[ ! -f $origin_headers ]] || sed -n '1,40p' "$origin_headers" >&2
  [[ ! -f $origin_body ]] || sed -n '1,120p' "$origin_body" >&2
  exit 1
fi

# Exercise component degradation and recovery on the disposable installed
# host. The site is still live here, so this proves both customer-facing DB
# recovery and that management readiness fails closed when the broker is down.
db_unit=${STEPANEL_DB_ENGINE:-mariadb}
[[ $db_unit == mysql ]] || db_unit=mariadb
systemctl is-active --quiet "$db_unit.service" || {
  echo "database service $db_unit.service is not active before outage drill" >&2
  exit 1
}
database_outage_injected=1
systemctl stop "$db_unit.service"
db_outage_status=$(curl --silent --show-error --max-time 20 \
  "${origin_curl_resolve[@]}" --output /dev/null --write-out '%{http_code}' \
  "$application_url/?p=$origin_post_id" || true)
[[ $db_outage_status != 200 ]] || {
  echo 'WordPress continued returning HTTP 200 while its database was stopped' >&2
  exit 1
}
systemctl start "$db_unit.service"
recovered_body="$work/recovered-response.html"
for _ in $(seq 1 60); do
  if systemctl is-active --quiet "$db_unit.service" && \
     curl --fail --silent --max-time 5 "${origin_curl_resolve[@]}" \
       --output "$recovered_body" "$application_url/?p=$origin_post_id" && \
     grep -Fq 'STEPANEL_WORDPRESS_ORIGIN_PROBE_6f1d0f3a' "$recovered_body"; then
    break
  fi
  sleep 1
done
if ! curl --fail --silent --show-error --max-time 10 "${origin_curl_resolve[@]}" \
  --output "$recovered_body" "$application_url/?p=$origin_post_id" || \
  ! grep -Fq 'STEPANEL_WORDPRESS_ORIGIN_PROBE_6f1d0f3a' "$recovered_body"; then
  echo 'WordPress did not recover after the database service restarted' >&2
  exit 1
fi
database_outage_injected=0
echo "installed WordPress database outage recovered (during outage HTTP $db_outage_status)"

systemctl is-active --quiet stepanel-root-broker.service || {
  echo 'root broker service is not active before outage drill' >&2
  exit 1
}
broker_outage_injected=1
systemctl stop stepanel-root-broker.service
live_status=$(curl --silent --show-error --max-time 5 --output /dev/null \
  --write-out '%{http_code}' http://127.0.0.1:8090/livez || true)
ready_status=$(curl --silent --show-error --max-time 5 --output "$work/broker-down-ready.json" \
  --write-out '%{http_code}' http://127.0.0.1:8090/readyz || true)
[[ $live_status == 204 && $ready_status == 503 ]] || {
  echo "broker outage health contract mismatch: /livez=$live_status /readyz=$ready_status (expected 204/503)" >&2
  exit 1
}
python3 - "$work/broker-down-ready.json" <<'PY'
import json, sys
data = json.load(open(sys.argv[1], encoding="utf-8"))
assert data.get("ready") is False, "readiness did not report degraded state during broker outage"
check = data.get("checks", {}).get("root_broker", {})
assert check.get("ready") is False, "readiness omitted the unavailable root broker"
PY
systemctl start stepanel-root-broker.service
for _ in $(seq 1 60); do
  if systemctl is-active --quiet stepanel-root-broker.service && \
     curl --fail --silent --max-time 2 http://127.0.0.1:8090/readyz >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent --show-error --max-time 10 http://127.0.0.1:8090/readyz >/dev/null || {
  echo 'panel readiness did not recover after the root broker restarted' >&2
  exit 1
}
broker_outage_injected=0
echo 'root broker outage degraded readiness while preserving liveness, then recovered'

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

# Exercise the helper's crash-replay contract on the installed webserver:
# delete the route once, then repeat after the include file is already gone.
if [[ ${STEPANEL_WEBSERVER:-caddy} == caddy ]]; then
  route_name="site-${site}-${domain//./_}.caddy"
else
  route_name="site-${site}-${domain//./_}.conf"
fi
/usr/local/sbin/stepanel-vhostctl delete "$route_name"
route_created=0
/usr/local/sbin/stepanel-vhostctl delete "$route_name"
echo 'installed vhost deletion replay passed with the route file already absent'
