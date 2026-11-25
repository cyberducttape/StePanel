#!/usr/bin/env bash
# Fill the database filesystem at the moment a managed restore starts its
# swap, and check that the restore still rolls forward to exactly the
# restored contents once space returns.
#
# The MySQL/MariaDB data directory is moved onto a loopback ext4 filesystem
# with room for the restore's admission reserve. A site database with a
# table, a view and a trigger is backed up with the helper, changed, and
# restored from that backup. A watcher polls the restore record and, as soon
# as it moves to the swap phase, fills the filesystem to its last block.
#
# The swap fences the site's database accounts, moves the tables with one
# RENAME TABLE, recreates views and triggers, and unlocks the accounts. With
# no free space the server may fail a statement or wait for space. Either
# way, once the load has succeeded the contract is to roll forward: after
# the filler is removed, the helper (or startup reconciliation) must finish
# the swap. The drill then requires that
#
#   - the database holds exactly the backup's contents, view and trigger;
#   - the site's own account can log in, read and write (the fence lifted);
#   - no shadow or retired database and no restore record remain;
#   - the server still serves.
#
# STEPANEL_DB_ENOSPC_FILL_PHASE=done fills the filesystem instead when the
# swap has finished and cleanup (dropping the shadow and the retired copy of
# the old tables) begins; the same final state is required.
#
# It reports whether the server actually logged an out-of-space condition,
# so a run in which the swap needed no new space is not mistaken for one
# that survived a full disk.
#
# Destructive to the host's database service layout: run only on a
# disposable host or CI runner. Restores the original data directory on exit.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'database swap ENOSPC smoke must run as root' >&2; exit 1; }
helper=${STEPANEL_DBCTL:-/usr/local/sbin/stepanel-dbctl}
registry=${STEPANEL_DBCTL_REGISTRY:-/var/lib/stepanel-privileged/db-managed}
datadir=${STEPANEL_DB_DATADIR:-/var/lib/mysql}
service=${STEPANEL_DB_SERVICE:-}
# stepanel-dbctl admits a restore only with at least 1 GiB free beyond its
# estimate; leave enough for that so the restore reaches the swap.
headroom_mib=${STEPANEL_DB_ENOSPC_HEADROOM_MIB:-1400}
full_seconds=${STEPANEL_DB_SWAP_FULL_SECONDS:-45}
fill_phase=${STEPANEL_DB_ENOSPC_FILL_PHASE:-swap}
[[ $fill_phase == swap || $fill_phase == 'done' ]] || { echo 'STEPANEL_DB_ENOSPC_FILL_PHASE must be swap or done' >&2; exit 64; }
client=$(command -v mariadb || command -v mysql) || { echo 'no MySQL/MariaDB client' >&2; exit 1; }
dump_client=$(command -v mariadb-dump || command -v mysqldump) || { echo 'no MySQL/MariaDB dump client' >&2; exit 1; }
for tool in mkfs.ext4 rsync losetup fallocate; do command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }; done
if [[ -z $service ]]; then
  for candidate in mysql mariadb mysqld; do
    if systemctl is-active --quiet "$candidate"; then service=$candidate; break; fi
  done
fi
[[ -n $service ]] || { echo 'no running MySQL/MariaDB service' >&2; exit 1; }
[[ -d $datadir && ! -L $datadir ]] || { echo "data directory $datadir is missing" >&2; exit 1; }

suffix=$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
db="swapfull_$suffix"
db_user="swapfull_$suffix"
site="swapfullsmoke-$suffix"
password="Swapfull${suffix}Smoke${suffix}pw"
work=$(mktemp -d /tmp/stepanel-db-swap-enospc.XXXXXX)
image="$work/datadir.img"
original="$work/original-datadir"
filler="$datadir/stepanel-swap-enospc.fill"
mounted=0
watcher=
restore_pid=

