#!/usr/bin/env bash
# Exhaust the database filesystem during a managed database restore and
# check real state, not only the helper's exit status.
#
# The MySQL/MariaDB data directory (binary logs included) is moved onto a
# loopback ext4 filesystem with a little free space. A site database is
# provisioned, then restore-dump is given a dump far larger than that space:
#
#   1. with its size declared, admission must refuse it before loading;
#   2. with its size unknown, the shadow import must hit ENOSPC.
#
# After each, the live database must be byte-for-byte unchanged and usable by
# the site's own account, no shadow database or restore record may remain,
# the server must still serve, the shadow's space must be reclaimed, and a
# small restore must then succeed.
#
# Destructive to the host's database service layout: run only on a
# disposable host or CI runner. Restores the original data directory on exit.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'database ENOSPC smoke must run as root' >&2; exit 1; }
helper=${STEPANEL_DBCTL:-/usr/local/sbin/stepanel-dbctl}
registry=${STEPANEL_DBCTL_REGISTRY:-/var/lib/stepanel-privileged/db-managed}
datadir=${STEPANEL_DB_DATADIR:-/var/lib/mysql}
service=${STEPANEL_DB_SERVICE:-}
# stepanel-dbctl keeps at least 1 GiB of the database filesystem free and
# refuses a restore that would dip into it, so a load can only exhaust the
# filesystem when there is more than that free and the dump is larger than
# estimated.
headroom_mib=${STEPANEL_DB_ENOSPC_HEADROOM_MIB:-1400}
client=$(command -v mariadb || command -v mysql) || { echo 'no MySQL/MariaDB client' >&2; exit 1; }
dump_client=$(command -v mariadb-dump || command -v mysqldump) || { echo 'no MySQL/MariaDB dump client' >&2; exit 1; }
for tool in mkfs.ext4 rsync losetup; do command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }; done
if [[ -z $service ]]; then
  for candidate in mysql mariadb mysqld; do
    if systemctl is-active --quiet "$candidate"; then service=$candidate; break; fi
  done
fi
[[ -n $service ]] || { echo 'no running MySQL/MariaDB service' >&2; exit 1; }
[[ -d $datadir && ! -L $datadir ]] || { echo "data directory $datadir is missing" >&2; exit 1; }

suffix=$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
db="enospc_$suffix"
db_user="enospc_$suffix"
site="enospcsmoke-$suffix"
password="Enospc${suffix}Smoke${suffix}pw"
work=$(mktemp -d /tmp/stepanel-db-enospc-smoke.XXXXXX)
image="$work/datadir.img"
original="$work/original-datadir"
mounted=0

