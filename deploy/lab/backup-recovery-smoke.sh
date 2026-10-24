#!/usr/bin/env bash
# Exercise a real durable backup job across a worker SIGKILL and restart.
set -Eeuo pipefail

on_error() {
  local status=$?
  echo "backup recovery smoke failed at line ${BASH_LINENO[0]:-unknown} (status $status)" >&2
  exit "$status"
}
trap on_error ERR

[[ $EUID -eq 0 ]] || { echo 'backup recovery smoke must run as root' >&2; exit 1; }
command -v curl >/dev/null || { echo 'backup recovery smoke requires curl' >&2; exit 77; }
command -v python3 >/dev/null || { echo 'backup recovery smoke requires python3' >&2; exit 77; }
command -v systemctl >/dev/null || { echo 'backup recovery smoke requires systemd' >&2; exit 77; }

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${BACKUP_RECOVERY_SMOKE_SITE:=ci-import-recovery}"
: "${BACKUP_KILL_AT:=backup:archive}"
: "${RESTORE_KILL_AT:=restore:activate}"
: "${DATABASE_RESTORE_KILL_AT:=restore:database}"
: "${TERMINATE_KILL_AT:=terminate:site-state}"
: "${STAGING_RESTORE_KILL_AT:=}"

dropin_dir=/run/systemd/system/stepanel-worker.service.d
dropin="$dropin_dir/recovery-smoke.conf"
work=''
database_name=''
database_user=''
staging_site=''
staging_database=''
staging_user=''
staging_unix_user=''
staging_route=''
# shellcheck source=../integrations/stepanel-site-location.sh
. /usr/local/lib/stepanel/site-location.sh
site_public_root() {
  local site=$1
  stepanel_site_location "$site" || return 1
  printf '%s\n' "$public_root"
}
dump_restore_permissions() {
  local site_root site_doc_root
  site_doc_root=$(site_public_root "$BACKUP_RECOVERY_SMOKE_SITE")
  site_root=${site_doc_root%/*}
  local recovery_root='/var/www/sites/.stepanel-recovery'
  echo 'restore permission diagnostics:' >&2
  id stepanel >&2 || true
  stat -c '%A %a %U:%G %n' /var/www /var/www/sites "$site_root" "$site_doc_root" "$recovery_root" >&2 || true
  namei -l "$site_doc_root" >&2 || true
  namei -l "$recovery_root" >&2 || true
  getfacl -p "$site_root" "$site_doc_root" "$recovery_root" >&2 || true
}
mkdir -p "$dropin_dir"
cleanup() {
  systemctl stop stepanel-worker.service >/dev/null 2>&1 || true
  rm -f -- "$dropin"
  systemctl daemon-reload >/dev/null 2>&1 || true
  timeout --foreground 30s systemctl start stepanel-worker.service >/dev/null 2>&1 || true
  if [[ -n "$database_name" && -x /usr/local/sbin/stepanel-dbctl ]]; then
    /usr/local/sbin/stepanel-dbctl drop-managed "$database_name" "$database_user" >/dev/null 2>&1 || true
  fi
  if [[ -n $work ]]; then
    rm -rf -- "$work"
  fi
}
trap cleanup EXIT

wait_for_panel_ready() {
  local stage=${1:-after worker restart}
  for _ in $(seq 1 120); do
    if systemctl is-active --quiet stepanel.service && \
       curl --fail --silent --max-time 2 "$PANEL/readyz" >/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "panel did not become ready $stage" >&2
  systemctl status stepanel.service stepanel-worker.service --no-pager >&2 || true
  journalctl -u stepanel.service -u stepanel-worker.service --no-pager -n 80 >&2 || true
  return 1
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

work=$(mktemp -d)
cookies="$work/cookies.txt"
wait_for_panel_ready 'before backup-recovery login'
login_status=''
for attempt in 1 2; do
  if (( attempt == 2 )); then
    # Another recovery smoke may have consumed this counter, or the request
    # may cross its boundary after code generation. Retry only a 401, with a
    # fresh code; transport and throttling failures remain fatal.
    sleep $((31 - $(date +%s) % 30))
    : > "$cookies"
  fi
  totp=$(generate_totp)
  curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
  login_status=$(curl --silent --show-error --max-time 10 -L \
    -b "$cookies" -c "$cookies" \
    --output "$work/login-response.html" --write-out '%{http_code}' \
    --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" \
    --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
    --data-urlencode "totp=$totp" \
    "$PANEL/login") || { echo 'backup recovery login request failed' >&2; exit 1; }
  if [[ $login_status == 401 && $attempt == 1 ]]; then
    continue
  fi
  [[ $login_status == 200 ]] || {
    echo "backup recovery login returned HTTP ${login_status:-unknown}" >&2
    exit 1
  }
  break
done

session=$(awk '$6 == "stepanel_session" {print $7}' "$cookies")
csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
[[ -n $session && -n $csrf ]] || { echo 'backup recovery login did not issue session and CSRF cookies' >&2; exit 1; }
cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"

printf '%s\n' '[Service]' \
  'Environment=STEPANEL_LAB_DIRECT_ROOT_BROKER=1' \
  "Environment=STEPANEL_KILL_AT=$BACKUP_KILL_AT" > "$dropin"
systemctl daemon-reload
systemctl restart stepanel-worker.service
systemctl is-active --quiet stepanel-worker.service
wait_for_panel_ready
before=$(systemctl show stepanel-worker.service -p MainPID --value)
[[ "$before" =~ ^[1-9][0-9]*$ ]] || { echo "could not determine worker PID: $before" >&2; exit 1; }

response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" \
  -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' \
  --data "{\"site\":\"$BACKUP_RECOVERY_SMOKE_SITE\",\"include_databases\":false}" \
  "$PANEL/api/backups")
job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
[[ -n $job_id ]] || { echo "backup request did not return a durable job: $response" >&2; exit 1; }

killed=0
restore_status=''
for _ in $(seq 1 90); do
  current=$(systemctl show stepanel-worker.service -p MainPID --value)
  if [[ "$current" =~ ^[1-9][0-9]*$ && "$current" != "$before" ]]; then
    killed=1
    # Stop systemd's replacement before clearing the kill injection; otherwise
    # Restart=on-failure can kill the replacement a second time.
    systemctl stop stepanel-worker.service || true
    rm -f -- "$dropin"
    systemctl daemon-reload
    systemctl start stepanel-worker.service
    wait_for_panel_ready
    break
  fi
  sleep 1
done

if (( ! killed )); then
  echo 'worker PID never changed; the injected backup process-kill boundary was not observed' >&2
  exit 1
fi

state=''
status=''
for _ in $(seq 1 240); do
  status=$(curl --fail --silent --show-error --max-time 10 \
    -H "Cookie: $cookie_header" "$PANEL/api/jobs/$job_id")
  state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
  case "$state" in
    completed) break ;;
    failed|dead-letter|cancelled)
      echo "backup recovery job $job_id ended in $state: $status" >&2
      exit 1
      ;;
  esac
  sleep 1
done
[[ "$state" == completed ]] || { echo "backup recovery job $job_id did not complete: ${status:-}" >&2; exit 1; }

backups=$(curl --fail --silent --show-error --max-time 10 \
  -H "Cookie: $cookie_header" "$PANEL/api/backups?site=$BACKUP_RECOVERY_SMOKE_SITE")
printf '%s' "$backups" | grep -Fq "\"site\":\"$BACKUP_RECOVERY_SMOKE_SITE\""
echo "backup recovery smoke passed (worker $before was killed at $BACKUP_KILL_AT)"

# Reuse the verified artifact for a durable file restore and kill the worker
# after activation has begun. Recovery must replay the job without leaving a
# partially published site.
backup_name=$(printf '%s' "$backups" | python3 -c 'import json, sys; items=json.load(sys.stdin)["backups"]; print(items[0]["path"].rstrip("/").rsplit("/", 1)[-1] if items else "")')
[[ -n $backup_name ]] || { echo 'backup listing did not expose a restore artifact' >&2; exit 1; }

printf '%s\n' '[Service]' \
  'Environment=STEPANEL_LAB_DIRECT_ROOT_BROKER=1' \
  "Environment=STEPANEL_KILL_AT=$RESTORE_KILL_AT" > "$dropin"
systemctl daemon-reload
systemctl restart stepanel-worker.service
systemctl is-active --quiet stepanel-worker.service
wait_for_panel_ready
before=$(systemctl show stepanel-worker.service -p MainPID --value)

response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" \
  -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' \
  --data "$(python3 - "$backup_name" "$BACKUP_RECOVERY_SMOKE_SITE" <<'PY'
import json, sys
print(json.dumps({"site": sys.argv[2], "backup": sys.argv[1], "confirm": "RESTORE_FILES"}))
PY
)" \
  "$PANEL/api/backups/restore-files")
restore_job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
[[ -n $restore_job_id ]] || { echo "restore request did not return a durable job: $response" >&2; exit 1; }

killed=0
for _ in $(seq 1 90); do
  current=$(systemctl show stepanel-worker.service -p MainPID --value)
  if [[ "$current" =~ ^[1-9][0-9]*$ && "$current" != "$before" ]]; then
    killed=1
    systemctl stop stepanel-worker.service || true
    rm -f -- "$dropin"
    systemctl daemon-reload
    systemctl start stepanel-worker.service
    wait_for_panel_ready
    break
  fi
  # A restore can fail before reaching the injected boundary (for example
  # because durable authorization or scheduling rejected it). Report that
  # durable result immediately instead of misdiagnosing it as a missing
  # process kill after 90 seconds.
  restore_status=$(curl --fail --silent --show-error --max-time 10 \
    -H "Cookie: $cookie_header" "$PANEL/api/jobs/$restore_job_id" || true)
  restore_state=$(printf '%s' "$restore_status" | sed -n 's/.*"state":"\([^\"]*\)".*/\1/p')
  case "$restore_state" in
    completed)
      echo "restore completed before injected kill boundary $RESTORE_KILL_AT: $restore_status" >&2
      exit 1
      ;;
    failed|dead-letter|cancelled)
      echo "restore job ended in $restore_state before injected kill boundary $RESTORE_KILL_AT: $restore_status" >&2
      dump_restore_permissions
      exit 1
      ;;
  esac
  sleep 1
