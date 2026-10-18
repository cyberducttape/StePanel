#!/usr/bin/env bash
# Abrupt whole-guest loss in the middle of a mutating operation, driven by
# deploy/lab/local-kvm-certification.sh (phase abrupt-loss-operations):
#
#   abrupt-loss-operation-drill.sh SCENARIO prepare   build the scenario's site and data
#   abrupt-loss-operation-drill.sh SCENARIO start     begin the operation and return once
#                                                     it is at the boundary to kill
#   (the harness kills the QEMU guest and boots it again)
#   abrupt-loss-operation-drill.sh SCENARIO verify    check customer data, the site over
#                                                     HTTP, and the job outcome
#
# Scenarios and the outcome each verify requires:
#
#   file-restore     Guest dies at restore:commit, after the restored files are
#                    live but before the restore journal commits. The site must
#                    hold exactly the pre-restore or the backed-up content, never
#                    a mix, and must hold the backed-up content if the job
#                    completed. HTTP must serve whichever is on disk.
#   database-restore Guest dies while the dump is loading into the live
#                    database. If the job completed, every table matches the
#                    backup. Otherwise the job must name the pre-restore safety
#                    backup, and restoring that backup must return every table
#                    to its pre-restore state. The site's PHP page must report
#                    the database state.
#   git-deploy       Guest dies at deploy:activate, after the new release
#                    replaced the site but before the activation journal
#                    recorded it. The original release must be back, the job
#                    must report the interruption, and no activation journal
#                    may remain. A fresh deploy must then succeed.
#   control-plane    Guest dies while a client is updating a backup schedule in
#                    a loop. The stored value must be the last acknowledged
#                    write or the one in flight: no acknowledged write is lost
#                    and none appears that was never sent. Readiness, including
#                    the control-plane integrity check, must recover.
#
# The pause boundaries use STEPANEL_PAUSE_AT, which StePanel honors only with
# STEPANEL_UNSAFE_LAB=1. It is set in a /run systemd drop-in, so the reboot
# removes it. Run only on a disposable lab guest installed with --unsafe-lab.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'abrupt-loss operation drill must run as root' >&2; exit 1; }
scenario=${1:?usage: abrupt-loss-operation-drill.sh file-restore|database-restore|git-deploy|control-plane prepare|start|verify}
action=${2:?usage: abrupt-loss-operation-drill.sh SCENARIO prepare|start|verify}

: "${PANEL:=http://127.0.0.1:8090}"
: "${STEPANEL_ADMIN_USERNAME:=admin}"
: "${STEPANEL_ADMIN_PASSWORD:=ci-install-only-password}"
: "${STEPANEL_ADMIN_TOTP_SECRET:=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP}"
: "${DRILL_CONTENT_MB:=50}"
: "${DRILL_DB_DOUBLINGS:=18}"
: "${DRILL_LOAD_DELAY:=3}"
: "${DRILL_GIT_REPOSITORY:=https://github.com/octocat/Hello-World.git}"
: "${DRILL_GIT_REF:=master}"

grep -q '^STEPANEL_UNSAFE_LAB="\?1"\?$' /etc/ste-panel.env || {
  echo 'refusing to run: /etc/ste-panel.env is not an --unsafe-lab install' >&2
  exit 1
}

state_dir=/var/lib/stepanel-cert/$scenario
recovery_root=/var/www/sites/.stepanel-recovery
pause_dir=/var/lib/ste-panel/pause-markers
install -d -m 0700 "$state_dir"
site=drill-${scenario}
domain=$site.example.test
public=/var/www/sites/$site/public

service=stepanel.service
if systemctl is-enabled --quiet stepanel-worker.service 2>/dev/null; then
  service=stepanel-worker.service
fi
dropin_dir=/run/systemd/system/$service.d
dropin=$dropin_dir/abrupt-loss-drill.conf

work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
cookies=$work/cookies.txt

fail() { echo "$scenario $action: $*" >&2; exit 1; }

wait_panel_ready() {
  for _ in $(seq 1 300); do
    curl --fail --silent --max-time 5 "$PANEL/readyz" >/dev/null && return 0
    sleep 1
  done
  fail "panel readiness did not recover: $(curl --silent --max-time 5 "$PANEL/readyz" || true)"
}