M() { "$client" --no-defaults --protocol=socket --batch --raw --skip-column-names "$@"; }
app() { MYSQL_PWD=$password "$client" --no-defaults --protocol=socket --user="$db_user" --batch --skip-column-names "$db" "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }
snapshot() {
  "$dump_client" --no-defaults --protocol=socket --skip-comments --skip-dump-date --order-by-primary --triggers "$db" | sed -e 's/ AUTO_INCREMENT=[0-9]*//'
}
free_kib() { df --output=avail -k "$datadir" | tail -1 | tr -d ' '; }
wait_for_server() {
  for _ in $(seq 1 120); do M -e 'SELECT 1' >/dev/null 2>&1 && return 0; sleep 1; done
  return 1
}

cleanup() {
  local status=$?
  set +e
  [[ -z $watcher ]] || kill "$watcher" 2>/dev/null
  rm -f -- "$filler" "$filler.tail"
  [[ -z $restore_pid ]] || { kill "$restore_pid" 2>/dev/null; wait "$restore_pid" 2>/dev/null; }
  if (( mounted )); then
    "$helper" reconcile >/dev/null 2>&1
    "$helper" drop-managed "$db" "$db_user" >/dev/null 2>&1
    systemctl stop "$service"
    umount "$datadir"
    systemctl start "$service"
  fi
  rm -rf -- "$work"
  if (( status != 0 )); then echo "database swap ENOSPC smoke failed (status $status)" >&2; fi
  return "$status"
}
trap cleanup EXIT

# Move the data directory onto a loopback filesystem sized to its current
# use plus the headroom, as the restore ENOSPC smoke does.
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
app -e "CREATE TABLE orders (id INT PRIMARY KEY, note VARCHAR(200), total INT NOT NULL DEFAULT 0);
  CREATE TABLE audit (id INT AUTO_INCREMENT PRIMARY KEY, order_id INT);
  INSERT INTO orders VALUES (1, 'backed-up order one', 10), (2, 'backed-up order two', 20);
  CREATE VIEW order_notes AS SELECT note FROM orders;
  CREATE TRIGGER orders_audit AFTER INSERT ON orders FOR EACH ROW INSERT INTO audit (order_id) VALUES (NEW.id);"
"$helper" dump "$db" > "$work/backup.sql"
expected=$(snapshot)
app -e "UPDATE orders SET note = 'changed after backup' WHERE id = 1; INSERT INTO orders VALUES (3, 'added after backup', 30)"
[[ $(snapshot) != "$expected" ]] || fail 'the live change did not alter the database'
pass "backed up $db and changed it"

# Watch the restore record. The moment it reaches the swap phase, fill the
# filesystem to its last block.
(
  record=''
  for _ in $(seq 1 60000); do
    for candidate in "$registry"/stepanel_restore_*; do
      [[ -f $candidate ]] || continue
      value=$(<"$candidate") || continue
      if [[ $value == "pending-restore:$site:$db:$fill_phase" ]]; then record=$candidate; break 2; fi
    done
    sleep 0.005
  done
  [[ -n $record ]] || exit 1
  avail=$(( $(df --output=avail -B1 "$datadir" | tail -1 | tr -d ' ') ))
  # Allocate nearly everything at once, then write the last blocks until the
  # filesystem refuses them.
  fallocate -l $(( avail > 1048576 ? avail - 1048576 : 0 )) "$filler" 2>/dev/null || true
  dd if=/dev/zero of="$filler.tail" bs=4096 2>/dev/null || true
  echo "$(df --output=avail -k "$datadir" | tail -1 | tr -d ' ')" > "$work/filled"
) &
watcher=$!

journal_since=$(date '+%Y-%m-%d %H:%M:%S')
# The server reports out-of-space conditions in its error log, which is a
# file on some distributions and the journal on others.
error_log=$(M -e 'SELECT @@log_error' 2>/dev/null || true)
case $error_log in
  ''|stderr) error_log= ;;
  /*) ;;
  *) error_log="$datadir/${error_log#./}" ;;
esac
error_log_offset=0
[[ -z $error_log || ! -f $error_log ]] || error_log_offset=$(stat -c %s "$error_log")
"$helper" restore-dump "$db" "$site" "$(stat -c %s "$work/backup.sql")" < "$work/backup.sql" > "$work/restore.out" 2> "$work/restore.err" &
restore_pid=$!

for _ in $(seq 1 600); do [[ -s $work/filled ]] && break; kill -0 "$restore_pid" 2>/dev/null || break; sleep 0.1; done
inconclusive=0
if [[ ! -s $work/filled ]]; then
  # The cleanup phase is short; if it finished before the watcher could
  # fill the filesystem, the run proves nothing about ENOSPC but the final
  # state is still checked. The swap phase must always be reached.
  sleep 1
  [[ ! -s $work/filled && $fill_phase == 'done' ]] && ! kill -0 "$restore_pid" 2>/dev/null && wait "$restore_pid" && inconclusive=1
  (( inconclusive )) || [[ -s $work/filled ]] || fail "the restore never reached its $fill_phase phase: $(cat "$work/restore.err")"
fi
kill "$watcher" 2>/dev/null || true
watcher=
if (( inconclusive )); then
  restore_pid=
  swap_during_full='finished cleanup before the filesystem could be filled'
else
  pass "filled the database filesystem as the $fill_phase phase started ($(cat "$work/filled") KiB left)"
fi

# Hold the filesystem full while the restore runs into it.
held=0
while (( ! inconclusive && held < full_seconds )) && kill -0 "$restore_pid" 2>/dev/null; do sleep 1; held=$((held + 1)); done
if (( inconclusive )); then
  :
elif kill -0 "$restore_pid" 2>/dev/null; then
  swap_during_full="still running after ${full_seconds}s on the full filesystem"
elif wait "$restore_pid"; then
  swap_during_full='completed while the filesystem was full'
  restore_pid=
else
  swap_during_full="failed while the filesystem was full: $(tail -1 "$work/restore.err")"
  echo 'helper output while the filesystem was full:'
  tail -n 8 "$work/restore.err" | sed 's/^/  /'
  restore_pid=
fi
echo "restore $fill_phase phase on a full filesystem: $swap_during_full"

# Space returns; the swap must roll forward.
rm -f -- "$filler" "$filler.tail"
if [[ -n $restore_pid ]]; then
  for _ in $(seq 1 300); do kill -0 "$restore_pid" 2>/dev/null || break; sleep 1; done
  kill -0 "$restore_pid" 2>/dev/null && fail 'the restore did not finish within 300s after space returned'
  wait "$restore_pid" || echo "the restore exited with an error after space returned: $(tail -1 "$work/restore.err")"
  restore_pid=
fi
if ! M -e 'SELECT 1' >/dev/null 2>&1; then
  echo 'the database server stopped; waiting for it to restart' >&2
  systemctl is-active --quiet "$service" || systemctl start "$service" || true
fi
wait_for_server || fail 'the database server did not serve after space returned'
"$helper" reconcile >/dev/null || fail 'startup reconciliation failed after space returned'

server_messages() {
  journalctl -u "$service" --since "$journal_since" --no-pager -q 2>/dev/null || true
  if [[ -n $error_log && -f $error_log ]]; then tail -c "+$((error_log_offset + 1))" "$error_log" 2>/dev/null || true; fi
}
space_pattern='disk is full|disk full|no space left|errcode: 28|error 28|errno: 28|out of disk|binlog_error_action|table .* is full'
out_of_space=$(server_messages | grep -Eci "$space_pattern" || true)
# The server may report the condition only to the failing client.
client_out_of_space=$(grep -Eci "$space_pattern" "$work/restore.err" || true)
[[ $(snapshot) == "$expected" ]] || fail 'the database does not hold exactly the backup contents after the swap'
app -e 'SELECT COUNT(*) FROM order_notes' | grep -qx 2 || fail 'the site account cannot read the restored view'
app -e "INSERT INTO orders VALUES (98, 'write after swap', 1)" || fail 'the site account cannot write after the swap (fence not lifted?)'
app -e 'SELECT COUNT(*) FROM audit WHERE order_id = 98' | grep -qx 1 || fail 'the restored trigger did not fire'
app -e 'DELETE FROM orders WHERE id = 98; DELETE FROM audit WHERE order_id = 98'
for record in "$registry"/stepanel_restore_*; do
  [[ -f $record ]] || continue
  [[ $(<"$record") != "pending-restore:$site:$db:"* ]] || fail "restore record ${record##*/} remains: $(<"$record")"
done
shadow=$(M -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE 'stepanel\\_restore\\_%' OR SCHEMA_NAME LIKE 'stepanel\\_retired\\_%'")
[[ -z $shadow ]] || fail "shadow database(s) remain: $shadow"
pass 'the swap rolled forward: backup contents, view and trigger restored, site account unfenced, no shadow or record left'
if (( inconclusive )); then
  echo "database $fill_phase-phase ENOSPC smoke inconclusive: the restore $swap_during_full; the final state was verified but no out-of-space condition was exercised"
elif (( out_of_space + client_out_of_space > 0 )); then
  echo "database $fill_phase-phase ENOSPC smoke passed: out-of-space reported $out_of_space time(s) in the server log and $client_out_of_space time(s) to the helper; restore $swap_during_full"
elif [[ $swap_during_full == completed* ]]; then
  echo "database $fill_phase-phase ENOSPC smoke passed without an out-of-space report: restore $swap_during_full (the $fill_phase phase needed no new space)"
else
  echo "database $fill_phase-phase ENOSPC smoke passed; the restore $swap_during_full, but neither the server log nor the helper reported out-of-space, so the failure cause is unconfirmed"
fi
