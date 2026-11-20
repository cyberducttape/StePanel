#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $EUID -ne 0 ]]; then
  echo 'site package cgroup smoke must run as root' >&2
  exit 1
fi
site=${1:-}
site_user=${2:-}
site_root=${3:-}
[[ $site =~ ^[a-z0-9_-]{1,32}$ && $site_user =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || {
  echo 'invalid site identity' >&2
  exit 1
}
[[ $site_root == "/var/www/sites/$site/public" && -d $site_root && ! -L $site_root ]] || {
  echo 'invalid site document root' >&2
  exit 1
}
[[ $(stat -c '%U' -- "$site_root") == "$site_user" ]] || {
  echo 'site document root is not owned by its site user' >&2
  exit 1
}
command -v systemctl >/dev/null
command -v runuser >/dev/null
command -v setsid >/dev/null

probe_dir=$(mktemp -d /run/stepanel-package-cgroup-smoke.XXXXXX)
chmod 0700 "$probe_dir"
composer=/usr/local/bin/composer
saved_composer="$probe_dir/composer.saved"
had_composer=0
appctl_pid=
active_unit=
marker_prefix="$site_root/.stepanel-cgroup-smoke"

cleanup() {
  if [[ -n $active_unit ]]; then
    systemctl stop "$active_unit" >/dev/null 2>&1 || true
  fi
  if [[ -n $appctl_pid ]]; then
    wait "$appctl_pid" 2>/dev/null || true
  fi
  rm -f -- "$marker_prefix.pid" "$marker_prefix.cgroup"
  if (( had_composer )); then
    rm -f -- "$composer"
    mv -f -- "$saved_composer" "$composer"
  else
    rm -f -- "$composer"
  fi
  rm -rf -- "$probe_dir"
}
trap cleanup EXIT

if [[ -e $composer || -L $composer ]]; then
  mv -- "$composer" "$saved_composer"
  had_composer=1
fi
cat > "$composer" <<EOF
#!/usr/bin/env bash
set -eu
setsid /bin/sh -c 'printf "%s\\n" "\$\$" > "\$1"; cat /proc/self/cgroup > "\$2"; exec /bin/sleep 600' sh '$marker_prefix.pid' '$marker_prefix.cgroup' >/dev/null 2>&1 &
while :; do /bin/sleep 1; done
EOF
chmod 0755 "$composer"
chown root:root "$composer"

/usr/local/sbin/stepanel-appctl composer-install "$site" "$site_root" 0 0 \
  >"$probe_dir/appctl.log" 2>&1 &
appctl_pid=$!

for _ in {1..120}; do
  active_unit=$(systemctl list-units --all --plain --no-legend \
    "stepanel-package-$site-*.service" 2>/dev/null |
    awk '$4 == "running" { print $1; exit }')
  if [[ -n $active_unit && -s $marker_prefix.pid && -s $marker_prefix.cgroup ]]; then
    break
  fi
  if ! kill -0 "$appctl_pid" 2>/dev/null; then
    cat "$probe_dir/appctl.log" >&2
    echo 'package command exited before its detached child was observed' >&2
    exit 1
  fi
  sleep 0.25
done
[[ -n $active_unit && -s $marker_prefix.pid && -s $marker_prefix.cgroup ]] || {
  cat "$probe_dir/appctl.log" >&2
  echo 'timed out waiting for isolated package command and detached child' >&2
  exit 1
}

child_pid=$(cat "$marker_prefix.pid")
[[ $child_pid =~ ^[0-9]+$ ]] || { echo 'invalid detached child pid' >&2; exit 1; }
pid_running() {
  local state
  kill -0 "$1" 2>/dev/null || return 1
  [[ -r "/proc/$1/stat" ]] || return 1
  state=$(awk '{ print $3 }' "/proc/$1/stat")
  [[ $state != Z && $state != X ]]
}
control_group=$(systemctl show "$active_unit" --property=ControlGroup --value)
slice=$(systemctl show "$active_unit" --property=Slice --value)
kill_mode=$(systemctl show "$active_unit" --property=KillMode --value)
tasks_max=$(systemctl show "$active_unit" --property=TasksMax --value)
runtime_max=$(systemctl show "$active_unit" --property=RuntimeMaxUSec --value)
slice_control_group=$(systemctl show "stepanel-site-$site.slice" --property=ControlGroup --value)
child_cgroup=$(sed -n 's/^0:://p' "$marker_prefix.cgroup")

[[ -n $control_group && $child_cgroup == "$control_group" ]] || {
  echo "detached child cgroup $child_cgroup does not match transient unit $control_group" >&2
  exit 1
}
[[ $slice == "stepanel-site-$site.slice" ]] || {
  echo "package unit is in unexpected slice $slice" >&2
  exit 1
}
[[ $kill_mode == control-group && $tasks_max == 256 ]] || {
  echo "package unit limits are KillMode=$kill_mode TasksMax=$tasks_max" >&2
  exit 1
}
case $runtime_max in
  15min|900s|900000000) ;;
  *)
    echo "package unit has unexpected RuntimeMaxUSec=$runtime_max" >&2
    exit 1
    ;;
esac
[[ $slice_control_group == /* && $slice_control_group != *..* ]] || {
  echo "site resource slice returned an unsafe cgroup path: $slice_control_group" >&2
  exit 1
}
memory_limit_file="/sys/fs/cgroup$slice_control_group/memory.max"
[[ -f $memory_limit_file ]] || {
  echo "site resource slice has no cgroup v2 memory.max: $memory_limit_file" >&2
  exit 1
}
slice_memory_max=$(cat "$memory_limit_file")
[[ $slice_memory_max =~ ^[0-9]+$ && $slice_memory_max -gt 0 ]] || {
  echo "site resource slice has no effective memory.max: $slice_memory_max" >&2
  exit 1
}
pid_running "$child_pid" || {
  echo 'detached child exited before the cgroup stop test' >&2
  exit 1
}

stopped_unit=$active_unit
systemctl stop "$stopped_unit"
active_unit=
for _ in {1..40}; do
  if ! pid_running "$child_pid"; then
    break
  fi
  sleep 0.25
done
if pid_running "$child_pid"; then
  echo "detached package descendant $child_pid survived stopping $stopped_unit" >&2
  exit 1
fi
wait "$appctl_pid" 2>/dev/null || true
appctl_pid=

echo "site package cgroup smoke passed: detached child was in $control_group/$slice and stopped with its unit"