login() {
  # A TOTP code is accepted once; start each login in a fresh 30 s window.
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
  rm -f -- "$cookies"
  curl --fail --silent --show-error --max-time 10 -c "$cookies" "$PANEL/login" >/dev/null
  curl --fail --silent --show-error --max-time 10 -L -b "$cookies" -c "$cookies" \
    --data-urlencode "username=$STEPANEL_ADMIN_USERNAME" \
    --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
    --data-urlencode "totp=$totp" "$PANEL/login" >/dev/null
  session=$(awk '$6 == "stepanel_session" {print $7}' "$cookies")
  csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$cookies")
  [[ -n $session && -n $csrf ]] || fail 'login did not issue session and CSRF cookies'
  cookie_header="stepanel_session=$session; stepanel_csrf=$csrf"
}

api() {
  local method=$1 path=$2 body=${3:-}
  if [[ -n $body ]]; then
    curl --fail --silent --show-error --max-time 60 -X "$method" -H "Cookie: $cookie_header" -H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json' --data "$body" "$PANEL$path"
  else
    curl --fail --silent --show-error --max-time 60 -X "$method" -H "Cookie: $cookie_header" "$PANEL$path"
  fi
}

json() { python3 -c "import json, sys; print(json.dumps(dict(zip(sys.argv[1::2], sys.argv[2::2]))))" "$@"; }
field() { python3 -c 'import json, sys; value = json.loads(sys.argv[1]).get(sys.argv[2], ""); print(value if isinstance(value, str) else json.dumps(value))' "$1" "$2"; }
job_id_of() { field "$1" job_id; }
job_state() { field "$(api GET "/api/jobs/$1")" state; }
job_error() { field "$(api GET "/api/jobs/$1")" error; }

wait_terminal() {
  local job=$1 state=''
  for _ in $(seq 1 900); do
    state=$(job_state "$job")
    case "$state" in completed|failed|dead-letter|cancelled) printf '%s' "$state"; return 0 ;; esac
    sleep 1
  done
  printf '%s' "${state:-unknown}"
  return 1
}

require_completed() {
  local job=$1 what=$2 final
  [[ -n $job ]] || fail "$what was not queued"
  final=$(wait_terminal "$job") || fail "$what job $job did not finish ($final)"
  [[ $final == completed ]] || fail "$what job $job ended $final: $(job_error "$job")"
}

create_site() {
  local response
  response=$(api POST /api/sites "$(json site "$site" template php)")
  require_completed "$(job_id_of "$response")" "site creation for $site"
}

latest_backup() {
  api GET "/api/backups?site=$site" | python3 -c 'import json, sys; items = json.load(sys.stdin).get("backups", []); print(items[0]["path"].rstrip("/").rsplit("/", 1)[-1] if items else "")'
}

backup_site() {
  local include=$1 response
  response=$(api POST /api/backups "{\"site\":\"$site\",\"include_databases\":$include}")
  require_completed "$(job_id_of "$response")" "backup of $site"
  latest_backup
}

# snapshot prints a sorted checksum list of every regular file under public,
# relative to it, so two snapshots compare equal only for identical trees.
snapshot() { (cd "$public" && find . -type f -print0 | sort -z | xargs -0 -r sha256sum); }

publish_route() {
  /usr/local/sbin/stepanel-sitectl seal "$site"
  /usr/local/sbin/stepanel-vhostctl apply "$site" "$domain"
}

# probe fetches a page of the site through the configured web server.
probe() {
  local path=$1 scheme=http port=80
  if [[ $(sed -n 's/^STEPANEL_WEBSERVER="\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' /etc/ste-panel.env) == caddy ]]; then
    scheme=https port=443
  fi
  for _ in $(seq 1 30); do
    if curl --fail --silent --insecure --location --max-time 5 --resolve "$domain:$port:127.0.0.1" "$scheme://$domain$path"; then
      return 0
    fi
    sleep 1
  done
  return 1
}

set_pause() {
  rm -rf -- "$pause_dir"
  mkdir -p "$dropin_dir"
  printf '%s\n' '[Service]' "Environment=STEPANEL_PAUSE_AT=$1" "Environment=STEPANEL_PAUSE_MARKER_DIR=$pause_dir" > "$dropin"
  systemctl daemon-reload
  systemctl restart "$service"
  wait_panel_ready
}

