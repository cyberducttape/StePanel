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

"$repair_tool" identity-repair apply --webroot /var/www --control-plane-db "$database" \
  --recovery-root /var/lib/stepanel/recovery --state-dir "$state_dir"
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

if getent passwd "$legacy_user" >/dev/null && [[ -n $(find -P "$root" -xdev -user "$legacy_user" -print -quit) ]]; then
  echo "legacy identity still owns data under the managed sites root: $legacy_user" >&2
  exit 1
fi
echo 'installed identity collision repair passed: journaled reassignment, retry, retired-identity ownership check, and cross-site read/write denial verified'
