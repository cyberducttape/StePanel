#!/usr/bin/env bash
# Verify client commands in untrusted SQL imports are disabled in both paths.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'database client-command smoke must run as root' >&2; exit 1; }
helper=${STEPANEL_DBCTL:-/usr/local/sbin/stepanel-dbctl}
config=${STEPANEL_DBCTL_CONFIG:-/etc/stepanel-dbctl.conf}
engine=mysql
if [[ -r $config ]]; then
  IFS='=' read -r config_key engine < "$config"
  [[ $config_key == engine ]] || { echo 'invalid database helper configuration' >&2; exit 1; }
fi
if [[ $engine != mysql && $engine != mariadb ]]; then
  echo "database client-command smoke skipped for $engine"
  exit 0
fi
command -v setpriv >/dev/null || { echo 'setpriv is required for database import isolation' >&2; exit 1; }
getent passwd mysql >/dev/null || { echo 'mysql service account is required' >&2; exit 1; }

suffix=$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
site="cmdsafe-$suffix"
database="cmdsafe_$suffix"
db_user="cmdsafe_$suffix"
work=$(mktemp -d /tmp/stepanel-dbctl-client-command.XXXXXX)
source_tmp="/tmp/stepanel-dbctl-client-command-$suffix.source.sql"
marker_shell="/tmp/stepanel-dbctl-client-command-$suffix.shell"
marker_source="/tmp/stepanel-dbctl-client-command-$suffix.source"
password='SafeSmokePassword123456'

cleanup() {
  "$helper" cleanup-wordpress "$database" "$db_user" >/dev/null 2>&1 || true
  "$helper" drop "$database" >/dev/null 2>&1 || true
  rm -f -- "$marker_shell" "$marker_source" "$source_tmp"
  rm -rf -- "$work"
}
trap cleanup EXIT

cat > "$source_tmp" <<SQL
\\! /usr/bin/touch $marker_source
CREATE TABLE client_command_source_probe (id INT PRIMARY KEY);
SQL
chmod 0644 "$source_tmp"
cat > "$work/system-command.sql" <<SQL
\\! /usr/bin/touch $marker_shell
SQL
cat > "$work/source-command.sql" <<SQL
\\. $source_tmp
SQL

assert_client_command_rejected() {
  local path=$1 command_name=$2 dump=$3 marker=$4 dump_bytes
  local log="$work/$path-$command_name.log"
  dump_bytes=$(wc -c < "$dump")
  case "$path" in
    cpmove)
      if "$helper" restore "$database" "$site" "$dump_bytes" >"$log" 2>&1 < "$dump"; then
        echo "cPanel import accepted the $command_name client command" >&2
        return 1
      fi
      ;;
    wordpress)
      if { printf '%s\n' "$password"; cat -- "$dump"; } | \
        "$helper" restore-wordpress "$database" "$db_user" "$site" "$dump_bytes" >"$log" 2>&1; then
        echo "WordPress import accepted the $command_name client command" >&2
        return 1
      fi
      ;;
    *) echo "unknown import path: $path" >&2; return 1 ;;
  esac
  if ! grep -Eiq 'unknown command|sandbox|syntax|command.*not allowed' "$log"; then
    cat "$log" >&2
    echo "$path import failed before demonstrating rejection of $command_name" >&2
    return 1
  fi
  if [[ -e $marker ]]; then
    echo "$path SQL import executed the $command_name local command" >&2
    return 1
  fi
}

assert_client_command_rejected cpmove system "$work/system-command.sql" "$marker_shell"
assert_client_command_rejected cpmove source "$work/source-command.sql" "$marker_source"
assert_client_command_rejected wordpress system "$work/system-command.sql" "$marker_shell"
assert_client_command_rejected wordpress source "$work/source-command.sql" "$marker_source"

echo "database client-command smoke passed for $engine (site $site)"
