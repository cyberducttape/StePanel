#!/usr/bin/env bash
# Verify that stepanel-dbctl restore-dump replaces a MySQL/MariaDB database
# only by a complete swap. It restores a database with foreign keys, triggers,
# views, routines and events, then checks that a failed load and a killed
# load leave it unchanged, that a helper killed before or after each DDL step
# of the swap keeps the site's logins refused until the next dump,
# restore-dump or reconcile finishes it, that site clients never see a
# partial schema, and that a restore waits for a running dump.
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
  M -e "DROP DATABASE IF EXISTS \`swpvictim_$suffix\`; DROP USER IF EXISTS 'swpevil_$suffix'@'localhost'" 2>/dev/null || true
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

with_injection() {
  # Writes a helper copy that runs the injected line just before the anchor.
  python3 -I - "$helper" "$work/dbctl-crash" "$1" "$2" <<'PY'
import sys
source, target, anchor, injected = sys.argv[1:5]
text = open(source).read()
if text.count(anchor) != 1:
    sys.exit("injection anchor not found exactly once: " + anchor)
open(target, "w").write(text.replace(anchor, injected + "\n" + anchor))
PY
  chmod 0700 "$work/dbctl-crash"
}
# shellcheck disable=SC2016 # the injected lines are helper source
with_crash_point() { with_injection "$1" '  [[ -z ${DBCTL_CRASH:-} ]] || kill -9 "$$" "$BASHPID"'; }

login_refused() { ! app -e 'SELECT 1' > /dev/null 2>&1; }

printf '%s\n' "$password" | "$helper" provision "$db" "$db_user" "$site" utf8mb4
mariadb_server=0
[[ $(M -e 'SELECT VERSION()') == *MariaDB* ]] && mariadb_server=1
# The site's own account creates the objects, as an application would: a
# restore accepts only that account as a definer.
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
CREATE TABLE filler (id INT PRIMARY KEY, body LONGTEXT) ENGINE=InnoDB;
INSERT INTO filler VALUES (1, REPEAT('x', 400000));
SQL
  (( mariadb_server == 0 )) || echo 'CREATE SEQUENCE seq1;'
} | app
"$helper" dump "$db" > "$work/backup.sql"
expected=$(snapshot)

app <<'SQL'
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

# The dump is untrusted: the loader account holds privileges only on the
# shadow, and definers other than the site's account are refused.
M -e "CREATE DATABASE IF NOT EXISTS \`swpvictim_$suffix\`"
attacks=(
  "USE \`swpvictim_$suffix\`; CREATE TABLE pwned (id INT);"
  "CREATE TABLE \`swpvictim_$suffix\`.pwned (id INT);"
  "DROP DATABASE \`swpvictim_$suffix\`;"
  "CREATE USER 'swpevil_$suffix'@'localhost';"
  "GRANT ALL ON *.* TO '$db_user'@'localhost';"
  "/*!40000 CREATE TABLE \`swpvictim_$suffix\`.pwned (id INT) */;"
  "SELECT 1 INTO OUTFILE '/tmp/swp-pwned-$suffix';"
  "CREATE DEFINER='root'@'localhost' SQL SECURITY DEFINER VIEW leak AS SELECT 1 AS x;"
  "system touch /tmp/swp-pwned-$suffix"
)
for attack in "${attacks[@]}"; do
  if printf 'CREATE TABLE innocent (id INT);\n%s\n' "$attack" | "$helper" restore-dump "$db" "$site" 2> "$work/error"; then
    fail "a dump with an unsafe statement was restored: $attack"
  fi
  grep -q 'the live database was not changed' "$work/error" || fail "an unsafe dump did not report an unchanged database: $attack: $(cat "$work/error")"
