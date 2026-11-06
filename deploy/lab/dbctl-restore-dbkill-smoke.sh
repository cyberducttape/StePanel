#!/usr/bin/env bash
# Kill the MySQL/MariaDB server in the middle of a managed database restore
# and check real state afterwards.
#
# A site database is provisioned; a large restore starts; once its shadow
# database exists the server is SIGKILLed, then started again (as the service
# manager would) and the helper's reconcile runs (as StePanel's startup
# does). The live database must be byte-for-byte unchanged and usable by the
# site's own account, no shadow database, restore record, or shadow table
# directory may remain, and a later restore must succeed.
#
# Destructive to the database service: run only on a disposable host or CI
# runner.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'database kill smoke must run as root' >&2; exit 1; }
helper=${STEPANEL_DBCTL:-/usr/local/sbin/stepanel-dbctl}
registry=${STEPANEL_DBCTL_REGISTRY:-/var/lib/stepanel-privileged/db-managed}
datadir=${STEPANEL_DB_DATADIR:-/var/lib/mysql}
service=${STEPANEL_DB_SERVICE:-}
dump_mib=${STEPANEL_DB_KILL_DUMP_MIB:-400}
client=$(command -v mariadb || command -v mysql) || { echo 'no MySQL/MariaDB client' >&2; exit 1; }
dump_client=$(command -v mariadb-dump || command -v mysqldump) || { echo 'no MySQL/MariaDB dump client' >&2; exit 1; }
if [[ -z $service ]]; then
  for candidate in mysql mariadb mysqld; do
    if systemctl is-active --quiet "$candidate"; then service=$candidate; break; fi
  done
fi
[[ -n $service ]] || { echo 'no running MySQL/MariaDB service' >&2; exit 1; }

suffix=$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
db="dbkill_$suffix"
db_user="dbkill_$suffix"
site="dbkillsmoke-$suffix"
password="Dbkill${suffix}Smoke${suffix}pw"
work=$(mktemp -d /tmp/stepanel-db-kill-smoke.XXXXXX)
restore_pid=''

M() { "$client" --no-defaults --protocol=socket --batch --raw --skip-column-names "$@"; }
app() { MYSQL_PWD=$password "$client" --no-defaults --protocol=socket --user="$db_user" --batch --skip-column-names "$db" "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }
snapshot() {
  "$dump_client" --no-defaults --protocol=socket --skip-comments --skip-dump-date --order-by-primary "$db" | sed -e 's/ AUTO_INCREMENT=[0-9]*//'
}
wait_for_server() {
  for _ in $(seq 1 90); do M -e 'SELECT 1' >/dev/null 2>&1 && return 0; sleep 1; done
  return 1
}
cleanup() {
  local status=$?
  set +e
  if [[ -n $restore_pid ]]; then kill -9 "$restore_pid" 2>/dev/null; wait "$restore_pid" 2>/dev/null; fi
  systemctl is-active --quiet "$service" || systemctl start "$service"
  wait_for_server
  "$helper" reconcile >/dev/null 2>&1
  "$helper" drop-managed "$db" "$db_user" >/dev/null 2>&1
  rm -rf -- "$work"
  if (( status != 0 )); then echo "database kill smoke failed (status $status)" >&2; fi
  return "$status"
}
trap cleanup EXIT

printf '%s\n' "$password" | "$helper" provision "$db" "$db_user" "$site" utf8mb4
app -e "CREATE TABLE orders (id INT PRIMARY KEY, note VARCHAR(200)); INSERT INTO orders VALUES (1, 'live order one'), (2, 'live order two'); CREATE VIEW order_notes AS SELECT note FROM orders;"
expected=$(snapshot)

python3 -I - "$dump_mib" > "$work/big.sql" <<'PY'
import sys
target_mib = int(sys.argv[1])
print("CREATE TABLE filler (id INT PRIMARY KEY, blob_value LONGTEXT);")
row = "y" * 60000
rows = target_mib * 1024 * 1024 // len(row) + 1
next_id = 1
while next_id <= rows:
    values = ",".join(f"({i},'{row}')" for i in range(next_id, min(next_id + 16, rows + 1)))
    print(f"INSERT INTO filler VALUES {values};")
    next_id += 16
PY
pass "generated a $(( $(stat -c %s "$work/big.sql") / 1048576 )) MiB dump"

"$helper" restore-dump "$db" "$site" < "$work/big.sql" > /dev/null 2> "$work/restore.err" &
restore_pid=$!

# Kill the server once the shadow database is receiving the load.
shadow=''
for _ in $(seq 1 600); do
  shadow=$(M -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE 'stepanel\\_restore\\_%'" 2>/dev/null | head -1)
  if [[ -n $shadow ]] && [[ $(M -e "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = '$shadow'" 2>/dev/null) -ge 1 ]]; then break; fi
  kill -0 "$restore_pid" 2>/dev/null || fail "the restore ended before the server could be killed: $(cat "$work/restore.err")"
  sleep 0.1
done
[[ -n $shadow ]] || fail 'the restore never created its shadow database'
sleep 1
systemctl kill --signal=SIGKILL "$service"
pass "killed $service while $shadow was loading"
if wait "$restore_pid"; then
  restore_pid=''
  fail 'the restore reported success although the server was killed during the load'
fi
restore_pid=''

# The service manager restarts the server; StePanel's startup reconciles.
for _ in $(seq 1 30); do systemctl is-active --quiet "$service" && break; sleep 1; done
systemctl is-active --quiet "$service" || systemctl start "$service"
wait_for_server || fail 'the database server did not come back'
"$helper" reconcile >/dev/null || fail 'reconciliation after the server kill failed'

[[ $(snapshot) == "$expected" ]] || fail 'the live database changed'
app -e 'SELECT COUNT(*) FROM order_notes' | grep -qx 2 || fail 'the site account cannot read its database'
app -e "INSERT INTO orders VALUES (99, 'after kill'); DELETE FROM orders WHERE id = 99" || fail 'the site account cannot write its database'
for record in "$registry"/stepanel_restore_*; do
  [[ -f $record ]] || continue
  [[ $(<"$record") != "pending-restore:$site:$db:"* ]] || fail "restore record ${record##*/} remains: $(<"$record")"
done
left=$(M -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE 'stepanel\\_restore\\_%' OR SCHEMA_NAME LIKE 'stepanel\\_retired\\_%'")
[[ -z $left ]] || fail "shadow database(s) remain: $left"
leftover=$(find "$datadir" -maxdepth 1 -type d \( -name 'stepanel_restore_*' -o -name 'stepanel_retired_*' \) -print 2>/dev/null)
[[ -z $leftover ]] || fail "shadow table directories remain on disk: $leftover"
pass 'the live database is unchanged and usable, and no shadow remains'

"$helper" dump "$db" > "$work/small.sql"
app -e "UPDATE orders SET note = 'changed after backup' WHERE id = 1"
"$helper" restore-dump "$db" "$site" < "$work/small.sql"
[[ $(snapshot) == "$expected" ]] || fail 'a restore after the server kill did not restore the backup'
pass 'a restore after the server kill completed'
echo 'database server kill during restore smoke passed'
