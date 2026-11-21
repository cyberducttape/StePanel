#!/usr/bin/env bash
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'identity repair smoke must run as root' >&2; exit 1; }

root=/var/www/sites
state_dir=/var/lib/ste-panel
database=$state_dir/stepanel-control.db
repair_tool=/usr/local/sbin/stepanel-root
sites=(shared-site-prefix00018346 shared-site-prefix0001d455)

for path in "$root" "$state_dir" "$database" "$repair_tool"; do
  [[ -e $path ]] || { echo "required installed-host path is missing: $path" >&2; exit 1; }
done
[[ -d $root && ! -L $root && -f $database && ! -L $database && -x $repair_tool ]] || {
  echo 'installed-host identity repair prerequisites are unsafe' >&2
  exit 1
}

legacy_user=$(python3 - "${sites[@]}" <<'PY'
import hashlib
import sys

def legacy_user(site):
    prefix = site[:18].replace("_", "-")
    digest = hashlib.sha256(site.encode()).hexdigest()[:8]
    return f"sp-{prefix}-{digest}"

users = [legacy_user(site) for site in sys.argv[1:]]
if len(users) != 2 or users[0] != users[1]:
    raise SystemExit(f"fixture no longer reproduces a legacy collision: {users}")
print(users[0])
PY
)

for site in "${sites[@]}"; do
  site_root=$root/$site
  [[ ! -e $site_root && ! -L $site_root ]] || { echo "collision fixture already exists: $site_root" >&2; exit 1; }
  [[ ! -e /etc/stepanel/sites/$site ]] || { echo "collision fixture is registered already: $site" >&2; exit 1; }
done
if getent passwd "$legacy_user" >/dev/null; then
  echo "legacy collision fixture account already exists: $legacy_user" >&2
  exit 1
fi

# Stop managed services before writing the real control-plane database so no
# panel or worker can race the collision fixture or its identity migration.
while IFS= read -r unit; do
  [[ $unit =~ ^(stepanel(-worker|-root-broker|-app-.*|-task-.*|-runner-.*)?|caddy|apache2|httpd|lsws|lshttpd|litespeed|openlitespeed|php[^/]*fpm|php-fpm|ssh|sshd|vsftpd|cron|crond)\.service$ ]] || continue
  systemctl stop "$unit"
done < <(systemctl list-units --type=service --state=active --no-legend --plain | awk '{print $1}')
while IFS= read -r unit; do
  [[ $unit =~ ^(stepanel.*\.(socket|timer)|ssh(d)?\.socket|vsftpd\.socket)$ ]] || continue
  systemctl stop "$unit"
done < <(systemctl list-units --type=socket --state=active --no-legend --plain | awk '{print $1}')
while IFS= read -r unit; do
  [[ $unit =~ ^stepanel.*\.timer$ ]] || continue
  systemctl stop "$unit"
done < <(systemctl list-units --type=timer --state=active --no-legend --plain | awk '{print $1}')

mkdir -p "$root/${sites[0]}/public" "$root/${sites[0]}/.php" \
  "$root/${sites[1]}/public" "$root/${sites[1]}/.php"
useradd --system --home-dir "$root/${sites[0]}" --no-create-home \
  --shell /usr/sbin/nologin --user-group "$legacy_user"
for index in 0 1; do
  site=${sites[$index]}
  printf 'private-to-%s\n' "$site" > "$root/$site/public/tenant-secret.txt"
  chown -R "$legacy_user:$legacy_user" "$root/$site"
  chmod 0750 "$root/$site" "$root/$site/public" "$root/$site/.php"
  chmod 0640 "$root/$site/public/tenant-secret.txt"
done

# Reproduce a real legacy installation: one persisted site mapping and two
# live document roots that resolve to the same 32-bit-derived Unix account.
python3 - "$database" "$state_dir" "$legacy_user" "${sites[@]}" <<'PY'
import json
import os
import sqlite3
import sys
import tempfile
import time