done
[[ $(snapshot) == "$before" ]] || fail 'an unsafe dump changed the live database'
[[ $(M -e "SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='swpvictim_$suffix'") == 1 ]] || fail 'an unsafe dump dropped another database'
[[ $(M -e "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA='swpvictim_$suffix'") == 0 ]] || fail 'an unsafe dump wrote to another database'
[[ $(M -e "SELECT COUNT(*) FROM mysql.user WHERE User LIKE 'swpevil\\_%' OR User LIKE 'spl\\_%'") == 0 ]] || fail 'an unsafe dump created an account, or a loader account remains'
if M -e "SHOW GRANTS FOR '$db_user'@'localhost'" | grep -F 'ON *.*' | grep -qv 'GRANT USAGE ON'; then fail 'an unsafe dump widened the site account'; fi
[[ ! -e /tmp/swp-pwned-$suffix ]] || fail 'an unsafe dump wrote a file'
M -e "DROP DATABASE \`swpvictim_$suffix\`"
no_restore_left
pass 'dumps that leave the shadow, create accounts, grant, write files or forge definers are refused unchanged'

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
  [after-fence]='    mysql_create_like "$database" "$retired"'
  [after-live-drop]='    mysql_drop_schema_objects "$shadow"'
  [before-rename]='    mysql_generated "$database" "$shadow" "$retired" "SELECT CONCAT('"'"'RENAME TABLE'
  [after-rename]='  moved=$(mysql_base_table_count "$shadow")'
  [after-live-cleanup]='  "$client" --no-defaults --protocol=socket --batch --binary-mode "$database" < "$journal/views.sql"'
  [after-views]='  "$client" --no-defaults --protocol=socket --batch --binary-mode "$database" < "$journal/objects.sql"'
  [before-unfence]='  mysql_set_fence "$database" "$journal" UNLOCK'
)
for point in after-fence after-live-drop before-rename after-rename after-live-cleanup after-views before-unfence; do
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
    login_refused || fail "the site's database login is accepted after a kill at $point, before recovery"
    case $recovery in
      dump) "$helper" dump "$db" > /dev/null ;;
      reconcile) "$helper" reconcile ;;
      restore-dump) "$helper" restore-dump "$db" "$site" < "$work/backup.sql" ;;
    esac
    [[ $(snapshot) == "$expected" ]] || { diff <(printf '%s\n' "$expected") <(snapshot) | head -20 >&2; fail "kill at $point finished by $recovery differs from the backup"; }
    no_restore_left
    app -e 'SELECT n FROM v_nested; SELECT double_it(4)' > /dev/null || fail "the site's database login is refused after a kill at $point finished by $recovery"
    pass "kill at $point keeps site logins refused until $recovery finishes it"
  done
done

# shellcheck disable=SC2016 # the injected line is helper source
with_injection "${anchors[before-rename]}" '    [[ ! -e ${DBCTL_FAIL_ONCE:-/nonexistent} ]] || { rm -f -- "$DBCTL_FAIL_ONCE"; false; }'
M "$db" -e "INSERT IGNORE INTO parent VALUES (9, 'drift')"
touch "$work/fail-once"
DBCTL_FAIL_ONCE="$work/fail-once" "$work/dbctl-crash" restore-dump "$db" "$site" < "$work/backup.sql" 2> "$work/error" || fail "a swap that failed once was not retried: $(cat "$work/error")"
[[ ! -e $work/fail-once ]] || fail 'the injected swap failure was not reached'
[[ $(snapshot) == "$expected" ]] || fail 'a retried swap differs from the backup'
no_restore_left
app -e 'SELECT 1' > /dev/null || fail "the site's database login is refused after a retried swap"
pass 'a failed swap step is retried and completes'