done
if (( ! killed )); then
  echo "worker PID never changed; injected restore process-kill boundary $RESTORE_KILL_AT was not observed" >&2
  echo "last durable restore status: ${restore_status:-unavailable}" >&2
  systemctl status stepanel-worker.service --no-pager >&2 || true
  journalctl -u stepanel-worker.service -n 100 --no-pager >&2 || true
  exit 1
fi

state=''
status=''
for _ in $(seq 1 240); do
  status=$(curl --fail --silent --show-error --max-time 10 \
    -H "Cookie: $cookie_header" "$PANEL/api/jobs/$restore_job_id")
  state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
  case "$state" in
    completed) break ;;
    failed|dead-letter|cancelled)
      echo "restore recovery job $restore_job_id ended in $state: $status" >&2
      dump_restore_permissions
      exit 1
      ;;
  esac
  sleep 1
done
[[ "$state" == completed ]] || { echo "restore recovery job $restore_job_id did not complete: ${status:-}" >&2; exit 1; }
echo "restore recovery smoke passed (worker $before was killed at $RESTORE_KILL_AT)"

if [[ ${DATABASE_RESTORE_RECOVERY:-0} == 1 ]]; then
  [[ ${STEPANEL_DB_ENGINE:-mariadb} == mariadb ]] || {
    echo 'database restore recovery smoke currently requires MariaDB' >&2
    exit 77
  }

  # Exercise the database-only restore path separately from the files-only
  # restore above. The database is disposable and is removed by cleanup even
  # when a later assertion fails.
  database_name=$(printf '%s_db' "$BACKUP_RECOVERY_SMOKE_SITE" | tr '-' '_')
  database_user=$(printf '%s_u' "$BACKUP_RECOVERY_SMOKE_SITE" | tr '-' '_')
  database_password='ci-database-restore-password-1234'
  response=$(curl --fail --silent --show-error --max-time 30 \
    -H "Cookie: $cookie_header" \
    -H "X-CSRF-Token: $csrf" \
    -H 'Content-Type: application/json' \
    --data "$(python3 - "$database_name" "$database_user" "$BACKUP_RECOVERY_SMOKE_SITE" "$database_password" <<'PY'
import json, sys
print(json.dumps({"name": sys.argv[1], "user": sys.argv[2], "site": sys.argv[3], "password": sys.argv[4], "encoding": "utf8mb4"}))
PY
)" \
    "$PANEL/api/databases")
