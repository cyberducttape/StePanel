#!/usr/bin/env bash
# Verify custom-format PostgreSQL restores do not invoke psql's client
# command interpreter, replace databases exactly, and preserve site ownership.
set -Eeuo pipefail

if [[ $EUID -ne 0 && -z ${STEPANEL_DBCTL_SMOKE_UNPRIVILEGED:-} ]]; then
  echo 'PostgreSQL restore smoke must run as root' >&2
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
if [[ $engine != postgresql ]]; then
  echo "PostgreSQL restore smoke skipped for $engine"
  exit 0
fi

suffix=$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
db="pgr_$suffix"
db_user="pgr_$suffix"
site="pgsmoke-$suffix"
db2="pgs_$suffix"
db_user2="pgs_$suffix"
site2="pgsmoke2-$suffix"
work=$(mktemp -d /tmp/stepanel-restore-pg-smoke.XXXXXX)
chmod 0755 "$work"
marker_shell=/tmp/stepanel-pg-shell-$suffix
marker_copy=/tmp/stepanel-pg-copy-$suffix
marker_output=/tmp/stepanel-pg-output-$suffix

P() { runuser -u postgres -- psql --no-psqlrc --no-password --set ON_ERROR_STOP=1 --tuples-only --no-align "$@"; }
# The site role creates the objects, as an application would.
as_site() {
  if (( $# > 0 )); then P --quiet --dbname "$db" -c "SET ROLE \"$db_user\"" "$@"; else { printf 'SET ROLE "%s";\n' "$db_user"; cat; } | P --quiet --dbname "$db"; fi
}
fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok: $*"; }

cleanup() {
  rm -f -- "$marker_shell" "$marker_copy" "$marker_output"
  P -c "DROP DATABASE IF EXISTS \"pgrvictim_$suffix\"" -c "DROP ROLE IF EXISTS \"pgrevil_$suffix\"" >/dev/null 2>&1 || true
  "$helper" reconcile >/dev/null 2>&1 || true
  "$helper" drop-managed "$db" "$db_user" >/dev/null 2>&1 || true
  "$helper" drop-managed "$db2" "$db_user2" >/dev/null 2>&1 || true
  rm -f -- "$registry/.locks/database-$db.lock" "$registry/.locks/site-$site.lock" "$registry/.locks/user-$db_user.lock" \
    "$registry/.locks/database-$db2.lock" "$registry/.locks/site-$site2.lock" "$registry/.locks/user-$db_user2.lock"
  rm -rf -- "$work"
}
trap cleanup EXIT

snapshot() {
  runuser -u postgres -- pg_dump --no-password --no-owner --no-privileges --schema-only "$db" | grep -v -e '^--' -e '^\\restrict' -e '^\\unrestrict'
  P --dbname "$db" -c 'SELECT id, name FROM parent ORDER BY id' -c 'SELECT msg FROM audit ORDER BY msg'
}

printf '%s\n' "PgSmoke${suffix}Password${suffix}" | "$helper" provision "$db" "$db_user" "$site" UTF8
as_site <<'SQL'
CREATE SCHEMA app;
CREATE TABLE parent (id INT PRIMARY KEY, name TEXT);
CREATE TABLE child (id INT PRIMARY KEY, parent_id INT REFERENCES parent(id));
CREATE TABLE audit (msg TEXT);
CREATE SEQUENCE app.counter;
CREATE FUNCTION log_parent() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO audit VALUES ('parent ' || NEW.id); RETURN NEW; END $$;
CREATE TRIGGER parent_ai AFTER INSERT ON parent FOR EACH ROW EXECUTE FUNCTION log_parent();
CREATE VIEW v_parent AS SELECT id, name FROM parent;
INSERT INTO parent VALUES (1, 'a'), (2, 'b');
INSERT INTO child VALUES (10, 1);
SQL
"$helper" dump "$db" > "$work/backup.dump"
expected=$(snapshot)
as_site -c "INSERT INTO parent VALUES (3, 'c')" -c 'DROP VIEW v_parent' -c 'CREATE TABLE extra (id INT)'
[[ $(snapshot) != "$expected" ]] || fail 'changing the database did not change its snapshot'

"$helper" restore-dump "$db" "$site" < "$work/backup.dump"
[[ $(snapshot) == "$expected" ]] || { diff <(printf '%s\n' "$expected") <(snapshot) | head -40 >&2; fail 'restored database differs from the backup'; }
owners=$(P --dbname "$db" -c "SELECT DISTINCT pg_get_userbyid(relowner) FROM pg_class WHERE relnamespace IN (SELECT oid FROM pg_namespace WHERE nspname IN ('public', 'app'))")
[[ $owners == "$db_user" ]] || fail "restored objects are owned by $owners, not $db_user"
as_site -c "INSERT INTO parent VALUES (5, 'e')" -c 'SELECT COUNT(*) FROM v_parent' -c "SELECT nextval('app.counter')" > /dev/null
[[ $(P --dbname "$db" -c "SELECT COUNT(*) FROM audit WHERE msg = 'parent 5'") == 1 ]] || fail "the restored trigger did not fire for the site's role"
pass "restore replaces the database exactly and the site's role owns and can use every object"

"$helper" restore-dump "$db" "$site" < "$work/backup.dump"
before=$(snapshot)
P -c "CREATE DATABASE \"pgrvictim_$suffix\"" > /dev/null
attacks=(
  "\\! touch $marker_shell"
  "\\copy (SELECT 1) TO PROGRAM 'touch $marker_copy'"
  "\\o $marker_output
SELECT 'this must never be written by psql';
\\o"
)
for attack in "${attacks[@]}"; do
  if printf 'CREATE TABLE innocent (id INT);\n%s\n' "$attack" | "$helper" restore-dump "$db" "$site" 2> "$work/error"; then
    fail "a plain SQL script was accepted as a PostgreSQL restore archive: $attack"
  fi
  grep -q 'plain SQL scripts are not accepted' "$work/error" || fail "a non-archive was not rejected at the restore boundary: $attack: $(cat "$work/error")"
  for marker in "$marker_shell" "$marker_copy" "$marker_output"; do
    [[ ! -e $marker ]] || fail "a psql client command created $marker"
  done
  "$helper" restore-dump "$db" "$site" < "$work/backup.dump"
done
[[ $(snapshot) == "$before" ]] || fail 'an unsafe dump changed the live database'

# Model tenant A in the grant-to-revoke section while a separate helper
# attempts tenant B's restore. The cluster-wide lock must hold B before its
# owner role is granted to the shared loader, so SET ROLE to B stays denied.
printf '%s\n' "PgSmoke${suffix}Password2${suffix}" | "$helper" provision "$db2" "$db_user2" "$site2" UTF8
(
  flock -x 9
  : > "$work/postgres-restore-lock-held"
  sleep 4
) 9>"$registry/.locks/postgres-restore.lock" &
lock_pid=$!
for _ in {1..100}; do
  [[ -e $work/postgres-restore-lock-held ]] && break
  sleep 0.05
done
[[ -e $work/postgres-restore-lock-held ]] || fail 'could not acquire the PostgreSQL restore coordination lock'
P -c "GRANT \"$db_user\" TO stepanel_dbload" >/dev/null
"$helper" restore-dump "$db2" "$site2" < "$work/backup.dump" > "$work/tenant-b.out" 2> "$work/tenant-b.err" &
tenant_b_pid=$!
sleep 1
kill -0 "$tenant_b_pid" 2>/dev/null || fail 'tenant B restore did not wait for the active PostgreSQL restore lock'
[[ $(P -c "SELECT COUNT(*) FROM pg_auth_members WHERE roleid=(SELECT oid FROM pg_roles WHERE rolname='$db_user2') AND member=(SELECT oid FROM pg_roles WHERE rolname='stepanel_dbload')") == 0 ]] || fail 'tenant B owner membership was granted before acquiring the global restore lock'
if runuser -u stepanel_dbload -- psql --no-psqlrc --no-password --dbname "$db2" --command "SET ROLE \"$db_user2\"" >/dev/null 2>&1; then
  fail 'the shared loader could SET ROLE to tenant B while tenant A restore membership was active'
fi
P -c "REVOKE \"$db_user\" FROM stepanel_dbload" >/dev/null
wait "$lock_pid"
if ! wait "$tenant_b_pid"; then fail "tenant B restore failed after lock release: $(cat "$work/tenant-b.err")"; fi
[[ $(P --dbname "$db2" -c 'SELECT COUNT(*) FROM parent') == 2 ]] || fail 'tenant B restore did not complete after lock release'
[[ $(P -c "SELECT COUNT(*) FROM pg_auth_members WHERE member=(SELECT oid FROM pg_roles WHERE rolname='stepanel_dbload')") == 0 ]] || fail 'restore role memberships remain after concurrent restore completion'
pass 'concurrent restores serialize shared loader-role grants; tenant B cannot SET ROLE during tenant A restore'
[[ $(P -c "SELECT COUNT(*) FROM pg_roles WHERE rolname = 'pgrevil_$suffix' OR (rolname = 'stepanel_dbload' AND (rolsuper OR rolcreatedb OR rolcreaterole))") == 0 ]] || fail 'an unsafe dump created or widened a role'
[[ $(P -c "SELECT COUNT(*) FROM pg_database WHERE datname IN ('pgrvictim_$suffix', 'pgr_pwned')") == 1 ]] || fail 'an unsafe dump created or dropped a database'
[[ $(P -c "SELECT COUNT(*) FROM pg_auth_members WHERE member = (SELECT oid FROM pg_roles WHERE rolname = 'stepanel_dbload')") == 0 ]] || fail 'the loader keeps a role membership after restores'
pass 'plain SQL and psql client commands (\\!, \\copy, \\o) are rejected without changing the live database'
echo 'PostgreSQL database restore smoke passed'
