#!/usr/bin/env bash
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'SFTP access smoke must run as root' >&2; exit 1; }
site=${1:-ci-sftp-smoke}
helper=/usr/local/sbin/stepanel-sitectl
command -v sshd >/dev/null || { echo 'sshd is required for SFTP access smoke' >&2; exit 77; }
command -v sftp >/dev/null || { echo 'sftp client is required for SFTP access smoke' >&2; exit 77; }
sshd_bin=$(readlink -f "$(command -v sshd)")
[[ -x $sshd_bin ]] || { echo 'could not resolve an executable sshd path' >&2; exit 1; }

tmp=$(mktemp -d /tmp/stepanel-sftp-smoke.XXXXXX)
sshd_pid=
cleanup() {
  set +e
  [[ -z ${sshd_pid:-} ]] || kill "$sshd_pid" 2>/dev/null
  [[ -z ${sshd_pid:-} ]] || wait "$sshd_pid" 2>/dev/null
  printf '\n' | "$helper" access "$site" 0 0 >/dev/null 2>&1
  "$helper" delete "$site" >/dev/null 2>&1
  rm -rf -- "$tmp"
}
trap cleanup EXIT

"$helper" prepare "$site"
site_user=$(getent passwd | awk -F: -v home="/var/www/sites/$site" '$6 == home { print $1; exit }')
[[ $site_user =~ ^sp-[a-z0-9-]+$ ]] || { echo 'could not resolve freshly provisioned SFTP site user' >&2; exit 1; }
[[ ! -d "/var/www/sites/$site/.ssh" ]] || { echo 'SFTP smoke found a customer-controlled SSH staging directory' >&2; exit 1; }

ssh-keygen -q -t ed25519 -N '' -f "$tmp/client"
cat "$tmp/client.pub" | "$helper" access "$site" 1 0
authorized_keys=/etc/stepanel/ssh/authorized_keys/$site_user
[[ -f $authorized_keys && ! -L $authorized_keys ]] || { echo 'SFTP authorized-key file was not published' >&2; exit 1; }
grep -Fq 'restrict,command="internal-sftp" ssh-ed25519 ' "$authorized_keys" || { echo 'SFTP key was not restricted to internal-sftp' >&2; exit 1; }

ssh-keygen -q -t ed25519 -N '' -f "$tmp/host"
port=$((22000 + ($$ % 1000)))
install -d -m 0755 /run/sshd
cat > "$tmp/sshd_config" <<EOF
Port $port
ListenAddress 127.0.0.1
HostKey $tmp/host
PidFile $tmp/sshd.pid
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AuthorizedKeysFile none
StrictModes no
AllowUsers $site_user
Subsystem sftp internal-sftp
Include /etc/ssh/sshd_config.d/stepanel-$site_user.conf
EOF
"$sshd_bin" -t -f "$tmp/sshd_config"
"$sshd_bin" -D -e -f "$tmp/sshd_config" >"$tmp/sshd.log" 2>&1 &
sshd_pid=$!
for _ in {1..20}; do
  if ssh-keyscan -T 1 -p "$port" 127.0.0.1 >/dev/null 2>&1; then break; fi
  sleep 0.1
done
kill -0 "$sshd_pid" 2>/dev/null || { sed -n '1,120p' "$tmp/sshd.log" >&2; exit 1; }

printf 'pwd\nquit\n' | sftp -q -P "$port" -i "$tmp/client" \
  -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  -b - "$site_user@127.0.0.1" >/dev/null
set +e
ssh -q -p "$port" -i "$tmp/client" -o BatchMode=yes -o StrictHostKeyChecking=no \
  -o UserKnownHostsFile=/dev/null "$site_user@127.0.0.1" true >/dev/null 2>&1
shell_status=$?
set -e
(( shell_status != 0 )) || { echo 'SFTP-only account accepted a shell command' >&2; exit 1; }
echo 'SFTP-only OpenSSH client/server smoke passed'