printf '%s\n' \
  'CREATE TABLE IF NOT EXISTS stepanel_recovery (id INT PRIMARY KEY, value VARCHAR(64) NOT NULL);' \
  "INSERT INTO stepanel_recovery (id, value) VALUES (1, 'database restore smoke') ON DUPLICATE KEY UPDATE value=VALUES(value);" \
  | /usr/local/sbin/stepanel-dbctl restore-dump "$database_name" "$BACKUP_RECOVERY_SMOKE_SITE"

  response=$(curl --fail --silent --show-error --max-time 30 \
    -H "Cookie: $cookie_header" \
    -H "X-CSRF-Token: $csrf" \
    -H 'Content-Type: application/json' \
    --data "{\"site\":\"$BACKUP_RECOVERY_SMOKE_SITE\",\"include_databases\":true}" \
    "$PANEL/api/backups")
  database_backup_job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^\"]*\)".*/\1/p')
  [[ -n "$database_backup_job_id" ]] || { echo "database backup request did not return a durable job: $response" >&2; exit 1; }
  state=''
  status=''
  for _ in $(seq 1 240); do
    status=$(curl --fail --silent --show-error --max-time 10 \
      -H "Cookie: $cookie_header" "$PANEL/api/jobs/$database_backup_job_id")
    state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^\"]*\)".*/\1/p')
    case "$state" in
      completed) break ;;
      failed|dead-letter|cancelled)
        echo "database backup job $database_backup_job_id ended in $state: $status" >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  [[ "$state" == completed ]] || { echo "database backup job $database_backup_job_id did not complete: ${status:-}" >&2; exit 1; }

  database_backup_name=$(curl --fail --silent --show-error --max-time 10 \
    -H "Cookie: $cookie_header" "$PANEL/api/backups?site=$BACKUP_RECOVERY_SMOKE_SITE" \
    | python3 -c 'import json, sys; database = sys.argv[1]; items = json.load(sys.stdin).get("backups", []); matches = [item["path"].rstrip("/").rsplit("/", 1)[-1] for item in items if database in item.get("databases", [])]; print(matches[0] if matches else "")' "$database_name"
  )
  [[ -n "$database_backup_name" ]] || { echo 'database-inclusive backup was not listed' >&2; exit 1; }

  if [[ -n "$STAGING_RESTORE_KILL_AT" ]]; then
    [[ $STAGING_RESTORE_KILL_AT == restore:provision || $STAGING_RESTORE_KILL_AT == restore:provisioned ]] || {
      echo "unsupported staging restore kill point: $STAGING_RESTORE_KILL_AT" >&2
      exit 64
    }
    # Exercise the database restore-to-staging path on an actual installed
    # host. These provisioning checkpoints used to have unit-only coverage.
    site_hash=$(printf '%s' "$BACKUP_RECOVERY_SMOKE_SITE" | sha256sum | cut -c1-6)
    staging_site="${BACKUP_RECOVERY_SMOKE_SITE:0:13}-st-$site_hash"
    staging_domain="$staging_site.localhost"
    staging_database="${staging_site//-/_}_db"
    staging_user="${staging_site//-/_}_u"
    staging_password='ci-staging-restore-password-2345'
    staging_route="site-${staging_site}-${staging_domain//./_}"
    if [[ -d /etc/caddy ]]; then
      staging_route+=.caddy
    else
      staging_route+=.conf
    fi
    [[ ! -e "/var/www/sites/$staging_site" ]] || {
      echo "staging recovery destination already exists: $staging_site" >&2
      exit 1
    }

    printf '%s\n' '[Service]' \
      'Environment=STEPANEL_LAB_DIRECT_ROOT_BROKER=1' \
      "Environment=STEPANEL_KILL_AT=$STAGING_RESTORE_KILL_AT" > "$dropin"
    systemctl daemon-reload
    systemctl restart stepanel-worker.service
    systemctl is-active --quiet stepanel-worker.service
    wait_for_panel_ready
    before=$(systemctl show stepanel-worker.service -p MainPID --value)

    response=$(curl --fail --silent --show-error --max-time 30 \
      -H "Cookie: $cookie_header" \
      -H "X-CSRF-Token: $csrf" \
      -H 'Content-Type: application/json' \
      --data "$(python3 - "$database_backup_name" "$staging_site" "$staging_domain" "$database_name" "$staging_database" "$staging_user" "$staging_password" <<'PY'
import json, sys
backup, site, domain, database, target_database, target_user, password = sys.argv[1:]
print(json.dumps({
    "backup": backup,
    "site": site,
    "domain": domain,
    "database": database,
    "target_database": target_database,
    "target_user": target_user,
    "target_password": password,
}))
PY
)" \
      "$PANEL/api/backups/restore-to-staging")
  staging_restore_job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
  [[ -n "$staging_restore_job_id" ]] || { echo "staging restore did not return a durable job: $response" >&2; exit 1; }

  killed=0
  staging_status=''
  for _ in $(seq 1 240); do
    current=$(systemctl show stepanel-worker.service -p MainPID --value)
    if [[ $current =~ ^[1-9][0-9]*$ && $current != "$before" ]]; then
      killed=1
      systemctl stop stepanel-worker.service || true
      rm -f -- "$dropin"
      systemctl daemon-reload
      systemctl start stepanel-worker.service
      wait_for_panel_ready
      break
    fi
    staging_status=$(curl --fail --silent --show-error --max-time 10 \
      -H "Cookie: $cookie_header" "$PANEL/api/jobs/$staging_restore_job_id")
    staging_state=$(printf '%s' "$staging_status" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
    case "$staging_state" in
      completed)
        echo "staging restore completed before injected kill point $STAGING_RESTORE_KILL_AT: $staging_status" >&2
        exit 1
        ;;
      failed|dead-letter|cancelled)
        echo "staging restore ended in $staging_state before injected kill point $STAGING_RESTORE_KILL_AT: $staging_status" >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  (( killed )) || { echo "worker PID never changed; staging restore kill point $STAGING_RESTORE_KILL_AT was not observed" >&2; exit 1; }

  staging_state=''
  staging_status=''
  for _ in $(seq 1 360); do
    staging_status=$(curl --fail --silent --show-error --max-time 10 \
      -H "Cookie: $cookie_header" "$PANEL/api/jobs/$staging_restore_job_id")
    staging_state=$(printf '%s' "$staging_status" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
    case "$staging_state" in
      completed|dead-letter) break ;;
      failed|cancelled)
        echo "staging restore recovery job $staging_restore_job_id ended in $staging_state: $staging_status" >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  if [[ $staging_state == dead-letter ]]; then
    # Site operations are intentionally not replayed after a worker crash: a
    # deployment may have been superseded by its requester. Staging restore
    # must instead compensate all partially provisioned resources before the
    # operation is surfaced for operator review.
    staging_root="/var/www/sites/$staging_site"
    [[ ! -e $staging_root ]] || { echo "interrupted staging restore left a partial site tree: $staging_root" >&2; exit 1; }
    for route_path in "/etc/caddy/stepanel.d/$staging_route" \
      "/etc/apache2/stepanel-sites/$staging_route" \
      "/etc/httpd/conf.d/$staging_route"; do
      [[ ! -e $route_path ]] || { echo "interrupted staging restore left a route: $route_path" >&2; exit 1; }
    done
    if getent passwd | awk -F: -v home="$staging_root" '$6 == home { found=1 } END { exit !found }'; then
      echo "interrupted staging restore left a Unix account for $staging_site" >&2
      exit 1
    fi
    if /usr/local/sbin/stepanel-dbctl dump "$staging_database" > "$work/staging-database.sql" 2>/dev/null; then
      echo "interrupted staging restore left managed database $staging_database" >&2
      exit 1
    fi
    source_public_root=$(site_public_root "$BACKUP_RECOVERY_SMOKE_SITE")
    [[ -f "$source_public_root/index.html" ]] || {
      echo "interrupted staging restore disturbed source site $BACKUP_RECOVERY_SMOKE_SITE" >&2
      exit 1
    }
    echo "staging restore rollback smoke passed (worker $before was killed at $STAGING_RESTORE_KILL_AT; dead-lettered and partial site, route, account, database removed)"
  else
    [[ $staging_state == completed ]] || { echo "staging restore recovery job did not complete: ${staging_status:-}" >&2; exit 1; }

  public_root="/var/www/sites/$staging_site/public"
  [[ -f "$public_root/index.html" && -f "/var/www/sites/$staging_site/.stepanel-staging-noindex" ]] || {
    echo "staging restore did not publish the expected isolated site: $staging_site" >&2
    exit 1
  }
  grep -Fx 'stepanel cpmove import smoke' "$public_root/index.html" >/dev/null
  [[ -f "/etc/caddy/stepanel.d/$staging_route" || \
     -f "/etc/apache2/stepanel-sites/$staging_route" || \
     -f "/etc/httpd/conf.d/stepanel-sites/$staging_route" ]] || {
    echo "staging restore did not publish its webserver route: $staging_route" >&2
    exit 1
  }
  if [[ -d /etc/caddy ]]; then
    staging_http=$(curl --insecure --fail --silent --show-error --max-time 10 \
      --resolve "$staging_domain:443:127.0.0.1" "https://$staging_domain/")
  else
    staging_http=$(curl --fail --silent --show-error --max-time 10 \
      --resolve "$staging_domain:80:127.0.0.1" "http://$staging_domain/")
  fi
  [[ $staging_http == *'stepanel cpmove import smoke'* ]] || {
    echo "restored staging site did not serve the expected HTTP content: $staging_domain" >&2
    exit 1
  }
  /usr/local/sbin/stepanel-dbctl dump "$staging_database" > "$work/staging-database.sql"
  grep -Fq 'database restore smoke' "$work/staging-database.sql"
  staging_unix_user=$(getent passwd | awk -F: -v home="/var/www/sites/$staging_site" '$6 == home { print $1; exit }')
  [[ $staging_unix_user =~ ^sp-[a-z0-9][a-z0-9-]{1,28}$ ]] || {
    echo "could not resolve immutable staging-site identity: $staging_site ($staging_unix_user)" >&2
    exit 1
  }
  echo "staging restore recovery smoke passed (worker $before was killed at $STAGING_RESTORE_KILL_AT; files, route, HTTP and database verified)"

  # Clean up through the canonical durable lifecycle path, not by deriving a
  # legacy username or deleting the route/files/database independently. This
  # also checks that a completed restore can be safely removed as a site.
  printf '%s\n' '[Service]' \
    'Environment=STEPANEL_LAB_DIRECT_ROOT_BROKER=1' \
    'Environment=STEPANEL_KILL_AT=' > "$dropin"
  systemctl daemon-reload
  systemctl restart stepanel-worker.service
  systemctl is-active --quiet stepanel-worker.service
  wait_for_panel_ready 'before staging-site cleanup'
  response=$(curl --fail --silent --show-error --max-time 30 \
    -H "Cookie: $cookie_header" \
    -H "X-CSRF-Token: $csrf" \
    -H 'Content-Type: application/json' \
    --data "$(python3 - "$staging_site" <<'PY'
import json, sys
site = sys.argv[1]
print(json.dumps({"site": site, "confirmation": "DELETE " + site}))
PY
)" \
    "$PANEL/api/sites/terminate")
  staging_termination_job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^\"]*\)".*/\1/p')
  [[ -n $staging_termination_job_id ]] || {
    echo "staging-site termination did not return a durable job: $response" >&2
    exit 1
  }
  staging_termination_status=''
  staging_termination_state=''
  for _ in $(seq 1 360); do
    staging_termination_status=$(curl --fail --silent --show-error --max-time 10 \
      -H "Cookie: $cookie_header" "$PANEL/api/jobs/$staging_termination_job_id")
    staging_termination_state=$(printf '%s' "$staging_termination_status" | sed -n 's/.*"state":"\([^\"]*\)".*/\1/p')
    case "$staging_termination_state" in
      completed) break ;;
      failed|dead-letter|cancelled)
        echo "staging-site termination ended in $staging_termination_state: $staging_termination_status" >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  [[ $staging_termination_state == completed ]] || {
    echo "staging-site termination did not complete: ${staging_termination_status:-}" >&2
    exit 1
  }
  [[ ! -e "/var/www/sites/$staging_site" ]] || { echo "staging test site cleanup failed: $staging_site" >&2; exit 1; }
  if getent passwd "$staging_unix_user" >/dev/null; then
    echo "staging-site Unix identity survived canonical termination: $staging_unix_user" >&2
    exit 1
  fi
  for route_path in "/etc/caddy/stepanel.d/$staging_route" \
    "/etc/apache2/stepanel-sites/$staging_route" \
    "/etc/httpd/conf.d/stepanel-sites/$staging_route"; do
    [[ ! -e $route_path ]] || { echo "staging route survived canonical termination: $route_path" >&2; exit 1; }
  done
  if /usr/local/sbin/stepanel-dbctl dump "$staging_database" > "$work/staging-database-after-termination.sql" 2>&1; then
    echo "staging database survived canonical termination: $staging_database" >&2
    exit 1
  fi
  echo "staging restore lifecycle cleanup passed (site, route, account and database removed)"
  staging_site=''
  fi