# A site client may be refused or disconnected by the fence, but every
# statement it does run sees the whole schema: views, functions and the
# trigger, whose side effect is checked in the same transaction.
rm -f "$work/reader.stop" "$work/reader.err" "$work/reader.ok"
( probe=100
  while [[ ! -e $work/reader.stop ]]; do
    probe=$((probe + 1))
    if out=$(app -e "START TRANSACTION; INSERT INTO parent VALUES ($probe, 'probe'); SELECT CONCAT('trigger:', COUNT(*)) FROM audit WHERE msg = 'parent $probe'; SELECT CONCAT('view:', n) FROM v_nested; SELECT CONCAT('function:', double_it(4)); COMMIT" 2>&1); then
      if [[ $out == *trigger:1* && $out == *view:* && $out == *function:8* ]]; then echo ok >> "$work/reader.ok"; else printf 'incomplete schema: %s\n' "$out" >> "$work/reader.err"; fi
    else
      case $out in
        *[Ll]ocked*|*"Lost connection"*|*"gone away"*|*"was killed"*|*interrupted*|*"Connection killed"*) ;;
        *) printf 'unexpected error: %s\n' "$out" >> "$work/reader.err" ;;
      esac
    fi
  done ) &
background=$!
sleep 1
for _ in 1 2 3; do "$helper" restore-dump "$db" "$site" < "$work/backup.sql"; done
sleep 1
touch "$work/reader.stop"
wait "$background"
background=''
[[ ! -s $work/reader.err ]] || fail "a site client saw a partial schema during restores: $(sort "$work/reader.err" | uniq -c | head -5)"
[[ $(wc -l < "$work/reader.ok") -ge 2 ]] || fail 'the site client never completed a probe around the restores'
no_restore_left
pass 'site clients were refused or saw the whole schema during restores, never a partial one'

# A restore must wait for a running dump: the dump below blocks on a full
# pipe after its snapshot, while holding the helper's database lock.
"$helper" restore-dump "$db" "$site" < "$work/backup.sql"
before=$(snapshot)
rm -f "$work/dump.fifo"; mkfifo "$work/dump.fifo"
"$helper" dump "$db" > "$work/dump.fifo" &
dump_pid=$!
exec 8< "$work/dump.fifo"
sleep 2
M "$db" -e "INSERT INTO parent VALUES (77, 'drift')"
"$helper" restore-dump "$db" "$site" < "$work/backup.sql" &
background=$!
sleep 3
kill -0 "$background" 2>/dev/null || fail 'a restore finished while a dump was still running'
if grep -lqx "pending-restore:$site:$db:"'.*' "$registry"/stepanel_restore_* 2>/dev/null; then fail 'a restore started while a dump was still running'; fi
cat <&8 > "$work/concurrent.sql"
exec 8<&-
wait "$dump_pid" || fail 'the dump failed'
wait "$background" || fail 'the restore waiting for the dump failed'
background=''
[[ $(snapshot) == "$expected" ]] || fail 'the restore after the dump differs from the backup'
"$helper" restore-dump "$db" "$site" < "$work/concurrent.sql"
[[ $(snapshot) == "$before" ]] || fail 'the dump taken during a waiting restore does not restore its snapshot'
no_restore_left
pass 'a restore waits for a running dump, and that dump restores exactly'

# shellcheck disable=SC2016 # the anchor and injected line are helper source
with_injection '  admit_restore_copy "$database" || { echo "$restore_unchanged_message" >&2; return 1; }' '  restore_reserve_percent=100'
if "$work/dbctl-crash" restore-dump "$db" "$site" < "$work/backup.sql" 2> "$work/error"; then fail 'a restore without room for its copy was admitted'; fi
if ! grep -q 'the restore copy needs about' "$work/error" || ! grep -q 'the live database was not changed' "$work/error"; then fail "a restore without room did not report an unchanged database: $(cat "$work/error")"; fi
no_restore_left
pass 'a restore that would leave the database filesystem below its reserve is refused before loading'

if "$helper" restore-dump "$db" "other-$suffix" < "$work/backup.sql" 2>/dev/null; then fail 'restore to another site was accepted'; fi
no_restore_left
pass 'restore for another site is refused before any shadow is created'
echo 'database restore swap smoke passed'