M() { "$client" --no-defaults --protocol=socket --batch --raw --skip-column-names "$@"; }
app() { MYSQL_PWD=$password "$client" --no-defaults --protocol=socket --user="$db_user" --batch --skip-column-names "$db" "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }
snapshot() {
  "$dump_client" --no-defaults --protocol=socket --skip-comments --skip-dump-date --order-by-primary "$db" | sed -e 's/ AUTO_INCREMENT=[0-9]*//'
}
free_kib() { df --output=avail -k "$datadir" | tail -1 | tr -d ' '; }
wait_for_server() {
  for _ in $(seq 1 60); do M -e 'SELECT 1' >/dev/null 2>&1 && return 0; sleep 1; done
  return 1
}
# A full filesystem can stop the server itself: MySQL's default
# binlog_error_action aborts it when the binary log cannot be written.
# Systemd restarts it, and startup reconciliation (the helper's reconcile,
# which StePanel runs at startup) finishes or discards interrupted restores.
recover_like_startup() {
  if ! M -e 'SELECT 1' >/dev/null 2>&1; then
    echo "the database server stopped on the full filesystem; waiting for it to restart" >&2
    systemctl is-active --quiet "$service" || systemctl start "$service" || true
  fi
  wait_for_server || fail "the database server did not come back after $1"
  "$helper" reconcile >/dev/null || fail "startup reconciliation failed after $1"
}

cleanup() {
  local status=$?
  set +e
  if (( mounted )); then
    "$helper" drop-managed "$db" "$db_user" >/dev/null 2>&1
    systemctl stop "$service"
    umount "$datadir"
    systemctl start "$service"
  fi
  rm -rf -- "$work"
  if (( status != 0 )); then echo "database ENOSPC smoke failed (status $status)" >&2; fi
  return "$status"
}
trap cleanup EXIT

# Move the data directory onto a loopback filesystem sized to its current
# use plus the headroom. The original directory stays underneath the mount
# and is back in place once it is unmounted.
owner=$(stat -c %u:%g "$datadir")
mode=$(stat -c %a "$datadir")
systemctl stop "$service"
used_mib=$(( $(du -sm "$datadir" | cut -f1) + headroom_mib + 64 ))
truncate -s "${used_mib}M" "$image"
mkfs.ext4 -q -F -m 0 "$image"
mkdir -p "$original"
mount -o loop "$image" "$original"
rsync -aHAX "$datadir/" "$original/"
umount "$original"
mount -o loop "$image" "$datadir"
mounted=1
chown "$owner" "$datadir"
chmod "$mode" "$datadir"
systemctl start "$service"
wait_for_server || fail 'database server did not start on the loopback data directory'
pass "data directory on a ${used_mib} MiB loopback filesystem with $(( $(free_kib) / 1024 )) MiB free"

printf '%s\n' "$password" | "$helper" provision "$db" "$db_user" "$site" utf8mb4
app -e "CREATE TABLE orders (id INT PRIMARY KEY, note VARCHAR(200)); INSERT INTO orders VALUES (1, 'live order one'), (2, 'live order two'); CREATE VIEW order_notes AS SELECT note FROM orders;"
expected=$(snapshot)
before_kib=$(free_kib)

# A dump 512 MiB larger than the free space, in multi-row inserts.
python3 -I - "$(( before_kib / 1024 + 512 ))" > "$work/huge.sql" <<'PY'
import sys
target_mib = int(sys.argv[1])
print("CREATE TABLE filler (id INT PRIMARY KEY, blob_value LONGTEXT);")
row = "x" * 60000
rows_per_insert = 16
total_rows = target_mib * 1024 * 1024 // len(row) + 1
next_id = 1
while next_id <= total_rows:
    values = ",".join(f"({i},'{row}')" for i in range(next_id, min(next_id + rows_per_insert, total_rows + 1)))
    print(f"INSERT INTO filler VALUES {values};")
    next_id += rows_per_insert
PY
dump_bytes=$(stat -c %s "$work/huge.sql")
pass "generated a $(( dump_bytes / 1048576 )) MiB dump for $(( before_kib / 1024 )) MiB free"

assert_live_intact() {
  local phase=$1 record shadow
  [[ $(snapshot) == "$expected" ]] || fail "$phase: the live database changed"
  app -e 'SELECT COUNT(*) FROM order_notes' | grep -qx 2 || fail "$phase: the site account cannot read its database"
  app -e "INSERT INTO orders VALUES (99, 'write after $phase'); DELETE FROM orders WHERE id = 99" || fail "$phase: the site account cannot write its database"
  for record in "$registry"/stepanel_restore_*; do
    [[ -f $record ]] || continue
    [[ $(<"$record") != "pending-restore:$site:$db:"* ]] || fail "$phase: restore record ${record##*/} remains: $(<"$record")"
  done
  shadow=$(M -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE 'stepanel\\_restore\\_%' OR SCHEMA_NAME LIKE 'stepanel\\_retired\\_%'")
  [[ -z $shadow ]] || fail "$phase: shadow database(s) remain: $shadow"
  M -e 'SELECT 1' >/dev/null || fail "$phase: the database server stopped serving"
}

# 1. Declared size: admission refuses before loading anything.
if "$helper" restore-dump "$db" "$site" "$dump_bytes" < "$work/huge.sql" 2> "$work/admission.err"; then
  fail 'a restore larger than the free space was admitted'
fi
grep -q 'byte reserve' "$work/admission.err" || fail "the oversized restore failed, but not by admission: $(cat "$work/admission.err")"
assert_live_intact 'admission refusal'
pass "admission refused the oversized restore: $(tail -1 "$work/admission.err")"

# 2. Unknown size: the shadow import runs until the filesystem is full.
if "$helper" restore-dump "$db" "$site" < "$work/huge.sql" 2> "$work/enospc.err"; then
  fail 'a restore that cannot fit on the filesystem reported success'
fi
# It must have passed admission and failed by running out of space.
! grep -q 'byte reserve' "$work/enospc.err" || fail "the unknown-size restore was refused by admission instead of loading: $(cat "$work/enospc.err")"
grep -qiE 'full|space|errno: 28|error 28|\(28\)' "$work/enospc.err" || fail "the shadow import failed, but not for lack of space: $(cat "$work/enospc.err")"
recover_like_startup 'ENOSPC during the shadow import'
assert_live_intact 'ENOSPC during the shadow import'
pass "the shadow import failed on a full filesystem: $(grep -m1 -iE 'full|space|errno|28' "$work/enospc.err" || tail -1 "$work/enospc.err")"
after_kib=$(free_kib)
# The shadow's tables are gone from disk. Free space need not come back in
# full: with binary logging on, the failed load's inserts stay in the binary
# log, which StePanel does not manage.
leftover=$(find "$datadir" -maxdepth 1 -type d \( -name 'stepanel_restore_*' -o -name 'stepanel_retired_*' \) -print)
[[ -z $leftover ]] || fail "shadow table directories remain on disk: $leftover"
pass "no shadow tables remain on disk ($(( before_kib / 1024 )) MiB free before, $(( after_kib / 1024 )) MiB after)"

# 3. After freeing space (purging binary logs, as an operator would), an
# ordinary restore completes.
if [[ $(M -e "SELECT @@log_bin") == 1 ]]; then
  M -e 'PURGE BINARY LOGS BEFORE NOW()'
  pass "purged binary logs ($(( $(free_kib) / 1024 )) MiB free)"
fi
"$helper" dump "$db" > "$work/small.sql"
app -e "UPDATE orders SET note = 'changed after backup' WHERE id = 1"
"$helper" restore-dump "$db" "$site" "$(stat -c %s "$work/small.sql")" < "$work/small.sql"
[[ $(snapshot) == "$expected" ]] || fail 'a restore after the ENOSPC failure did not restore the backup'
pass 'a restore after the ENOSPC failure completed'

echo 'database restore ENOSPC smoke passed'