fi

  printf '%s\n' '[Service]' \
    'Environment=STEPANEL_LAB_DIRECT_ROOT_BROKER=1' \
    "Environment=STEPANEL_KILL_AT=$DATABASE_RESTORE_KILL_AT" > "$dropin"
  systemctl daemon-reload
  systemctl restart stepanel-worker.service
  systemctl is-active --quiet stepanel-worker.service
  wait_for_panel_ready
  before=$(systemctl show stepanel-worker.service -p MainPID --value)

  response=$(curl --fail --silent --show-error --max-time 30 \
    -H "Cookie: $cookie_header" \
    -H "X-CSRF-Token: $csrf" \
    -H 'Content-Type: application/json' \
    --data "$(python3 - "$database_backup_name" "$BACKUP_RECOVERY_SMOKE_SITE" "$database_name" <<'PY'
import json, sys
print(json.dumps({"backup": sys.argv[1], "site": sys.argv[2], "database": sys.argv[3], "confirm": "RESTORE_DATABASE"}))
PY
)" \
    "$PANEL/api/backups/restore-database")
  database_restore_job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^\"]*\)".*/\1/p')
  [[ -n "$database_restore_job_id" ]] || { echo "database restore request did not return a durable job: $response" >&2; exit 1; }

  killed=0
  for _ in $(seq 1 240); do
    current=$(systemctl show stepanel-worker.service -p MainPID --value)
    if [[ "$current" =~ ^[1-9][0-9]*$ && "$current" != "$before" ]]; then
      killed=1
      systemctl stop stepanel-worker.service || true
      rm -f -- "$dropin"
      systemctl daemon-reload
      systemctl start stepanel-worker.service
      wait_for_panel_ready
      break
    fi
    status=$(curl --fail --silent --show-error --max-time 10 \
      -H "Cookie: $cookie_header" "$PANEL/api/jobs/$database_restore_job_id")
    state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^\"]*\)".*/\1/p')
    case "$state" in
      completed)
        echo 'database restore completed before the injected kill boundary was observed' >&2
        exit 1
        ;;
      failed|dead-letter|cancelled)
        echo "database restore job ended in $state before the injected kill boundary: $status" >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  (( killed )) || { echo 'worker PID never changed; injected database restore process-kill boundary was not observed' >&2; exit 1; }

  state=''
  status=''
  for _ in $(seq 1 240); do
    status=$(curl --fail --silent --show-error --max-time 10 \
      -H "Cookie: $cookie_header" "$PANEL/api/jobs/$database_restore_job_id")
    state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^\"]*\)".*/\1/p')
    case "$state" in
      completed) break ;;
      failed|dead-letter|cancelled)
        echo "database restore recovery job $database_restore_job_id ended in $state: $status" >&2
        exit 1
        ;;
    esac
    sleep 1
  done
  [[ "$state" == completed ]] || { echo "database restore recovery job $database_restore_job_id did not complete: ${status:-}" >&2; exit 1; }
  echo "database restore recovery smoke passed (worker $before was killed at $DATABASE_RESTORE_KILL_AT)"
