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
  rm -f -- "$registry/.locks/database-$db.lock" "$registry/.locks/site-$site.lock" "$registry/.locks/user-$db_user.lock"
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
[[ $(P -c "SELECT COUNT(*) FROM pg_roles WHERE rolname = 'pgrevil_$suffix' OR (rolname = 'stepanel_dbload' AND (rolsuper OR rolcreatedb OR rolcreaterole))") == 0 ]] || fail 'an unsafe dump created or widened a role'
[[ $(P -c "SELECT COUNT(*) FROM pg_database WHERE datname IN ('pgrvictim_$suffix', 'pgr_pwned')") == 1 ]] || fail 'an unsafe dump created or dropped a database'
[[ $(P -c "SELECT COUNT(*) FROM pg_auth_members WHERE member = (SELECT oid FROM pg_roles WHERE rolname = 'stepanel_dbload')") == 0 ]] || fail 'the loader keeps a role membership after restores'
pass 'plain SQL and psql client commands (\\!, \\copy, \\o) are rejected without changing the live database'
echo 'PostgreSQL database restore smoke passed'
