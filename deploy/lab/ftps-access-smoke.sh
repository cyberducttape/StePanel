#!/usr/bin/env bash
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'FTPS access smoke must run as root' >&2; exit 1; }
site=${1:-ci-ftps-smoke}
helper=/usr/local/sbin/stepanel-sitectl
vsftpd_bin=$(command -v vsftpd) || { echo 'vsftpd is required for FTPS access smoke' >&2; exit 77; }
command -v curl >/dev/null || { echo 'curl is required for FTPS access smoke' >&2; exit 77; }
command -v openssl >/dev/null || { echo 'openssl is required for FTPS access smoke' >&2; exit 77; }
[[ -f /etc/vsftpd.conf || -f /etc/vsftpd/vsftpd.conf ]] || { echo 'StePanel vsftpd configuration is unavailable' >&2; exit 77; }

tmp=$(mktemp -d /tmp/stepanel-ftps-smoke.XXXXXX)
vsftpd_pid=
sshd_pid=
password='ci-ftps-password-012345'
ftps_diagnostics() {
  echo 'FTPS authentication diagnostics (password hash omitted):' >&2
  getent passwd "$site_user" >&2 || true
  shadow_password=$(getent shadow "$site_user" | cut -d: -f2)
  case $shadow_password in
    '!'*|'*'*) echo 'site account password is locked' >&2 ;;
    '') echo 'site account password is empty' >&2 ;;
    *) echo 'site account password is set and unlocked' >&2 ;;
  esac
  case $shadow_password in
    \$1\$*) hash_scheme=md5 ;;
    \$2*\$*) hash_scheme=bcrypt ;;
    \$5\$*) hash_scheme=sha256 ;;
    \$6\$*) hash_scheme=sha512 ;;
    \$y\$*) hash_scheme=yescrypt ;;
    *) hash_scheme=other ;;
  esac
  echo "site account password hash scheme: $hash_scheme" >&2
  printf '%s\n%s\n' "$password" "$shadow_password" | PYTHONWARNINGS=ignore python3 -c \
    'import sys
try:
 import crypt
 password, stored = (line.rstrip() for line in sys.stdin)
 print("shadow hash matches test password" if crypt.crypt(password, stored) == stored else "shadow hash does not match test password", file=sys.stderr)
except (ImportError, OSError, ValueError):
 print("could not locally verify shadow hash", file=sys.stderr)' || true
  stat -c '%A %U:%G %n' /etc/vsftpd/stepanel.users /etc/pam.d/stepanel-vsftpd >&2 || true
  grep -Fxq "$site_user" /etc/vsftpd/stepanel.users && echo 'site user is in the FTPS allowlist' >&2 || true
  sed -n '1,80p' /etc/pam.d/stepanel-vsftpd >&2 || true
  if [[ -r /etc/pam.d/password-auth ]]; then
    echo 'password-auth policy:' >&2
    sed -n '1,100p' /etc/pam.d/password-auth >&2
  fi
  authselect current 2>&1 | sed -n '1,20p' >&2 || true
  faillock --user "$site_user" 2>&1 | sed -n '1,20p' >&2 || true
  sed -n '1,100p' "$tmp/vsftpd.log" >&2 || true
  for auth_log in /var/log/secure /var/log/auth.log; do
    if [[ -r $auth_log ]]; then
      grep -F "$site_user" "$auth_log" | tail -n 20 >&2 || true
    fi
  done
  journalctl -b --no-pager 2>/dev/null | grep -E "(vsftpd|pam_unix|pam_listfile).*${site_user}" | tail -n 20 >&2 || true
}
cleanup() {
  set +e
  [[ -z ${vsftpd_pid:-} ]] || kill "$vsftpd_pid" 2>/dev/null
  [[ -z ${vsftpd_pid:-} ]] || wait "$vsftpd_pid" 2>/dev/null
  [[ -z ${sshd_pid:-} ]] || kill "$sshd_pid" 2>/dev/null
  [[ -z ${sshd_pid:-} ]] || wait "$sshd_pid" 2>/dev/null
  printf '\n' | "$helper" ftp "$site" 0 >/dev/null 2>&1
  "$helper" delete "$site" >/dev/null 2>&1
  rm -rf -- "$tmp"
}
trap cleanup EXIT