fi

# Finally exercise the irreversible lifecycle journal. The worker dies after
# the site-state step starts; the restarted worker must roll the journal
# forward and leave the site absent, with the verified backup retained.
# Save the authoritative document root before termination removes its
# placement record, so the postcondition checks the original path.
source_public_root=$(site_public_root "$BACKUP_RECOVERY_SMOKE_SITE")
# The isolated lab mode routes the read-only database inventory through the
# root broker because the disposable container may restrict privilege
# transitions for sudo and setuid subprocesses.
# Keep both panel and worker unprivileged so audit ownership and service
# privilege boundaries remain identical to production.
printf '%s\n' '[Service]' \
  'Environment=STEPANEL_LAB_DIRECT_ROOT_BROKER=1' \
  "Environment=STEPANEL_KILL_AT=$TERMINATE_KILL_AT" > "$dropin"
systemctl daemon-reload
systemctl restart stepanel-worker.service
systemctl is-active --quiet stepanel-worker.service
wait_for_panel_ready
before=$(systemctl show stepanel-worker.service -p MainPID --value)

response=$(curl --fail --silent --show-error --max-time 30 \
  -H "Cookie: $cookie_header" \
  -H "X-CSRF-Token: $csrf" \
  -H 'Content-Type: application/json' \
  --data "$(python3 - "$BACKUP_RECOVERY_SMOKE_SITE" <<'PY'
import json, sys
site = sys.argv[1]
print(json.dumps({"site": site, "confirmation": "DELETE " + site}))
PY
)" \
  "$PANEL/api/sites/terminate")
