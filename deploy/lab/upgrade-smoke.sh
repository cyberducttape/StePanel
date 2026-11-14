#!/usr/bin/env bash
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'upgrade smoke must run as root' >&2; exit 1; }
previous_root=${1:?previous release tree is required}
candidate_root=${2:?candidate release tree is required}
broken_root=${3:-}
# STEPANEL_UPGRADE_INTERRUPT_AT names an installer journal phase. When set,
# the first candidate install is killed with SIGKILL as soon as that phase
# starts, so no rollback runs, and the smoke then requires a plain rerun of
# the candidate installer to complete the upgrade.
interrupt_at=${STEPANEL_UPGRADE_INTERRUPT_AT:-}
[[ -z $interrupt_at || $interrupt_at =~ ^[a-z_]+$ ]] || { echo "invalid installer phase: $interrupt_at" >&2; exit 1; }
# STEPANEL_UPGRADE_ROLLBACK=1 finally undoes the committed upgrade with the
# documented procedure (docs/COMPATIBILITY.md), proves the previous release
# runs again, and upgrades once more.
post_upgrade_rollback=${STEPANEL_UPGRADE_ROLLBACK:-0}
[[ $post_upgrade_rollback == 0 || $post_upgrade_rollback == 1 ]] || { echo 'STEPANEL_UPGRADE_ROLLBACK must be 0 or 1' >&2; exit 1; }
previous_version=${STEPANEL_PREVIOUS_VERSION:?STEPANEL_PREVIOUS_VERSION (for example 0.7.0) is required}
[[ $previous_version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "invalid previous version: $previous_version" >&2; exit 1; }

[[ -x "$previous_root/install.sh" && -x "$previous_root/stepanel" ]] || { echo "invalid previous release tree: $previous_root" >&2; exit 1; }
[[ -x "$candidate_root/install.sh" && -x "$candidate_root/stepanel" && -x "$candidate_root/stepanel-root" ]] || { echo "invalid candidate release tree: $candidate_root" >&2; exit 1; }
if [[ -n "$broken_root" && ( ! -x "$broken_root/install.sh" || ! -x "$broken_root/stepanel" ) ]]; then
  echo "invalid deliberately broken release tree: $broken_root" >&2
  exit 1
fi
# Lab only: the smoke installs and replaces StePanel and prints diagnostics
# on failure, so it refuses a host that already has an installation.
if [[ -e /etc/ste-panel.env || -e /opt/stepanel ]]; then
  echo 'upgrade smoke refuses a host with an existing StePanel installation; run it on a disposable host' >&2
  exit 1
fi
if [[ ! -f /sys/fs/cgroup/cgroup.controllers && ! -d /sys/fs/cgroup/systemd ]]; then
  echo 'upgrade smoke requires a systemd-compatible cgroup hierarchy' >&2
  exit 77
fi

export STEPANEL_ENV=production
export STEPANEL_LISTEN=127.0.0.1:8090
export STEPANEL_TLS_TERMINATED=1
export STEPANEL_ADMIN_PASSWORD=ci-upgrade-only-password
export STEPANEL_ADMIN_TOTP_SECRET=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP
export STEPANEL_SESSION_SECRET=ci-upgrade-session-secret-012345678901234567890123
export STEPANEL_AUDIT_KEY=ci-upgrade-audit-key-012345678901234567890123456789
export STEPANEL_ACCOUNT_KEY=ci-upgrade-account-key-012345678901234567890123456
# This disposable host has no project quota filesystem; production installs
# retain the default quota enforcement.
export STEPANEL_SKIP_QUOTA_CHECK=1
# Keep the systemd credential file non-empty even though local socket database
# administration does not use a database password. The service deliberately
# rejects an invalid credential file during startup.
export STEPANEL_DB_PASSWORD=ci-upgrade-db-credential
export STEPANEL_DB_ENGINE=${STEPANEL_DB_ENGINE:-mariadb}
export STEPANEL_WEBSERVER=${STEPANEL_WEBSERVER:-caddy}
export STEPANEL_DB_VERSION=default
export STEPANEL_PANEL_HOSTNAME=panel.example.test
export STEPANEL_INSTALL_DB_ADMIN=0
export STEPANEL_INSTALL_MAIL=0
export STEPANEL_INSTALL_FTP=0
export STEPANEL_INSTALL_NODE=0
export STEPANEL_INSTALL_SECURITY=0
export STEPANEL_REQUIRE_OFFSITE_BACKUP=1
export STEPANEL_OFFSITE_TARGET=local:/tmp/stepanel-offsite

if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y rclone
else
  dnf install -y epel-release
  dnf install -y rclone
fi

cd "$previous_root"
# Older installers ignore arguments; current ones require the explicit lab opt-in.
./install.sh --unsafe-lab
systemctl is-active --quiet stepanel.service
# The disposable offsite target is a configured rclone local remote. Without
# this file, rclone interprets `local:/path` as an unknown remote and the
# recovery smoke would fail before testing backup durability.
install -d -m 0755 -o root -g root /etc/stepanel
printf '[local]\ntype = local\n' > /etc/stepanel/rclone.conf
chown root:root /etc/stepanel/rclone.conf
chmod 0644 /etc/stepanel/rclone.conf
export RCLONE_CONFIG=/etc/stepanel/rclone.conf
# Avoid taking the running daemon's process lock just to verify the immutable
# previous-release build identity; the release version is compiled into the executable.
grep -aFq "$previous_version" /opt/stepanel/stepanel || { echo "installed binary is not release $previous_version" >&2; exit 1; }
curl --fail --silent --max-time 5 http://127.0.0.1:8090/readyz >/dev/null

# Prove the previous release's audit chain before the candidate installer touches the
# service. If the candidate later reports a signature failure, this separates
# an incompatible legacy state from state mutation during the upgrade.
"$previous_root/stepanel" verify-audit /var/lib/ste-panel/audit.jsonl

# Exercise the previous release's state path before the candidate opens the durable database.
install -d -m 0750 -o stepanel -g stepanel /var/lib/ste-panel
printf '%s\n' '[]' > /var/lib/ste-panel/jobs.json
chown stepanel:stepanel /var/lib/ste-panel/jobs.json
chmod 0600 /var/lib/ste-panel/jobs.json

# Upgrade a host containing actual customer-like data, not only an empty
# control plane. The legacy layout is intentionally retained across the N-1
# upgrade and documented downgrade.
legacy_site=upgrade-customer
legacy_site_root="/var/www/sites/$legacy_site/public"
legacy_site_marker=customer-content-survives-upgrade-and-rollback
install -d -m 0755 "$legacy_site_root"
printf '%s\n' "$legacy_site_marker" > "$legacy_site_root/index.html"
legacy_db=stepanel_upgrade_customer
mariadb --protocol=socket --user=root --batch --skip-column-names <<SQL
CREATE DATABASE $legacy_db;
CREATE TABLE $legacy_db.recovery_fixture (value VARCHAR(128) NOT NULL);
INSERT INTO $legacy_db.recovery_fixture VALUES ('customer-database-survives-upgrade-and-rollback');
SQL
assert_populated_upgrade_fixture() {
  [[ $(<"$legacy_site_root/index.html") == "$legacy_site_marker" ]] || {
    echo 'upgrade or rollback changed the existing customer site marker' >&2
    return 1
  }
  local database_value
  database_value=$(mariadb --protocol=socket --user=root --batch --skip-column-names \
    --execute "SELECT value FROM $legacy_db.recovery_fixture")
  [[ $database_value == customer-database-survives-upgrade-and-rollback ]] || {
    echo 'upgrade or rollback changed the existing customer database fixture' >&2
    return 1
  }
}

web_service=apache2
[[ $STEPANEL_WEBSERVER != caddy ]] || web_service=caddy
[[ $STEPANEL_WEBSERVER != openlitespeed ]] || web_service=lsws
web_active_before_candidate=$(systemctl is-active "$web_service")
web_enabled_before_candidate=$(systemctl is-enabled "$web_service")

# Operators configure these runtime values in /etc/ste-panel.env. The
# candidate installer must preserve them while regenerating installer-owned
# settings. Use non-secret values with no external side effects in this lab.
printf '%s\n' 'STEPANEL_GIT_ALLOWED_HOSTS="git.example.test,code.example.test"' >> /etc/ste-panel.env
printf '%s\n' 'STEPANEL_HSTS_PRELOAD="0"' >> /etc/ste-panel.env
chmod 0600 /etc/ste-panel.env

wait_ready() {
  local label=$1
  for _ in {1..30}; do
    curl --fail --silent --max-time 2 http://127.0.0.1:8090/readyz >/dev/null && return 0
    sleep 1
  done
  echo "$label did not become ready" >&2
  curl --silent --show-error --max-time 5 http://127.0.0.1:8090/readyz >&2 || true
  return 1
}

# interrupt_candidate_install PHASE starts the candidate installer in its own
# process group and kills the whole group once its journal records PHASE.
interrupt_candidate_install() {
  local phase=$1 marker journal='' pid pgid log=/tmp/stepanel-interrupted-install.log
  marker=$(mktemp)
  (cd "$candidate_root" && exec setsid ./install.sh --unsafe-lab) >"$log" 2>&1 &
  pid=$!
  # Package installation can take minutes; the phase itself may be short,
  # so poll finely.
  for _ in {1..12000}; do
    [[ -n $journal ]] || journal=$(find /var/lib/stepanel/install-journal -maxdepth 1 -name '*.tsv' -newer "$marker" -print -quit 2>/dev/null || true)
    if [[ -n $journal ]] && grep -q $'\t'"$phase"$'\tstarted\t' "$journal"; then
      break
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "candidate installer exited before reaching phase $phase" >&2
      tail -n 40 "$log" >&2 || true
      return 1
    fi
    sleep 0.1
  done
  if [[ -z $journal ]] || ! grep -q $'\t'"$phase"$'\tstarted\t' "$journal"; then
    echo "candidate installer never reached phase $phase" >&2
    return 1
  fi
  pgid=$(ps -o pgid= -p "$pid" | tr -d ' ')
  [[ $pgid =~ ^[0-9]+$ && $pgid != 1 ]] || { echo "could not find the installer process group" >&2; return 1; }
  kill -KILL -- "-$pgid"
  wait "$pid" 2>/dev/null || true
  rm -f -- "$marker"
  # A kill that landed after the commit record would not test an interrupted
  # upgrade.
  if grep -q $'\tcommit\t' "$journal"; then
    echo "installer committed before it was killed; the interruption was not exercised" >&2
    return 1
  fi
  echo "candidate installer killed at phase $phase; journal $journal:"
  cut -f2-4 "$journal"
}

if [[ -n $interrupt_at ]]; then
  interrupt_candidate_install "$interrupt_at"
fi

cd "$candidate_root"
if ! ./install.sh --unsafe-lab; then
  # Key names only: the environment holds the session, audit and account
  # keys, which must never reach CI logs.
  echo 'candidate installer failed; runtime environment keys (values withheld):' >&2
  sed -n -e 's/^\([A-Z_][A-Z0-9_]*\)=$/  \1 empty/p' -e 's/^\([A-Z_][A-Z0-9_]*\)=.\{1,\}/  \1 set/p' /etc/ste-panel.env >&2 || true
  systemctl show stepanel.service -p FragmentPath -p ExecStart -p EnvironmentFiles -p MainPID --no-pager >&2 || true
  echo 'candidate audit files:' >&2
  stat -c '%n mode=%a uid=%u gid=%g size=%s' /var/lib/ste-panel/audit.jsonl /var/lib/ste-panel/audit.jsonl.state /var/lib/ste-panel/audit.jsonl.lock 2>&1 || true
  sha256sum /var/lib/ste-panel/audit.jsonl /var/lib/ste-panel/audit.jsonl.state 2>&1 || true
  sed -n '1,3p' /var/lib/ste-panel/audit.jsonl.state 2>&1 || true
  echo 'candidate audit verification:' >&2
  "$candidate_root/stepanel" verify-audit /var/lib/ste-panel/audit.jsonl 2>&1 || true
  exit 1
fi
systemctl is-active --quiet stepanel.service stepanel-worker.service
curl --fail --silent --max-time 5 http://127.0.0.1:8090/readyz >/dev/null
assert_populated_upgrade_fixture
test -s /var/lib/ste-panel/stepanel-control.db
grep -Fxq 'STEPANEL_GIT_ALLOWED_HOSTS="git.example.test,code.example.test"' /etc/ste-panel.env || {
  echo 'candidate upgrade dropped operator-managed Git host configuration' >&2
  exit 1
}
grep -Fxq 'STEPANEL_HSTS_PRELOAD="0"' /etc/ste-panel.env || {
  echo 'candidate upgrade dropped operator-managed HSTS configuration' >&2
  exit 1
}

# The installed service environment is root-owned. Load it for the CLI so the
# commands examine the production paths rather than development defaults.
set -a
# shellcheck disable=SC1091
set +u
. /etc/ste-panel.env
set -u
set +a
/opt/stepanel/stepanel dr-check >/tmp/stepanel-upgrade-dr.json
/opt/stepanel/stepanel backup-control-plane /tmp/stepanel-upgrade-control.db
/opt/stepanel/stepanel restore-control-plane /tmp/stepanel-upgrade-control.db --dry-run

# The upgrade drain must not cut off a running job. Hold a backup in the
# worker (lab pause injection), then reinstall the candidate with a short
# drain wait: the installer must refuse before stopping anything, leave the
# release serving and accepting changes, and the backup must still finish.
drain_cookies=$(mktemp)
drain_login() {
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
  rm -f -- "$drain_cookies"
  curl --fail --silent --show-error --max-time 10 -c "$drain_cookies" http://127.0.0.1:8090/login >/dev/null
  curl --fail --silent --show-error --max-time 10 -L -b "$drain_cookies" -c "$drain_cookies" \
    --data-urlencode "username=${STEPANEL_ADMIN_USERNAME:-admin}" \
    --data-urlencode "password=$STEPANEL_ADMIN_PASSWORD" \
    --data-urlencode "totp=$totp" http://127.0.0.1:8090/login >/dev/null
}
drain_api() {
  local method=$1 path=$2 body=${3:-} csrf
  local -a data=()
  csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$drain_cookies")
  [[ -z $body ]] || data=(-H 'Content-Type: application/json' --data "$body")
  curl --fail --silent --show-error --max-time 60 -X "$method" -b "$drain_cookies" -H "X-CSRF-Token: $csrf" \
    "${data[@]}" "http://127.0.0.1:8090$path"
}
drain_job_state() { drain_api GET "/api/jobs/$1" | python3 -c 'import json, sys; print(json.load(sys.stdin).get("state", ""))'; }
drain_dropin=/run/systemd/system/stepanel-worker.service.d/upgrade-drain-smoke.conf
drain_pause_dir=/var/lib/ste-panel/pause-markers
rm -rf -- "$drain_pause_dir"
install -d -m 0755 "$(dirname "$drain_dropin")"
printf '%s\n' '[Service]' 'Environment=STEPANEL_UNSAFE_LAB=1' 'Environment=STEPANEL_PAUSE_AT=backup:init' "Environment=STEPANEL_PAUSE_MARKER_DIR=$drain_pause_dir" > "$drain_dropin"
systemctl daemon-reload
systemctl restart stepanel-worker.service
wait_ready 'the candidate with a held worker'
drain_login
drain_job=$(drain_api POST /api/backups "{\"site\":\"$legacy_site\",\"include_databases\":false}" | python3 -c 'import json, sys; print(json.load(sys.stdin)["job_id"])')
for _ in $(seq 1 120); do [[ -f $drain_pause_dir/backup-init ]] && break; sleep 0.5; done
[[ -f $drain_pause_dir/backup-init ]] || { echo "backup $drain_job never reached its held point" >&2; exit 1; }
[[ $(drain_job_state "$drain_job") == running ]] || { echo "held backup $drain_job is not running" >&2; exit 1; }
binary_before_drain=$(sha256sum /opt/stepanel/stepanel)
worker_before_drain=$(systemctl show --property=MainPID --value stepanel-worker.service)
if (cd "$candidate_root" && STEPANEL_UPGRADE_DRAIN_SECONDS=20 ./install.sh --unsafe-lab) > /tmp/stepanel-drain-refusal.log 2>&1; then
  echo 'an upgrade started although a job was running past the drain wait' >&2
  exit 1
fi
grep -q 'still running after' /tmp/stepanel-drain-refusal.log || { echo 'the upgrade failed, but not by the drain:' >&2; tail -20 /tmp/stepanel-drain-refusal.log >&2; exit 1; }
systemctl is-active --quiet stepanel.service stepanel-worker.service || { echo 'a refused upgrade stopped StePanel' >&2; exit 1; }
[[ $(sha256sum /opt/stepanel/stepanel) == "$binary_before_drain" ]] || { echo 'a refused upgrade replaced the binary' >&2; exit 1; }
[[ $(drain_job_state "$drain_job") == running ]] || { echo "a refused upgrade disturbed backup $drain_job" >&2; exit 1; }
[[ $(systemctl show --property=MainPID --value stepanel-worker.service) == "$worker_before_drain" ]] || { echo 'a refused upgrade restarted the worker' >&2; exit 1; }
# The refused drain is cleared (the gate caches it for a second): the panel
# accepts changes again. The held backup keeps its site locked, so probe with
# a change for a site that does not exist: the drain gate would answer 503
# before the handler; once cleared, the handler rejects the site with 422.
sleep 2
drain_csrf=$(awk '$6 == "stepanel_csrf" {print $7}' "$drain_cookies")
drain_probe=$(curl --silent --show-error --max-time 30 -o /dev/null -w '%{http_code}' -X PUT -b "$drain_cookies" \
  -H "X-CSRF-Token: $drain_csrf" -H 'Content-Type: application/json' \
  --data '{"site":"drain-probe-missing","interval_minutes":60,"keep_last":2}' http://127.0.0.1:8090/api/backup-schedules)
[[ $drain_probe == 422 ]] || { echo "the panel refuses changes after a refused upgrade (HTTP $drain_probe)" >&2; exit 1; }
# Release the held backup: the pause holds until the process is killed, and
# crash recovery resumes the job in the restarted worker.
rm -f -- "$drain_dropin"
systemctl daemon-reload
systemctl kill --kill-who=main --signal=SIGKILL stepanel-worker.service
systemctl restart stepanel-worker.service
wait_ready 'the candidate after releasing the held worker'
drain_state=''
for _ in $(seq 1 180); do
  drain_state=$(drain_job_state "$drain_job" || true)
  [[ $drain_state == queued || $drain_state == running || -z $drain_state ]] || break
  sleep 2
done
[[ $drain_state == completed ]] || { echo "backup $drain_job ended ${drain_state:-unknown} after the refused upgrade" >&2; exit 1; }
rm -f -- "$drain_cookies"
assert_populated_upgrade_fixture
echo 'upgrade drain refused while a job ran, and the job completed afterwards'

# Exercise the installer's transaction rollback using a release tree whose
# service binary damages the control-plane database and fails its
# post-install health check (deploy/lab/broken-candidate.sh). The previously
# upgraded candidate must be active and ready on the restored database.
if [[ -n "$broken_root" ]]; then
  candidate_version=$("$candidate_root/stepanel" version | awk 'NR == 1 { print $2 }')
  control_db=/var/lib/ste-panel/stepanel-control.db
  rm -f "$control_db".failed-upgrade-*
  if (cd "$broken_root" && ./install.sh --unsafe-lab); then
    echo 'broken candidate unexpectedly installed successfully' >&2
    exit 1
  fi
  systemctl is-active --quiet stepanel.service stepanel-worker.service
  [[ $(systemctl is-active "$web_service") == "$web_active_before_candidate" ]] || {
    echo "installer rollback did not restore $web_service active state" >&2
    exit 1
  }
  [[ $(systemctl is-enabled "$web_service") == "$web_enabled_before_candidate" ]] || {
    echo "installer rollback did not restore $web_service enabled state" >&2
    exit 1
  }
  assert_populated_upgrade_fixture
  /opt/stepanel/stepanel version | grep -Fx "StePanel $candidate_version"
  ready_after_rollback=0
  for _ in {1..30}; do
    if curl --fail --silent --max-time 2 http://127.0.0.1:8090/readyz >/dev/null; then
      ready_after_rollback=1
      break
    fi
    sleep 1
  done
  if (( ready_after_rollback != 1 )); then
    echo 'The previous release did not become ready after installer rollback.' >&2
    curl --silent --show-error --max-time 5 http://127.0.0.1:8090/readyz >&2 || true
    echo >&2
    exit 1
  fi
  # The broken candidate really did damage the database (its copy was kept
  # aside), and the database now in place is the sound pre-upgrade one.
  compgen -G "$control_db.failed-upgrade-*" >/dev/null || { echo 'broken candidate never reached the database; rollback was not exercised' >&2; exit 1; }
  /opt/stepanel/stepanel restore-control-plane "$control_db" --dry-run
fi

# Undo the committed upgrade as docs/COMPATIBILITY.md instructs: restore the
# previous release's configuration (its installer may refuse the new one) and
# its pre-migration database snapshot, then reinstall it. A release from
# before the SQLite control plane (v0.6.0, v0.7.0) reads its own state files,
# which the upgrade leaves in place, and predates the root broker, which must
# be disabled so it does not return at the next boot.
if (( post_upgrade_rollback )); then
  control_db=/var/lib/ste-panel/stepanel-control.db
  # Backups the candidate keeps in per-site directories (at least the one the
  # drain check took) must survive the downgrade and the repeat upgrade.
  backup_root=/var/backups/stepanel
  mapfile -t layout_backups < <(find "$backup_root/sites" -mindepth 2 -maxdepth 2 -type d ! -name '.*' -printf '%f\n' 2>/dev/null | sort)
  (( ${#layout_backups[@]} > 0 )) || { echo 'no backup in per-site directories to carry through the downgrade' >&2; exit 1; }
  echo "carrying ${#layout_backups[@]} backup(s) through the downgrade: ${layout_backups[*]}"
  # The copy to restore is the one made when the release now installed
  # replaced the previous one.
  installed_sha=$(sha256sum /opt/stepanel/stepanel | cut -d' ' -f1)
  marker=$(grep -lFx "replaced_by_sha256=$installed_sha" /var/lib/stepanel/upgrade-rollback/*/upgrade 2>/dev/null | sort | tail -n 1 || true)
  [[ -n $marker ]] || { echo 'the upgrade kept no copy of the previous release configuration' >&2; exit 1; }
  echo "restoring the previous release configuration from $(dirname "$marker")"
  systemctl stop stepanel.service stepanel-worker.service stepanel-root-broker.service
  install -m 0600 -o root -g root "$(dirname "$marker")/ste-panel.env" /etc/ste-panel.env
  aside=/var/lib/ste-panel/post-upgrade-$(date -u +%Y%m%dT%H%M%SZ)
  install -d -m 0700 -o root -g root "$aside"
  for file in "$control_db" "$control_db-wal" "$control_db-shm"; do
    [[ ! -e $file ]] || cp -p -- "$file" "$aside/"
  done
  snapshot=$(find "$control_db.snapshots" -maxdepth 1 -name 'pre-migration-v*.db' -printf '%T@ %p\n' 2>/dev/null | sort -n | tail -n 1 | cut -d' ' -f2- || true)
  if [[ -n $snapshot ]]; then
    echo "restoring pre-migration snapshot $snapshot"
    install -m 0600 -o stepanel -g stepanel -- "$snapshot" "$control_db"
  else
    echo 'no pre-migration snapshot: the previous release predates the SQLite control plane'
    rm -f -- "$control_db"
  fi
  rm -f -- "$control_db-wal" "$control_db-shm"
  # The previous release reads backups only from the backup root; the
  # installed (current) release moves them back out of per-site directories.
  runuser -u stepanel -- /opt/stepanel/stepanel backup-layout flatten /var/backups/stepanel
  if [[ ! -f $previous_root/deploy/stepanel-root-broker.service ]]; then
    systemctl disable stepanel-root-broker.service
  fi
  (cd "$previous_root" && ./install.sh --unsafe-lab)
  systemctl is-active --quiet stepanel.service
  grep -aFq "$previous_version" /opt/stepanel/stepanel || { echo "rollback did not reinstall release $previous_version" >&2; exit 1; }
  ! systemctl is-active --quiet stepanel-root-broker.service || { echo 'the root broker is still running under the previous release' >&2; exit 1; }
  wait_ready "release $previous_version after the post-upgrade rollback"
  assert_populated_upgrade_fixture
  # The previous release must still accept the audit log, including the
  # records the upgraded release appended.
  "$previous_root/stepanel" verify-audit /var/lib/ste-panel/audit.jsonl
  # Every backup is back where the previous release reads backups. Backups
  # the candidate encrypted cannot be read by a release without backup
  # encryption (docs/COMPATIBILITY.md); any others must still verify.
  [[ ! -e $backup_root/sites ]] || { echo "$backup_root/sites remains after the downgrade" >&2; exit 1; }
  for name in "${layout_backups[@]}"; do
    [[ -f $backup_root/$name/manifest.json ]] || { echo "backup $name is not in $backup_root after the downgrade" >&2; exit 1; }
    if [[ -f $backup_root/$name/backup.tar.gz.enc ]]; then
      echo "backup $name is encrypted by the newer release; $previous_version cannot read it, as documented"
    else
      (set -a; . /etc/ste-panel.env; "$previous_root/stepanel" verify-backup "$backup_root/$name") >/dev/null || { echo "$previous_version cannot verify backup $name after the downgrade" >&2; exit 1; }
    fi
  done
  echo "post-upgrade rollback to $previous_version passed"

  # The rolled-back host must accept the same upgrade again.
  (cd "$candidate_root" && ./install.sh --unsafe-lab)
  systemctl is-active --quiet stepanel.service stepanel-worker.service
  wait_ready 'the candidate after upgrading the rolled-back host'
  assert_populated_upgrade_fixture
  /opt/stepanel/stepanel restore-control-plane "$control_db" --dry-run
  # The repeat upgrade moves the backups into per-site directories again,
  # and the candidate verifies each one, archive included.
  for name in "${layout_backups[@]}"; do
    path=$backup_root/sites/${name:26}/$name
    [[ -f $path/manifest.json ]] || { echo "backup $name was not moved back into its site directory" >&2; exit 1; }
    (set -a; . /etc/ste-panel.env; /opt/stepanel/stepanel verify-backup "$path") >/dev/null || { echo "the candidate cannot verify backup $name after the round trip" >&2; exit 1; }
  done
  echo 'repeat upgrade after rollback passed'
fi