database, state_dir, legacy_user, site_a, site_b = sys.argv[1:]
db = sqlite3.connect(database, timeout=15)
try:
    columns = {row[1] for row in db.execute("PRAGMA table_info(site_identities)")}
    if not {"site", "username", "created_at"} <= columns:
        raise SystemExit("site identity schema is unavailable or incomplete")
    if db.execute("SELECT 1 FROM site_identities WHERE site IN (?,?)", (site_a, site_b)).fetchone():
        raise SystemExit("collision fixture sites already have identity mappings")
    db.execute("INSERT INTO site_identities(site,username,created_at) VALUES(?,?,?)", (site_a, legacy_user, int(time.time())))
    db.commit()
finally:
    db.close()

def update_json(name, update):
    path = os.path.join(state_dir, name)
    try:
        with open(path, encoding="utf-8") as source:
            value = json.load(source)
    except FileNotFoundError:
        value = {}
    if not isinstance(value, dict):
        raise SystemExit(f"{name} is not a JSON object")
    update(value)
    fd, stage = tempfile.mkstemp(prefix=f".{name}.", dir=state_dir)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as target:
            json.dump(value, target)
            target.write("\n")
            target.flush()
            os.fsync(target.fileno())
        os.chmod(stage, 0o600)
        os.replace(stage, path)
    finally:
        if os.path.exists(stage):
            os.unlink(stage)

def add_profiles(value):
    for site in (site_a, site_b):
        value[site] = {
            "account": "identity-repair-smoke",
            "site": site,
            "cpu_percent": 100,
            "cpu_weight": 100,
            "memory_high_mb": 460,
            "memory_mb": 512,
            "io_weight": 100,
            "tasks_max": 128,
            "php_workers": 4,
            "disk_mb": 0,
            "inodes": 0,
            "filesystem_quota_state": "none",
            "state": "applied",
        }

def add_access(value):
    for site in (site_a, site_b):
        value[site] = {"state": "disabled", "sftp_enabled": False, "shell_enabled": False, "keys": []}

update_json("resource-profiles.json", add_profiles)
update_json("site-access.json", add_access)
PY

plan=$(mktemp)
trap 'rm -f "$plan"' EXIT
"$repair_tool" identity-repair plan --webroot /var/www --control-plane-db "$database" > "$plan"
python3 - "$plan" "${sites[@]}" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    plan = json.load(source)
expected = set(sys.argv[2:])
assignments = [item for item in plan["assignments"] if item["site"] in expected]
if len(plan["assignments"]) != len(expected) or {item["site"] for item in assignments} != expected:
    raise SystemExit(f"repair plan omitted a colliding site: {assignments}")
users = [item["username"] for item in assignments]
if len(set(users)) != len(expected) or any(item["username"] == item["legacy_user"] for item in assignments):
    raise SystemExit(f"repair plan did not allocate distinct replacement identities: {assignments}")
print("installed repair plan assigns distinct identities to both colliding sites")
PY

recovery_root=/var/lib/stepanel/recovery
journal=$recovery_root/site-identity-repair.json
site_ctl=/usr/local/sbin/stepanel-sitectl
site_ctl_real=$site_ctl.identity-repair-real
fault_spec=/run/stepanel-identity-repair-fault
apply_repair() {
  "$repair_tool" identity-repair apply --webroot /var/www --control-plane-db "$database" \
    --recovery-root "$recovery_root" --state-dir "$state_dir"
}
restore_site_ctl() {
  rm -f "$fault_spec"
  if [[ -f $site_ctl_real ]]; then mv -f "$site_ctl_real" "$site_ctl"; fi
}
trap 'restore_site_ctl; rm -f "$plan"' EXIT
[[ ! -e $journal ]] || { echo "an identity repair journal already exists: $journal" >&2; exit 1; }