"$helper" prepare "$site"
site_user=$(getent passwd | awk -F: -v home="/var/www/sites/$site" '$6 == home { print $1; exit }')
[[ -n $site_user ]] || { echo 'could not resolve FTPS site user' >&2; exit 1; }
printf '%s' "$password" | "$helper" ftp "$site" 1
grep -Fxq "$site_user" /etc/vsftpd/stepanel.users

openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=localhost' \
  -keyout "$tmp/server.key" -out "$tmp/server.crt" >/dev/null 2>&1
port=$((23000 + ($$ % 1000)))
pasv_min=$((port + 1)); pasv_max=$((port + 1))
cat > "$tmp/vsftpd.conf" <<EOF
listen=YES
listen_ipv6=NO
listen_address=127.0.0.1
listen_port=$port
background=NO
anonymous_enable=NO
local_enable=YES
write_enable=YES
local_umask=022
chroot_local_user=YES
allow_writeable_chroot=YES
check_shell=NO
userlist_enable=YES
userlist_deny=NO
userlist_file=/etc/vsftpd/stepanel.users
pam_service_name=stepanel-vsftpd
ssl_enable=YES
implicit_ssl=NO
force_local_data_ssl=YES
force_local_logins_ssl=YES
require_ssl_reuse=NO
rsa_cert_file=$tmp/server.crt
rsa_private_key_file=$tmp/server.key
pasv_min_port=$pasv_min
pasv_max_port=$pasv_max
pasv_address=127.0.0.1
EOF
"$vsftpd_bin" "$tmp/vsftpd.conf" >"$tmp/vsftpd.log" 2>&1 &
vsftpd_pid=$!
server_ready=0
for _ in {1..20}; do
  if timeout 1 bash -c 'exec 3<>/dev/tcp/127.0.0.1/$1' _ "$port" 2>/dev/null; then server_ready=1; break; fi
  sleep 0.1
done
kill -0 "$vsftpd_pid" 2>/dev/null || { sed -n '1,120p' "$tmp/vsftpd.log" >&2; exit 1; }
(( server_ready == 1 )) || { echo 'temporary FTPS server did not open its loopback port' >&2; exit 1; }
if ! curl --fail --silent --show-error --max-time 5 --ftp-ssl --ssl-reqd --insecure \
  --user "$site_user:$password" --list-only "ftp://127.0.0.1:$port/" >/dev/null; then
  ftps_diagnostics
  exit 1
fi
printf '%s\n' 'FTPS upload/download proof' > "$tmp/upload.txt"
curl --fail --silent --show-error --ftp-ssl --ssl-reqd --insecure --user "$site_user:$password" \
  --upload-file "$tmp/upload.txt" "ftp://127.0.0.1:$port/upload.txt"
curl --fail --silent --show-error --ftp-ssl --ssl-reqd --insecure --user "$site_user:$password" \
  "ftp://127.0.0.1:$port/upload.txt" -o "$tmp/download.txt"
upload_sha=$(sha256sum "$tmp/upload.txt" | awk '{print $1}')
download_sha=$(sha256sum "$tmp/download.txt" | awk '{print $1}')
[[ $upload_sha == "$download_sha" ]] || { echo 'FTPS download content differs from uploaded content' >&2; exit 1; }

