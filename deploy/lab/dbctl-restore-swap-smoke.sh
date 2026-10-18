#!/usr/bin/env bash
# Verify that stepanel-dbctl restore-dump replaces a MySQL/MariaDB database
# only by a complete swap. It restores a database with foreign keys, triggers,
# views, routines and events, then checks that a failed load and a killed
# load leave it unchanged, and that a helper killed at each step of the swap
# is finished by the next dump, restore-dump or reconcile.
set -Eeuo pipefail

if [[ $EUID -ne 0 && -z ${STEPANEL_DBCTL_SMOKE_UNPRIVILEGED:-} ]]; then
  echo 'database restore swap smoke must run as root' >&2
  exit 1
fi
helper=${STEPANEL_DBCTL:-/usr/local/sbin/stepanel-dbctl}
registry=${STEPANEL_DBCTL_REGISTRY:-/var/lib/stepanel-privileged/db-managed}
config=${STEPANEL_DBCTL_CONFIG:-/etc/stepanel-dbctl.conf}
engine=mysql
if [[ -r $config ]]; then
  IFS='=' read -r config_key engine < "$config"
  [[ $config_key == engine ]] || { echo 'invalid database helper configuration' >&2; exit 1; }
fi
if [[ $engine != mysql && $engine != mariadb ]]; then
  echo "database restore swap smoke skipped for $engine"
  exit 0
fi
client=$(command -v mariadb || command -v mysql) || { echo 'no MySQL/MariaDB client' >&2; exit 1; }
dump_client=$(command -v mariadb-dump || command -v mysqldump) || { echo 'no MySQL/MariaDB dump client' >&2; exit 1; }
command -v python3 >/dev/null || { echo 'python3 is required' >&2; exit 1; }

suffix=$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
db="swp_$suffix"
db_user="swp_$suffix"
site="swapsmoke-$suffix"
password="Swap${suffix}Smoke${suffix}pw"
work=$(mktemp -d /tmp/stepanel-restore-swap-smoke.XXXXXX)
background=''