wait_pause() {
  local marker=$pause_dir/${1/:/-}
  for _ in $(seq 1 600); do
    [[ -f $marker ]] && { sync; return 0; }
    sleep 0.5
  done
  fail "operation never reached pause boundary $1"
}

clear_pause_after_reboot() {
  # The drop-in lived on /run; it must be gone after the reboot.
  [[ ! -e $dropin ]] || fail "pause drop-in $dropin survived the reboot"
  rm -rf -- "$pause_dir"
}

# --- file-restore ------------------------------------------------------------
write_file_state() {
  local label=$1 owner
  owner=$(stat -c %U:%G "$public")
  find "$public" -mindepth 1 -delete
  printf '<?php echo "DRILL_STATE_%s";\n' "$label" > "$public/index.php"
  for i in $(seq 1 $((DRILL_CONTENT_MB / 10))); do
    head -c $((10 << 20)) /dev/urandom > "$public/asset-$label-$i.bin"
  done
  chown -R "$owner" "$public"
}

file_restore() {
  case "$action" in
    prepare)
      login
      create_site
      write_file_state A
      snapshot > "$state_dir/backup.sha256"
      backup_site false > "$state_dir/backup-name"
      [[ -s $state_dir/backup-name ]] || fail 'backup of state A is not listed'
      write_file_state B
      snapshot > "$state_dir/before.sha256"
      publish_route
      probe / | grep -Fq DRILL_STATE_B || fail 'site does not serve state B before the restore'
      echo "prepared $site: backup holds state A, site serves state B"
      ;;
    start)
      set_pause "${DRILL_PAUSE_AT:-restore:commit}"
      login
      response=$(api POST /api/backups/restore-files "$(json site "$site" backup "$(<"$state_dir/backup-name")" confirm RESTORE_FILES)")
      job=$(job_id_of "$response")
      [[ -n $job ]] || fail "file restore was not queued: $response"
      printf '%s\n' "$job" > "$state_dir/job"
      sync
      wait_pause "${DRILL_PAUSE_AT:-restore:commit}"
      echo "file restore $job paused at ${DRILL_PAUSE_AT:-restore:commit}"
      ;;
    verify)
      clear_pause_after_reboot
      wait_panel_ready
      login
      job=$(<"$state_dir/job")
      final=$(wait_terminal "$job") || fail "interrupted restore $job did not finish ($final)"
      snapshot > "$work/after.sha256"
      if cmp -s "$work/after.sha256" "$state_dir/backup.sha256"; then
        on_disk=A
      elif cmp -s "$work/after.sha256" "$state_dir/before.sha256"; then
        on_disk=B
      else
        diff "$state_dir/backup.sha256" "$work/after.sha256" >&2 || true
        fail 'site content is neither the backed-up nor the pre-restore tree'
      fi
      [[ $final != completed || $on_disk == A ]] || fail "restore job completed but the site holds state $on_disk"
      probe / | grep -Fq "DRILL_STATE_$on_disk" || fail "site does not serve state $on_disk, which is on disk"
      response=$(api POST /api/backups/restore-files "$(json site "$site" backup "$(<"$state_dir/backup-name")" confirm RESTORE_FILES)")
      require_completed "$(job_id_of "$response")" 'fresh file restore'
      snapshot | cmp -s - "$state_dir/backup.sha256" || fail 'fresh file restore did not produce the backed-up tree'
      probe / | grep -Fq DRILL_STATE_A || fail 'site does not serve state A after the fresh restore'
      echo "interrupted file restore $job ended $final with state $on_disk intact; fresh restore served state A"
      ;;
  esac
}

# --- database-restore --------------------------------------------------------
db_name=$(printf '%s_db' "$site" | tr '-' '_')
db_user=$(printf '%s_u' "$site" | tr '-' '_')
db_password=drill-database-restore-password-1234

sql() {
  local client
  client=$(command -v mariadb || command -v mysql) || fail 'no MariaDB client'
  "$client" --no-defaults --protocol=socket -N -B "$db_name" -e "$1"
}

