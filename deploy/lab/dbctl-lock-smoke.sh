#!/usr/bin/env bash
# Verify independent managed-database operations are not globally serialized.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'database lock smoke must run as root' >&2; exit 1; }
config=/etc/stepanel-dbctl.conf
engine=mysql
if [[ -r $config ]]; then
  IFS='=' read -r config_key engine < "$config"
  [[ $config_key == engine ]] || { echo 'invalid database helper configuration' >&2; exit 1; }
fi
if [[ $engine != mysql && $engine != mariadb ]]; then
  echo "database lock smoke skipped for $engine"
  exit 0
fi

helper=/usr/local/sbin/stepanel-dbctl
registry=/var/lib/stepanel-privileged/db-managed
lock_dir="$registry/.locks"
install -d -m 0700 -o root -g root "$registry" "$lock_dir"
work=$(mktemp -d /tmp/stepanel-db-lock-smoke.XXXXXX)
suffix=$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
slow_db="lockslow_$suffix"
fast_db="lockfast_$suffix"
slow_user="lockslow_$suffix"
fast_user="lockfast_$suffix"
site="locksmoke-$suffix"
slow_record="$registry/$slow_db"
fast_record="$registry/$fast_db"
slow_pid=''
fast_pid=''

cleanup() {
  if [[ -n $slow_pid ]]; then kill "$slow_pid" 2>/dev/null || true; wait "$slow_pid" 2>/dev/null || true; fi
  if [[ -n $fast_pid ]]; then kill "$fast_pid" 2>/dev/null || true; wait "$fast_pid" 2>/dev/null || true; fi
  rm -f -- "$slow_record" "$fast_record" \
    "$lock_dir/database-$slow_db.lock" "$lock_dir/database-$fast_db.lock" \
    "$lock_dir/site-$site.lock" "$lock_dir/user-$slow_user.lock" "$lock_dir/user-$fast_user.lock"
  rm -rf -- "$work"
}
trap cleanup EXIT

[[ ! -e $slow_record && ! -e $fast_record ]] || { echo 'database lock smoke name collision' >&2; exit 1; }
printf 'panel:%s:%s\n' "$site" "$slow_user" > "$slow_record"
printf 'panel:%s:%s\n' "$site" "$fast_user" > "$fast_record"
chmod 0600 "$slow_record" "$fast_record"

cat > "$work/mariadb" <<'MOCK'
#!/usr/bin/env bash
set -Eeuo pipefail
statement=$(cat)
case "$statement" in
  *"ALTER USER 'lockslow_"*)
    : > "$DBCTL_LOCK_SMOKE_DIR/slow-started"
    sleep 8
    : > "$DBCTL_LOCK_SMOKE_DIR/slow-done"
    ;;
  *"ALTER USER 'lockfast_"*)
    : > "$DBCTL_LOCK_SMOKE_DIR/fast-done"
    ;;
  *) echo "unexpected SQL in lock smoke: $statement" >&2; exit 1 ;;
esac
MOCK
chmod 0755 "$work/mariadb"

printf '%s\n' 'LockSmokePassword_123456789' | \
  PATH="$work:$PATH" DBCTL_LOCK_SMOKE_DIR="$work" "$helper" rotate "$slow_db" "$slow_user" \
  >"$work/slow.log" 2>&1 &
slow_pid=$!

for _ in $(seq 1 50); do
  [[ -e $work/slow-started ]] && break
  sleep 0.1
done
[[ -e $work/slow-started ]] || { echo 'slow database operation did not reach its SQL call' >&2; exit 1; }

printf '%s\n' 'LockSmokePassword_123456789' | \
  PATH="$work:$PATH" DBCTL_LOCK_SMOKE_DIR="$work" "$helper" rotate "$fast_db" "$fast_user" \
  >"$work/fast.log" 2>&1 &
fast_pid=$!

for _ in $(seq 1 40); do
  [[ -e $work/fast-done ]] && break
  sleep 0.1
done
if [[ ! -e $work/fast-done ]] || ! kill -0 "$slow_pid" 2>/dev/null; then
  echo 'independent database operation was blocked behind the slow operation' >&2
  cat "$work/slow.log" "$work/fast.log" >&2 || true
  exit 1
fi
wait "$fast_pid"
fast_pid=''
wait "$slow_pid"
slow_pid=''
[[ -e $work/slow-done ]] || { echo 'slow database operation did not complete' >&2; exit 1; }
echo 'database lock smoke passed (independent database operations overlapped)'