M() { "$client" --no-defaults --protocol=socket --batch --raw --skip-column-names "$@"; }
app() { MYSQL_PWD=$password "$client" --no-defaults --protocol=socket --user="$db_user" --batch --skip-column-names "$db" "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

cleanup() {
  if [[ -n $background ]]; then kill -9 "$background" 2>/dev/null || true; wait "$background" 2>/dev/null || true; fi
  local record value
  for record in "$registry"/stepanel_restore_*; do
    [[ -f $record ]] || continue
    value=$(<"$record")
    [[ $value == "pending-restore:$site:$db:"* ]] || continue
    M -e "DROP DATABASE IF EXISTS \`${record##*/}\`; DROP DATABASE IF EXISTS \`stepanel_retired_${record##*/stepanel_restore_}\`" || true
    rm -rf -- "$registry/.restores/${record##*/stepanel_restore_}"
    rm -f -- "$record"
  done
  "$helper" drop-managed "$db" "$db_user" >/dev/null 2>&1 || true
  rm -f -- "$registry/.locks/database-$db.lock" "$registry/.locks/site-$site.lock" "$registry/.locks/user-$db_user.lock"
  rm -rf -- "$work"
}
trap cleanup EXIT

snapshot() {
  "$dump_client" --no-defaults --protocol=socket --skip-comments --skip-dump-date --routines --events --triggers --order-by-primary "$db" \
    | grep -v '^/\*M!999999' | sed -e 's/ AUTO_INCREMENT=[0-9]*//'
}

no_restore_left() {
  local record shadow
  for record in "$registry"/stepanel_restore_*; do
    [[ -f $record ]] || continue
    [[ $(<"$record") != "pending-restore:$site:$db:"* ]] || fail "restore record ${record##*/} remains: $(<"$record")"
  done
  for shadow in $(M -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE 'stepanel\\_restore\\_%' OR SCHEMA_NAME LIKE 'stepanel\\_retired\\_%'"); do
    [[ -f $registry/stepanel_restore_${shadow##*_} ]] || fail "database $shadow remains without a restore record"
  done
}

with_crash_point() {
  # Writes a helper copy that SIGKILLs itself just before the anchor line.
  python3 -I - "$helper" "$work/dbctl-crash" "$1" <<'PY'
import sys
source, target, anchor = sys.argv[1:4]
text = open(source).read()
if text.count(anchor) != 1:
    sys.exit("crash anchor not found exactly once: " + anchor)
open(target, "w").write(text.replace(anchor, '  [[ -z ${DBCTL_CRASH:-} ]] || kill -9 "$$" "$BASHPID"\n' + anchor))
PY
  chmod 0700 "$work/dbctl-crash"
}

printf '%s\n' "$password" | "$helper" provision "$db" "$db_user" "$site" utf8mb4
mariadb_server=0
[[ $(M -e 'SELECT VERSION()') == *MariaDB* ]] && mariadb_server=1
{
  cat <<'SQL'
CREATE TABLE parent (id INT PRIMARY KEY, name VARCHAR(20)) ENGINE=InnoDB;
CREATE TABLE child (id INT PRIMARY KEY, parent_id INT, FOREIGN KEY (parent_id) REFERENCES parent(id)) ENGINE=InnoDB;
CREATE TABLE `odd``name` (id INT PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE audit (msg VARCHAR(50)) ENGINE=InnoDB;
CREATE TRIGGER parent_ai AFTER INSERT ON parent FOR EACH ROW INSERT INTO audit VALUES (CONCAT('parent ', NEW.id));
CREATE VIEW v_parent AS SELECT id, name FROM parent;
CREATE VIEW v_nested AS SELECT COUNT(*) AS n FROM v_parent;
CREATE PROCEDURE count_parents() SELECT COUNT(*) FROM parent;
CREATE FUNCTION double_it(x INT) RETURNS INT DETERMINISTIC NO SQL RETURN x * 2;
CREATE EVENT nightly ON SCHEDULE EVERY 1 DAY DO DELETE FROM audit WHERE msg = 'never';
INSERT INTO parent VALUES (1, 'a'), (2, 'b');
INSERT INTO child VALUES (10, 1), (20, 2);
INSERT INTO `odd``name` VALUES (7);
SQL
  (( mariadb_server == 0 )) || echo 'CREATE SEQUENCE seq1;'
} | M "$db"
"$helper" dump "$db" > "$work/backup.sql"
expected=$(snapshot)

M "$db" <<'SQL'
INSERT INTO parent VALUES (3, 'c');
CREATE TABLE extra (id INT);
DROP VIEW v_nested;
DROP TRIGGER parent_ai;
CREATE TRIGGER other_ai AFTER INSERT ON child FOR EACH ROW INSERT INTO audit VALUES ('child');
DROP PROCEDURE count_parents;
SQL
[[ $(snapshot) != "$expected" ]] || fail 'changing the database did not change its snapshot'

"$helper" restore-dump "$db" "$site" < "$work/backup.sql"
[[ $(snapshot) == "$expected" ]] || { diff <(printf '%s\n' "$expected") <(snapshot) | head -40 >&2; fail 'restored database differs from the backup'; }
no_restore_left
app -e "INSERT INTO parent VALUES (5, 'e'); SELECT n FROM v_nested; CALL count_parents(); SELECT double_it(4); SELECT msg FROM audit" > "$work/app.out"
grep -qx 'parent 5' "$work/app.out" || fail "restored trigger did not fire for the site's database user"
grep -qx 8 "$work/app.out" || fail 'restored function is missing'
[[ $(M -e "SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA='$db'") == 1 ]] || fail 'restored event is missing'
pass "restore replaces the database exactly and the site's user can use every object"
"$helper" restore-dump "$db" "$site" < "$work/backup.sql"

before=$(snapshot)
if { head -c 2000 "$work/backup.sql"; printf '\nTHIS IS NOT SQL;\n'; } | "$helper" restore-dump "$db" "$site" 2> "$work/error"; then
  fail 'a broken dump was restored'
fi
grep -q 'the live database was not changed' "$work/error" || fail "failed load did not report an unchanged database: $(cat "$work/error")"
[[ $(snapshot) == "$before" ]] || fail 'failed load changed the live database'
no_restore_left
pass 'failed load leaves the live database unchanged'

mkfifo "$work/fifo"
( exec setsid "$helper" restore-dump "$db" "$site" < "$work/fifo" > /dev/null 2>&1 ) &
background=$!
exec 9> "$work/fifo"
head -c 3000 "$work/backup.sql" >&9
sleep 2
grep -lqx "pending-restore:$site:$db:load" "$registry"/stepanel_restore_* || fail 'no load-phase restore record while loading'
kill -9 -- "-$background" 2>/dev/null || kill -9 "$background"
exec 9>&-
wait "$background" 2>/dev/null || true
background=''
[[ $(snapshot) == "$before" ]] || fail 'killed load changed the live database'
"$helper" reconcile
no_restore_left
[[ $(snapshot) == "$before" ]] || fail 'reconcile changed the live database'
pass 'killed load leaves the live database unchanged and reconcile drops the shadow'

# shellcheck disable=SC2016 # the anchors are literal helper source lines
declare -A anchors=(
  [before-rename]='    mysql_generated "$database" "$shadow" "$retired" "SELECT CONCAT('"'"'RENAME TABLE'
  [after-rename]='  moved=$(mysql_base_table_count "$shadow")'
  [after-views]='  "$client" --no-defaults --protocol=socket --batch --binary-mode "$database" < "$journal/objects.sql"'
)
for point in before-rename after-rename after-views; do
  with_crash_point "${anchors[$point]}"
  for recovery in dump reconcile restore-dump; do
    M "$db" -e "INSERT IGNORE INTO parent VALUES (9, 'drift'); DROP VIEW IF EXISTS v_nested"
    if DBCTL_CRASH=1 "$work/dbctl-crash" restore-dump "$db" "$site" < "$work/backup.sql" 2>/dev/null; then
      fail "crash point $point was not reached"
    fi
    # Nothing may keep running after the kill: the swap stays half done
    # until a recovery path finishes it.
    sleep 2
    grep -lqx "pending-restore:$site:$db:swap" "$registry"/stepanel_restore_* || fail "no swap-phase restore record after a kill at $point"
    case $recovery in
      dump) "$helper" dump "$db" > /dev/null ;;
      reconcile) "$helper" reconcile ;;
      restore-dump) "$helper" restore-dump "$db" "$site" < "$work/backup.sql" ;;
    esac
    [[ $(snapshot) == "$expected" ]] || { diff <(printf '%s\n' "$expected") <(snapshot) | head -20 >&2; fail "kill at $point finished by $recovery differs from the backup"; }
    no_restore_left
    pass "kill at $point is finished by $recovery"
  done
done

rm -f "$work/reader.stop" "$work/reader.err"
( while [[ ! -e $work/reader.stop ]]; do
    app -e 'SELECT COUNT(*) FROM parent; SELECT COUNT(*) FROM child' > /dev/null 2>> "$work/reader.err" || echo 'read failed' >> "$work/reader.err"
  done ) &
background=$!
for _ in 1 2 3; do "$helper" restore-dump "$db" "$site" < "$work/backup.sql"; done
touch "$work/reader.stop"
wait "$background"
background=''
[[ ! -s $work/reader.err ]] || fail "table reads failed during restores: $(sort "$work/reader.err" | uniq -c)"
no_restore_left
pass 'concurrent table reads by the site user never failed during restores'

if "$helper" restore-dump "$db" "other-$suffix" < "$work/backup.sql" 2>/dev/null; then fail 'restore to another site was accepted'; fi
no_restore_left
pass 'restore for another site is refused before any shadow is created'
echo 'database restore swap smoke passed'