terminate_job_id=$(printf '%s' "$response" | sed -n 's/.*"job_id":"\([^"]*\)".*/\1/p')
[[ -n $terminate_job_id ]] || { echo "termination request did not return a durable job: $response" >&2; exit 1; }

killed=0
for _ in $(seq 1 240); do
  current=$(systemctl show stepanel-worker.service -p MainPID --value)
  if [[ "$current" =~ ^[1-9][0-9]*$ && "$current" != "$before" ]]; then
    killed=1
    systemctl stop stepanel-worker.service || true
    rm -f -- "$dropin"
    systemctl daemon-reload
    systemctl start stepanel-worker.service
    wait_for_panel_ready
    break
  fi
  status=$(curl --fail --silent --show-error --max-time 10 \
    -H "Cookie: $cookie_header" "$PANEL/api/jobs/$terminate_job_id")
  state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
  case "$state" in
    completed)
      echo 'termination completed before the injected kill boundary was observed' >&2
      exit 1
      ;;
    failed|dead-letter|cancelled)
      echo "termination job ended in $state before the injected kill boundary: $status" >&2
      exit 1
      ;;
  esac
  sleep 1
done
(( killed )) || { echo 'worker PID never changed; injected termination process-kill boundary was not observed' >&2; exit 1; }

state=''
status=''
for _ in $(seq 1 240); do
  status=$(curl --fail --silent --show-error --max-time 10 \
    -H "Cookie: $cookie_header" "$PANEL/api/jobs/$terminate_job_id")
  state=$(printf '%s' "$status" | sed -n 's/.*"state":"\([^"]*\)".*/\1/p')
  case "$state" in
    completed) break ;;
    failed|dead-letter|cancelled)
      echo "termination recovery job $terminate_job_id ended in $state: $status" >&2
      exit 1
      ;;
  esac
  sleep 1
done
[[ "$state" == completed ]] || { echo "termination recovery job $terminate_job_id did not complete: ${status:-}" >&2; exit 1; }
[[ ! -e $source_public_root ]] || { echo "terminated site document root still exists: $source_public_root" >&2; exit 1; }
echo "termination recovery smoke passed (worker $before was killed at $TERMINATE_KILL_AT)"