# db_checksum prints one line per table: name, row count, and a checksum over
# every column, so a partly restored database cannot match either state.
db_checksum() {
  local i
  for i in $(seq 1 8); do
    printf 't%s %s\n' "$i" "$(sql "SELECT COUNT(*), COALESCE(BIT_XOR(CRC32(CONCAT_WS('|', id, state, pad))), 0) FROM t$i" | tr '\t' ' ')"
  done
}

database_restore() {
  case "$action" in
    prepare)
      login
      create_site
      api POST /api/databases "$(json name "$db_name" user "$db_user" site "$site" password "$db_password" encoding utf8mb4)" >/dev/null \
        || fail "could not create managed database $db_name"
      {
        for i in $(seq 1 8); do
          printf 'CREATE TABLE t%s (id INT AUTO_INCREMENT PRIMARY KEY, state VARCHAR(16) NOT NULL, pad CHAR(64) NOT NULL);\n' "$i"
          printf "INSERT INTO t%s (state, pad) VALUES ('backup', SHA2(RAND(), 256));\n" "$i"
          for _ in $(seq 1 "$DRILL_DB_DOUBLINGS"); do
            printf "INSERT INTO t%s (state, pad) SELECT 'backup', SHA2(RAND(), 256) FROM t%s;\n" "$i" "$i"
          done
        done
      } | /usr/local/sbin/stepanel-dbctl restore-dump "$db_name" "$site"
      db_checksum > "$state_dir/backup.sum"
      backup_site true > "$state_dir/backup-name"
      [[ -s $state_dir/backup-name ]] || fail 'database backup is not listed'
      for i in $(seq 1 8); do sql "UPDATE t$i SET state = 'pre'"; done
      db_checksum > "$state_dir/before.sum"
      owner=$(stat -c %U:%G "$public")
      cat > "$public/index.php" <<PHP
<?php
\$db = new mysqli('localhost', '$db_user', '$db_password', '$db_name');
\$row = \$db->query('SELECT state FROM t1 WHERE id = 1')->fetch_row();
echo 'DRILL_DB_STATE=' . \$row[0];
PHP
      chown "$owner" "$public/index.php"
      publish_route
      probe / | grep -Fq DRILL_DB_STATE=pre || fail 'site does not report the pre-restore database state'
      echo "prepared $db_name with $((8 << DRILL_DB_DOUBLINGS)) rows; backup holds state 'backup', database holds 'pre'"
      ;;
    start)
      login
      response=$(api POST /api/backups/restore-database "$(json site "$site" backup "$(<"$state_dir/backup-name")" database "$db_name" confirm RESTORE_DATABASE)")
      job=$(job_id_of "$response")
      [[ -n $job ]] || fail "database restore was not queued: $response"
      printf '%s\n' "$job" > "$state_dir/job"
      sync
      # The restore writes this marker just before it starts loading the dump.
      for _ in $(seq 1 1200); do
        [[ -f $recovery_root/database-restore-$job.json ]] && break
        [[ $(job_state "$job") == running || $(job_state "$job") == queued ]] || fail "database restore ended $(job_state "$job") before its load started"
        sleep 0.5
      done
      [[ -f $recovery_root/database-restore-$job.json ]] || fail 'database restore load never started'
      field "$(<"$recovery_root/database-restore-$job.json")" safety_backup > "$state_dir/first-safety"
      [[ -s $state_dir/first-safety ]] || fail 'database restore marker does not name its safety backup'
      sleep "$DRILL_LOAD_DELAY"
      [[ $(job_state "$job") == running ]] || fail "database restore finished within ${DRILL_LOAD_DELAY}s of starting its load; raise DRILL_DB_DOUBLINGS so the kill lands mid-load"
      sync
      echo "database restore $job is loading into $db_name"
      ;;
    verify)
      wait_panel_ready
      # The helper loads into a shadow database and swaps it in, so the live
      # tables are only ever the pre-restore or the restored state, even
      # while a retry of the job is loading.
      db_checksum > "$work/boot.sum"
      if cmp -s "$work/boot.sum" "$state_dir/before.sum"; then
        live='untouched'
      elif cmp -s "$work/boot.sum" "$state_dir/backup.sum"; then
        live='fully restored'
      else
        fail 'database holds neither its pre-restore nor its restored state after the kill'
      fi
      login
      job=$(<"$state_dir/job")
      first_safety=$(<"$state_dir/first-safety")
      final=$(wait_terminal "$job") || fail "interrupted database restore $job did not finish ($final)"
      db_checksum > "$work/after.sum"
      leftovers=$(find /var/lib/stepanel-privileged/db-managed -maxdepth 1 -name 'stepanel_restore_*' -printf '%f ')
      [[ -z $leftovers ]] || fail "restore records remain after the job ended: $leftovers"
      [[ -z $(sql "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE 'stepanel\\_%'") ]] || fail 'restore shadow or retired databases remain after the job ended'
      if [[ $final == completed ]]; then
        cmp -s "$work/after.sum" "$state_dir/backup.sum" || fail 'restore job completed but the database does not match the backup'
        [[ ! -e $recovery_root/database-restore-$job.json ]] || fail 'completed restore left its in-progress marker'
        result_safety=$(python3 -c 'import json, os, sys; print(os.path.basename((json.loads(sys.argv[1]).get("restore") or {}).get("safety_backup", "")))' "$(api GET "/api/jobs/$job")")
        [[ $result_safety == "$first_safety" ]] || fail "retried restore reports safety backup '$result_safety', not the first attempt's $first_safety"
        probe / | grep -Fq DRILL_DB_STATE=backup || fail 'site does not report the restored database state'
        echo "interrupted database restore $job left the database $live after the kill and completed on retry with the first safety backup $first_safety; all 8 tables match the backup"
        return
      fi
      error=$(job_error "$job")
      [[ $error == *'the live database was not changed'* || $error == *'did not report completion'* ]] || fail "database restore $job ended $final without its restore outcome: $error"
      [[ $error == *"$first_safety"* ]] || fail "database restore error does not name the first safety backup $first_safety: $error"
      cmp -s "$work/after.sum" "$state_dir/before.sum" || cmp -s "$work/after.sum" "$state_dir/backup.sum" || fail 'failed restore left the database in neither state'
      api POST /api/backups/verify "$(json site "$site" backup "$first_safety")" >/dev/null || fail "named safety backup $first_safety does not verify"
      response=$(api POST /api/backups/restore-database "$(json site "$site" backup "$first_safety" database "$db_name" confirm RESTORE_DATABASE)")
      require_completed "$(job_id_of "$response")" "restore of safety backup $first_safety"
      db_checksum | cmp -s - "$state_dir/before.sum" || fail "restoring safety backup $first_safety did not return the database to its pre-restore state"
      probe / | grep -Fq DRILL_DB_STATE=pre || fail 'site does not report the recovered pre-restore state'
      echo "interrupted database restore $job left the database $live and ended $final; restoring named safety backup $first_safety recovered all 8 tables"
      ;;
  esac
}