# check_state <label> <journal state> <mappings committed 0|1> <completed sites...>
check_state() {
  python3 - "$journal" "$database" "$legacy_user" "$@" "${sites[@]}" <<'PY'
import json
import sqlite3
import sys

journal_path, database, legacy, label, state, committed = sys.argv[1:7]
rest = sys.argv[7:]
fixture = rest[-2:]
completed = set(rest[:-2])
with open(journal_path, encoding="utf-8") as source:
    journal = json.load(source)
if journal["state"] != state:
    raise SystemExit(f"{label}: journal state {journal['state']!r}, want {state!r}")
done = {item["site"] for item in journal["assignments"] if item.get("completed")}
if done != completed:
    raise SystemExit(f"{label}: completed sites {sorted(done)}, want {sorted(completed)}")
db = sqlite3.connect(database)
try:
    rows = dict(db.execute("SELECT site,username FROM site_identities WHERE site IN (?,?)", fixture).fetchall())
finally:
    db.close()
assigned = {item["site"]: item["username"] for item in journal["assignments"]}
if committed == "1":
    if rows != {site: assigned[site] for site in fixture}:
        raise SystemExit(f"{label}: committed mappings {rows} do not match journal {assigned}")
elif rows != {fixture[0]: legacy}:
    raise SystemExit(f"{label}: mappings changed before commit: {rows}")
print(f"{label}: journal {state}, mappings {'committed' if committed == '1' else 'unchanged'}, completed {sorted(done) or 'none'}")
PY
}

# Interruption 1, database checkpoint: hold a write lock so the mapping
# commit blocks after the plan journal and SQLite snapshot are durable, then
# SIGKILL the repair. A busy-timeout failure leaves the same durable state.
lock_ready=$(mktemp -u)
python3 - "$database" "$lock_ready" <<'PY' &
import sqlite3
import sys
import time

db = sqlite3.connect(sys.argv[1], timeout=15, isolation_level=None)
db.execute("BEGIN IMMEDIATE")
open(sys.argv[2], "w").close()
# Held until the smoke kills this process: preflight scans every site tree
# and can take longer than any fixed hold on a host with many sites.
time.sleep(3600)
PY
lock_pid=$!
for _ in $(seq 1 500); do [[ -e $lock_ready ]] && break; sleep 0.02; done
[[ -e $lock_ready ]] || { echo 'could not hold the control-plane write lock' >&2; exit 1; }
repair_log=$(mktemp)
apply_repair > "$repair_log" 2>&1 &
repair_pid=$!
killed=0
lock_started=$SECONDS
# The plan journal is durable before the snapshot and the mapping commit,
# which then waits on the held lock for the 1 s busy timeout: kill as soon as
# the journal appears.
fail_lock=0
for _ in $(seq 1 120000); do
  if [[ -s $journal ]]; then
    kill -KILL "$repair_pid" 2>/dev/null && killed=1
    kill -0 "$lock_pid" 2>/dev/null || fail_lock=1
    break
  fi
  kill -0 "$repair_pid" 2>/dev/null || break
  sleep 0.005
done
repair_status=0
wait "$repair_pid" || repair_status=$?
kill "$lock_pid" 2>/dev/null || true
wait "$lock_pid" 2>/dev/null || true
rm -f "$lock_ready"
(( repair_status != 0 )) || { echo 'repair completed while the mapping commit was blocked' >&2; exit 1; }
echo "database checkpoint interruption: killed=$killed exit=$repair_status after $((SECONDS - lock_started)) s"
(( ! fail_lock )) || { echo 'the write lock holder had exited before the plan journal appeared' >&2; exit 1; }
(( killed )) || { echo 'the repair exited before it could be killed at the database checkpoint:' >&2; cat "$repair_log" >&2; }
rm -f "$repair_log"
check_state 'before mapping commit' planned 0

# Interruptions 2-4, host checkpoints: a wrapper SIGKILLs the repair on one
# chosen stepanel-sitectl call, before or after the real helper runs.
mv "$site_ctl" "$site_ctl_real"
cat > "$site_ctl" <<EOF
#!/usr/bin/env bash
if [[ -f $fault_spec ]]; then
  { read -r mode; read -r pattern; } < $fault_spec
  # shellcheck disable=SC2053
  if [[ "\$*" == \$pattern ]]; then
    rm -f $fault_spec
    [[ \$mode == post ]] && $site_ctl_real "\$@"
    kill -KILL "\$PPID"
    exit 137
  fi
