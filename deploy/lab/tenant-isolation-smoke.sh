#!/usr/bin/env bash
# Host-level isolation between two ordinary sites on an installed host.
#
# Each site's Unix account is the identity its PHP-FPM workers, SFTP session
# and scheduled tasks run as. Acting as one site's account, try to read, list,
# traverse and write the other site's tree and private PHP state, and to
# connect to the other site's PHP-FPM socket. Every attempt must be refused.
# Also check that no site account belongs to the web server group or to the
# other site's group, which would widen every group-readable path.
#
# Run after install-smoke.sh on a disposable --unsafe-lab host.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'tenant isolation smoke must run as root' >&2; exit 1; }
helper=/usr/local/sbin/stepanel-sitectl
sites=(ci-isolation-a ci-isolation-b)
web_group=$(sed -n 's/^STEPANEL_WEB_GROUP="\([a-z_][a-z0-9_-]*\)"$/\1/p' /etc/ste-panel.env)
[[ -n $web_group ]] || { echo 'could not read the configured web server group' >&2; exit 1; }

failures=()
cleanup() {
  set +e
  for site in "${sites[@]}"; do "$helper" delete "$site" >/dev/null 2>&1; done
}
trap cleanup EXIT

declare -A user root socket
for site in "${sites[@]}"; do
  "$helper" prepare "$site" >/dev/null
  root[$site]=/var/www/sites/$site
  user[$site]=$(getent passwd | awk -F: -v home="${root[$site]}" '$6 == home { print $1; exit }')
  [[ ${user[$site]} =~ ^sp-[a-z0-9-]+$ ]] || { echo "could not resolve the account of $site" >&2; exit 1; }
  pool=$(compgen -G "/etc/php-fpm.d/stepanel-$site.conf" || compgen -G "/etc/php/*/fpm/pool.d/stepanel-$site.conf" | head -1)
  [[ -n $pool ]] || { echo "no PHP-FPM pool was written for $site" >&2; exit 1; }
  socket[$site]=$(sed -n 's/^listen = //p' "$pool")
  for _ in $(seq 1 30); do [[ -S ${socket[$site]} ]] && break; sleep 1; done
  [[ -S ${socket[$site]} ]] || { echo "PHP-FPM socket for $site did not appear: ${socket[$site]}" >&2; exit 1; }
  printf 'secret-of-%s\n' "$site" > "${root[$site]}/public/tenant-secret.txt"
  printf 'session-of-%s\n' "$site" > "${root[$site]}/.php/sessions/sess_isolation"
  chown "${user[$site]}:${user[$site]}" "${root[$site]}/public/tenant-secret.txt" "${root[$site]}/.php/sessions/sess_isolation"
  chmod 0640 "${root[$site]}/public/tenant-secret.txt"
  chmod 0600 "${root[$site]}/.php/sessions/sess_isolation"
done

refused() {
  local label=$1; shift
  if "$@" >/dev/null 2>&1; then failures+=("$label"); fi
}
# Sanity: each account can use its own tree and socket, so a refusal below is
# a permission decision, not a broken fixture.
can_connect() {
  runuser -u "$1" -- python3 -I - "$2" <<'PY'
import socket
import sys
connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
connection.settimeout(3)
connection.connect(sys.argv[1])
connection.close()
PY
}
for site in "${sites[@]}"; do
  runuser -u "${user[$site]}" -- test -r "${root[$site]}/public/tenant-secret.txt" \
    || failures+=("${user[$site]} cannot read its own site file")
  can_connect "${user[$site]}" "${socket[$site]}" >/dev/null 2>&1 \
    || failures+=("${user[$site]} cannot connect to its own PHP-FPM socket")
done

for index in 0 1; do
  self=${sites[$index]} other=${sites[$((1 - index))]}
  me=${user[$self]} them=${root[$other]}
  refused "$me reads $other's public file" runuser -u "$me" -- cat "$them/public/tenant-secret.txt"
  refused "$me reads $other's PHP session" runuser -u "$me" -- cat "$them/.php/sessions/sess_isolation"
  refused "$me lists $other's site root" runuser -u "$me" -- ls "$them"
  refused "$me lists $other's public tree" runuser -u "$me" -- ls "$them/public"
  refused "$me traverses $other's site root" runuser -u "$me" -- test -x "$them"
  refused "$me writes into $other's public tree" runuser -u "$me" -- sh -c 'printf x > "$1"' sh "$them/public/intrusion.php"
  refused "$me overwrites $other's public file" runuser -u "$me" -- sh -c 'printf x >> "$1"' sh "$them/public/tenant-secret.txt"
  refused "$me writes into $other's PHP state" runuser -u "$me" -- sh -c 'printf x > "$1"' sh "$them/.php/sessions/sess_intrusion"
  refused "$me connects to $other's PHP-FPM socket" can_connect "$me" "${socket[$other]}"
  groups=" $(id -nG "$me") "
  [[ $groups != *" $web_group "* ]] || failures+=("$me is a member of the web server group $web_group")
  [[ $groups != *" ${user[$other]} "* ]] || failures+=("$me is a member of $other's group")
done
[[ ! -e ${root[ci-isolation-a]}/public/intrusion.php && ! -e ${root[ci-isolation-b]}/public/intrusion.php ]] \
  || failures+=('a cross-site write left a file behind')
[[ $(cat "${root[ci-isolation-b]}/public/tenant-secret.txt") == 'secret-of-ci-isolation-b' ]] \
  || failures+=("ci-isolation-b's file was modified")

if (( ${#failures[@]} )); then
  printf 'tenant isolation violation: %s\n' "${failures[@]}" >&2
  exit 1
fi
echo 'tenant isolation smoke passed: cross-site read, list, traverse, write, session access, and PHP-FPM socket access refused in both directions'