# FTPS uses the site account's Unix password, so prove that the managed sshd
# policy blocks that credential even when the surrounding sshd allows it.
ssh_bin=$(command -v ssh) || { echo 'OpenSSH client is required for FTPS/SSH isolation smoke' >&2; exit 77; }
sshd_bin=$(command -v sshd) || { echo 'OpenSSH server is required for FTPS/SSH isolation smoke' >&2; exit 77; }
ssh_port=$((24000 + ($$ % 1000)))
ssh-keygen -q -t ed25519 -N '' -f "$tmp/ssh-host"
install -d -m 0755 /run/sshd
cat > "$tmp/sshd_config" <<EOF
Port $ssh_port
ListenAddress 127.0.0.1
HostKey $tmp/ssh-host
PidFile $tmp/sshd.pid
UsePAM yes
PasswordAuthentication yes
KbdInteractiveAuthentication no
PubkeyAuthentication no
StrictModes no
AllowUsers $site_user
Include /etc/ssh/sshd_config.d/stepanel-ftp-$site_user.conf
EOF
"$sshd_bin" -t -f "$tmp/sshd_config"
effective_password_auth=$("$sshd_bin" -T -f "$tmp/sshd_config" -C "user=$site_user,host=localhost,addr=127.0.0.1" | awk '$1 == "passwordauthentication" { print $2; exit }')
[[ $effective_password_auth == no ]] || { echo 'FTPS-enabled site account still permits SSH password authentication' >&2; exit 1; }
"$sshd_bin" -D -e -f "$tmp/sshd_config" >"$tmp/sshd.log" 2>&1 &
sshd_pid=$!
for _ in {1..20}; do
  if ssh-keyscan -T 1 -p "$ssh_port" 127.0.0.1 >/dev/null 2>&1; then break; fi
  sleep 0.1
done
kill -0 "$sshd_pid" 2>/dev/null || { sed -n '1,120p' "$tmp/sshd.log" >&2; exit 1; }
cat > "$tmp/askpass" <<EOF
#!/bin/sh
printf '%s\\n' '$password'
EOF
chmod 0700 "$tmp/askpass"
set +e
SSH_ASKPASS="$tmp/askpass" SSH_ASKPASS_REQUIRE=force DISPLAY=:stepanel-ftps-smoke \
  "$ssh_bin" -vv -p "$ssh_port" -o PreferredAuthentications=password \
  -o PubkeyAuthentication=no -o NumberOfPasswordPrompts=1 \
  -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
  "$site_user@127.0.0.1" true >"$tmp/ssh-client.log" 2>&1 </dev/null
ssh_status=$?
set -e
(( ssh_status != 0 )) || { echo 'FTPS password unexpectedly opened an SSH session' >&2; exit 1; }
if grep -Fq 'Authenticated to ' "$tmp/ssh-client.log"; then
  echo 'FTPS password authenticated over SSH despite the per-user policy' >&2
  sed -n '1,120p' "$tmp/ssh-client.log" >&2
  exit 1
fi

# FTPS and SSH/SFTP keys share the account password field: installing keys
# must keep the FTPS password, and revoking FTPS must keep the keys usable.
ssh-keygen -q -t ed25519 -N '' -f "$tmp/client"
"$helper" access "$site" 1 0 < "$tmp/client.pub"
curl --fail --silent --show-error --ftp-ssl --ssl-reqd --insecure --user "$site_user:$password" \
  "ftp://127.0.0.1:$port/upload.txt" -o "$tmp/after-ssh.txt" || { echo 'enabling SFTP keys replaced the FTPS password' >&2; exit 1; }

printf '\n' | "$helper" ftp "$site" 0
[[ $(getent shadow "$site_user" | cut -d: -f2) != '!'* ]] || { echo 'revoking FTPS locked out installed SSH/SFTP keys' >&2; exit 1; }
set +e
curl --silent --ftp-ssl --ssl-reqd --insecure --user "$site_user:$password" \
  "ftp://127.0.0.1:$port/upload.txt" -o "$tmp/revoked.txt"
revoked_status=$?
set -e
(( revoked_status != 0 )) || { echo 'revoked FTPS account still authenticated' >&2; exit 1; }
if grep -Fxq "$site_user" /etc/vsftpd/stepanel.users; then echo 'revoked FTPS account is still allowlisted' >&2; exit 1; fi
"$helper" access "$site" 0 0 < /dev/null
[[ $(getent shadow "$site_user" | cut -d: -f2) == '!'* ]] || { echo 'site account stayed unlocked with neither FTPS nor SSH access' >&2; exit 1; }
echo 'FTPS upload/download, SSH password isolation, and revocation smoke passed'