# --- git-deploy ----------------------------------------------------------------
git_deploy() {
  case "$action" in
    prepare)
      login
      create_site
      owner=$(stat -c %U:%G "$public")
      printf '<?php echo "DRILL_GIT_ORIGINAL";\n' > "$public/index.php"
      printf '%s\n' 'original release survives an interrupted deployment' > "$public/original-release.txt"
      chown -R "$owner" "$public"
      snapshot > "$state_dir/original.sha256"
      publish_route
      probe / | grep -Fq DRILL_GIT_ORIGINAL || fail 'site does not serve the original release'
      echo "prepared $site with its original release"
      ;;
    start)
      set_pause "${DRILL_PAUSE_AT:-deploy:activate}"
      login
      response=$(api POST /api/sites/git-deploy "$(json site "$site" repository "$DRILL_GIT_REPOSITORY" ref "$DRILL_GIT_REF")")
      job=$(job_id_of "$response")
      [[ -n $job ]] || fail "Git deploy was not queued: $response"
      printf '%s\n' "$job" > "$state_dir/job"
      sync
      wait_pause "${DRILL_PAUSE_AT:-deploy:activate}"
      echo "Git deploy $job paused at ${DRILL_PAUSE_AT:-deploy:activate}"
      ;;
    verify)
      clear_pause_after_reboot
      wait_panel_ready
      login
      job=$(<"$state_dir/job")
      final=$(wait_terminal "$job") || fail "interrupted deploy $job did not finish ($final)"
      [[ $final == failed || $final == dead-letter ]] || fail "interrupted deploy $job ended $final; activation was not committed"
      [[ $(job_error "$job") == *interrupted* ]] || fail "interrupted deploy $job does not report the interruption: $(job_error "$job")"
      snapshot | cmp -s - "$state_dir/original.sha256" || fail 'site does not hold exactly the original release'
      probe / | grep -Fq DRILL_GIT_ORIGINAL || fail 'site does not serve the original release'
      if find "$recovery_root" -maxdepth 1 -type f -name 'release-activation-*.json' -print -quit | grep -q .; then
        fail 'a release activation journal remains after recovery'
      fi
      response=$(api POST /api/sites/git-deploy "$(json site "$site" repository "$DRILL_GIT_REPOSITORY" ref "$DRILL_GIT_REF")")
      require_completed "$(job_id_of "$response")" 'fresh Git deploy'
      echo "interrupted deploy $job ended $final with the original release served; fresh deploy completed"
      ;;
  esac
}