fi
exec $site_ctl_real "\$@"
EOF
chown root:root "$site_ctl"
chmod 0755 "$site_ctl"
interrupt_at() {
  printf '%s\n%s\n' "$1" "$2" > "$fault_spec"
  local status=0
  apply_repair > /dev/null 2>&1 || status=$?
  [[ ! -e $fault_spec ]] || { echo "fault point was never reached: $2" >&2; exit 1; }
  (( status == 137 )) || { echo "repair was not killed at $2 (exit $status)" >&2; exit 1; }
}
interrupt_at pre "access ${sites[0]} $legacy_user 0 0"
check_state 'after commit, before host work' mappings-committed 1
interrupt_at post "resources ${sites[0]} *"
check_state 'mid-site host reconcile' mappings-committed 1
interrupt_at post "seal ${sites[1]} *"
check_state 'second site sealed, first verified' mappings-committed 1 "${sites[0]}"
restore_site_ctl

apply_repair
check_state 'resumed to completion' completed 1 "${sites[@]}"
# A completed journal must make retries idempotent, including after the
# already-repaired mapping has become the only durable source of identity.
"$repair_tool" identity-repair apply --webroot /var/www --control-plane-db "$database" \
  --recovery-root /var/lib/stepanel/recovery --state-dir "$state_dir"

mapfile -t new_users < <(python3 - "$database" "${sites[@]}" <<'PY'
import sqlite3
import sys

db = sqlite3.connect(sys.argv[1])
try:
    rows = db.execute("SELECT site,username FROM site_identities WHERE site IN (?,?) ORDER BY site", sys.argv[2:]).fetchall()
finally:
    db.close()
if len(rows) != 2 or len({user for _, user in rows}) != 2:
    raise SystemExit(f"repaired site identities are not unique: {rows}")
for site, user in rows:
    print(user)
PY
)
[[ ${#new_users[@]} -eq 2 ]] || { echo 'could not read both repaired site identities' >&2; exit 1; }
for index in 0 1; do
  site=${sites[$index]}
  user=${new_users[$index]}
  account=$(getent passwd "$user")
  [[ ${account%%:*} == "$user" && $(cut -d: -f6 <<<"$account") == "$root/$site" ]] || {
    echo "repaired account has the wrong home for $site: $account" >&2
    exit 1
  }
  [[ $(stat -c %U "$root/$site/public/tenant-secret.txt") == "$user" ]] || {
    echo "repaired file ownership is wrong for $site" >&2
    exit 1
  }
done

for index in 0 1; do
  other=$((1 - index))
  user=${new_users[$index]}
  secret="$root/${sites[$other]}/public/tenant-secret.txt"
  if runuser -u "$user" -- test -r "$secret"; then
    echo "cross-site read unexpectedly succeeded: $user -> ${sites[$other]}" >&2
    exit 1
  fi
  if runuser -u "$user" -- sh -c 'printf intrusion > "$1"' sh "$secret" 2>/dev/null; then
    echo "cross-site write unexpectedly succeeded: $user -> ${sites[$other]}" >&2
    exit 1
  fi
done

# Reassignment must not leave the retired account a named ACL entry: it
# would keep access that ownership checks alone do not show.
for site in "${sites[@]}"; do
  if getfacl -R -P -p "$root/$site" 2>/dev/null | grep -Eq "^(default:)?user:$legacy_user:"; then
    echo "retired identity $legacy_user keeps an ACL entry under $site" >&2
    exit 1
  fi
done
if getent passwd "$legacy_user" >/dev/null && [[ -n $(find -P "$root" -xdev -user "$legacy_user" -print -quit) ]]; then
  echo "legacy identity still owns data under the managed sites root: $legacy_user" >&2
  exit 1
fi
echo 'installed identity collision repair passed: journaled reassignment, retry, retired-identity ownership check, and cross-site read/write denial verified'