# --- control-plane -------------------------------------------------------------
control_plane() {
  local log=$state_dir/writes.log
  case "$action" in
    prepare)
      login
      create_site
      : > "$log"
      echo "prepared $site for schedule writes"
      ;;
    start)
      login
      : > "$log"
      # The writer survives this script and runs until the guest dies. It
      # logs each attempt before sending and each acknowledgement after the
      # 200 response, syncing the log every time.
      setsid python3 - "$PANEL" "$cookie_header" "$csrf" "$site" "$log" > "$state_dir/writer.out" 2>&1 <<'PY' &
import json, os, sys, urllib.request
panel, cookie, csrf, site, path = sys.argv[1:]
log = open(path, "a")
def record(line):
    log.write(line + "\n"); log.flush(); os.fsync(log.fileno())
for n in range(1, 10070):
    record(f"attempt {n}")
    body = json.dumps({"site": site, "interval_minutes": 5 + n, "keep_last": 7}).encode()
    request = urllib.request.Request(panel + "/api/backup-schedules", data=body, method="PUT",
        headers={"Cookie": cookie, "X-CSRF-Token": csrf, "Content-Type": "application/json"})
    with urllib.request.urlopen(request, timeout=30) as response:
        if response.status != 200:
            sys.exit(f"write {n} returned HTTP {response.status}")
    record(f"ack {n}")
PY
      for _ in $(seq 1 120); do
        (( $(grep -c '^ack ' "$log" || true) >= 20 )) && { echo "schedule writer running ($(grep -c '^ack ' "$log") acknowledged)"; exit 0; }
        sleep 0.5
      done
      fail "schedule writer did not get 20 acknowledgements: $(tail -3 "$state_dir/writer.out" 2>/dev/null)"
      ;;
    verify)
      wait_panel_ready
      readyz=$(curl --silent --max-time 10 "$PANEL/readyz")
      [[ $(field "$readyz" ready) == true ]] || fail "readiness did not recover: $readyz"
      [[ $(field "$(field "$readyz" checks)" control_plane_integrity) == *'"ready": true'* ]] || fail "control-plane integrity check is not ready: $readyz"
      login
      acked=$(sed -n 's/^ack //p' "$log" | tail -1)
      attempted=$(sed -n 's/^attempt //p' "$log" | tail -1)
      [[ -n $acked && -n $attempted ]] || fail 'write log is empty'
      stored=$(api GET /api/backup-schedules | python3 -c 'import json, sys; site = sys.argv[1]; print(next((s["interval_minutes"] - 5 for s in json.load(sys.stdin)["schedules"] if s["site"] == site), ""))' "$site")
      [[ -n $stored ]] || fail "schedule for $site was lost after $acked acknowledged writes"
      (( stored >= acked )) || fail "acknowledged write $acked was lost: the stored schedule is write $stored"
      (( stored <= attempted )) || fail "stored write $stored was never sent (last attempt $attempted)"
      api DELETE "/api/backup-schedules?site=$site" >/dev/null
      echo "schedule holds write $stored after $acked acknowledged and $attempted attempted; control-plane integrity ready"
      ;;
  esac
}

case "$action" in prepare|start|verify) ;; *) fail "unknown action $action" ;; esac
case "$scenario" in
  file-restore) file_restore ;;
  database-restore) database_restore ;;
  git-deploy) git_deploy ;;
  control-plane) control_plane ;;
  *) fail "unknown scenario $scenario" ;;
esac
