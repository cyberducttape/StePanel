#!/usr/bin/env bash
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'install smoke must run as root' >&2; exit 1; }
cd /work
bash /work/deploy/lab/python-dependencies-smoke.sh

if [[ ! -f /sys/fs/cgroup/cgroup.controllers && ! -d /sys/fs/cgroup/systemd ]]; then
  echo 'install smoke requires a systemd-compatible cgroup hierarchy; use a KVM/Cloud VM or CI runner' >&2
  exit 77
fi

export STEPANEL_ENV=production
if [[ ${STEPANEL_QUOTA_SMOKE:-0} != 1 ]]; then
  export STEPANEL_SKIP_QUOTA_CHECK=1
else
  if [[ ${STEPANEL_SKIP_QUOTA_CHECK:-0} == 1 ]]; then
    echo 'quota smoke must not bypass production quota validation' >&2
    exit 1
  fi
  if ! awk '$2 == "/var/www" && $4 ~ /(^|,)usrquota(,|$)/ { found=1 } END { exit !found }' /proc/mounts; then
    echo '/var/www is not mounted with user quotas enabled' >&2
    exit 1
  fi
fi
export STEPANEL_SKIP_STARTUP_DB_RECONCILE=1
export STEPANEL_SKIP_STARTUP_HOST_RECONCILE=1
export STEPANEL_LAB_HTTP_COOKIES=1
export STEPANEL_LAB_DIRECT_ROOT_BROKER=1
export STEPANEL_LISTEN=127.0.0.1:8090
export STEPANEL_TLS_TERMINATED=1
export STEPANEL_ADMIN_PASSWORD=ci-install-only-password
export STEPANEL_ADMIN_TOTP_SECRET=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP
export STEPANEL_SESSION_SECRET=ci-install-session-secret-012345678901234567890123
export STEPANEL_ACCOUNT_KEY=ci-install-account-key-012345678901234567890123
export STEPANEL_AUDIT_KEY=ci-install-audit-key-012345678901234567890123456789
export STEPANEL_DB_ENGINE=${STEPANEL_DB_ENGINE:-mariadb}
export STEPANEL_WEBSERVER=${STEPANEL_WEBSERVER:-caddy}
export STEPANEL_DB_VERSION=default
export STEPANEL_PANEL_HOSTNAME=panel.example.test
export STEPANEL_INSTALL_DB_ADMIN=0
export STEPANEL_INSTALL_MAIL=0
export STEPANEL_INSTALL_FTP=0
export STEPANEL_INSTALL_NODE=0
export STEPANEL_INSTALL_SECURITY=0
export STEPANEL_REQUIRE_OFFSITE_BACKUP=1
export STEPANEL_OFFSITE_TARGET=local:/tmp/stepanel-offsite

if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y rclone
else
  dnf install -y epel-release
  dnf install -y rclone
fi

# The post-install readiness probe now verifies offsite write/read/delete, so
# provide the harmless local rclone remote before the installer starts the
# panel. RCLONE_CONFIG is explicitly preserved by install.sh into the systemd
# environment; provider configuration remains operator-managed in production.
install -d -m 0755 -o root -g root /etc/stepanel
printf '[local]\ntype = local\n' > /etc/stepanel/rclone.conf
chmod 0644 /etc/stepanel/rclone.conf
export RCLONE_CONFIG=/etc/stepanel/rclone.conf

if ! ./install.sh --unsafe-lab; then
  systemctl status stepanel.service stepanel-worker.service --no-pager || true
  journalctl -u stepanel.service -u stepanel-worker.service --no-pager -n 100 || true
  exit 1
fi
if ! runuser -u stepanel -- test -r "$RCLONE_CONFIG"; then
  echo 'StePanel service account cannot read the configured rclone file' >&2
  exit 1
fi
if [[ -e /etc/sudoers.d/stepanel ]]; then
  echo 'installer left a panel sudoers policy after installing the root broker service' >&2
  exit 1
fi
if ! grep -Fxq 'RCLONE_CONFIG="/etc/stepanel/rclone.conf"' /etc/ste-panel.env; then
  echo 'installer did not preserve the explicit rclone config path for systemd' >&2
  exit 1
fi
if [[ $STEPANEL_WEBSERVER == caddy ]] && ! grep -Eq 'reverse_proxy[[:space:]]+127\.0\.0\.1:8090([[:space:]]|$)' /etc/caddy/stepanel.d/panel.caddy; then
  echo 'Caddy panel proxy does not target the installed StePanel listen address' >&2
  exit 1
fi
# The recovery smoke runs the separately supervised worker, so make the
# explicitly lab-only direct-broker mode visible to both installed units even
# when the installer is exercising an environment-file transition.
if ! grep -q '^STEPANEL_LAB_DIRECT_ROOT_BROKER="1"$' /etc/ste-panel.env; then
  printf '%s\n' 'STEPANEL_LAB_DIRECT_ROOT_BROKER="1"' >> /etc/ste-panel.env
fi
# The marker is root-owned and lives only in the disposable host's /run.
# It avoids relying on systemd environment propagation for lab-only broker
# routing.
install -m 0600 -o root -g root /dev/null /run/stepanel-lab-direct-root-broker
install -m 0600 -o root -g root /dev/null /etc/stepanel-lab-direct-root-broker
# Container runtimes can disable setuid transitions even for privileged
# containers. Run the broker as a separate root service over a local socket so
# this smoke still exercises the production unprivileged panel/worker boundary.
install -m 0644 /work/deploy/lab/stepanel-root-broker.service /etc/systemd/system/stepanel-root-broker.service
systemctl daemon-reload
if ! grep -q '^STEPANEL_LAB_ROOT_BROKER_SOCKET=' /etc/ste-panel.env; then
  printf '%s\n' 'STEPANEL_LAB_ROOT_BROKER_SOCKET="/run/stepanel-root-broker.sock"' >> /etc/ste-panel.env
fi
systemctl daemon-reload
systemctl stop stepanel-root-broker.service 2>/dev/null || true
rm -f /run/stepanel-root-broker.sock
systemctl start stepanel-root-broker.service
systemctl is-active --quiet stepanel-root-broker.service
for _ in $(seq 1 60); do
  [[ -S /run/stepanel-root-broker.sock ]] && break
  sleep 0.5
done
if [[ ! -S /run/stepanel-root-broker.sock ]]; then
  systemctl status stepanel-root-broker.service --no-pager || true
  journalctl -u stepanel-root-broker.service --no-pager -n 100 || true
  echo 'root broker did not create its socket after restart' >&2
  exit 1
fi
systemctl is-active --quiet stepanel-root-broker.service
root_broker_props=$(systemctl show stepanel-root-broker.service -p NoNewPrivileges -p PrivateDevices -p PrivateTmp -p ProtectSystem -p RestrictNamespaces)
root_broker_rw_paths=$(systemctl show stepanel-root-broker.service -p ReadWritePaths --value)
grep -Fxq 'NoNewPrivileges=yes' <<<"$root_broker_props"
grep -Fxq 'PrivateDevices=yes' <<<"$root_broker_props"
grep -Fxq 'PrivateTmp=yes' <<<"$root_broker_props"
grep -Fxq 'ProtectSystem=strict' <<<"$root_broker_props"
grep -Fxq 'RestrictNamespaces=yes' <<<"$root_broker_props"
grep -Eq '(^| )/etc( |$)' <<<"$root_broker_rw_paths"
grep -Eq '^ReadWritePaths=.* /etc/\.pwd\.lock' /etc/systemd/system/stepanel-root-broker.service
systemctl restart stepanel.service stepanel-worker.service
if ! systemctl is-active --quiet stepanel.service; then
  systemctl status stepanel.service stepanel-worker.service --no-pager || true
  journalctl -u stepanel.service -u stepanel-worker.service --no-pager -n 100 || true
  exit 1
fi

wait_for_panel_health() {
  local endpoint=$1
  local attempts=${2:-120}
  for _ in $(seq 1 "$attempts"); do
    if systemctl is-active --quiet stepanel.service && \
       curl --fail --silent --max-time 2 "$endpoint" >/dev/null; then
      return 0
    fi
    sleep 1
  done
  systemctl status stepanel.service stepanel-worker.service --no-pager || true
  journalctl -u stepanel.service -u stepanel-worker.service --no-pager -n 200 || true
  return 1
}

wait_for_panel_health http://127.0.0.1:8090/livez
systemctl restart stepanel.service
wait_for_panel_health http://127.0.0.1:8090/readyz

# Exercise recovery from an unclean daemon death on the installed host. The
# container-level CI drill covers one process boundary; this also verifies the
# systemd units, durable state paths, and worker restart contract together.
for unit in stepanel.service stepanel-worker.service; do
  systemctl is-active --quiet "$unit"
  systemctl kill --kill-who=main --signal=SIGKILL "$unit"
  systemctl reset-failed "$unit" 2>/dev/null || true
  systemctl start "$unit"
done
for _ in $(seq 1 120); do
  if systemctl is-active --quiet stepanel.service && \
     systemctl is-active --quiet stepanel-worker.service && \
     curl --fail --silent --max-time 2 http://127.0.0.1:8090/livez >/dev/null && \
     curl --fail --silent --max-time 2 http://127.0.0.1:8090/readyz >/dev/null; then
    break
  fi
  sleep 1
done
wait_for_panel_health http://127.0.0.1:8090/readyz
systemctl is-active --quiet stepanel-worker.service
test -s /var/lib/ste-panel/stepanel-control.db
test -s /var/lib/ste-panel/audit.jsonl
systemd-analyze security stepanel.service stepanel-worker.service
# Keep the unprivileged services' sandbox from regressing (1.5 when hardened;
# 5.4 before capabilities, namespaces, IPC, and syscalls were restricted).
for unit in stepanel.service stepanel-worker.service; do
  exposure=$(systemd-analyze security "$unit" 2>/dev/null | awk '/Overall exposure level/ { print $(NF-2) }')
  if ! awk -v e="$exposure" 'BEGIN { exit !(e != "" && e + 0 <= 2.5) }'; then
    echo "$unit systemd exposure is ${exposure:-unknown}; expected at most 2.5" >&2
    exit 1
  fi
done
for unit in stepanel.service stepanel-worker.service; do
  systemctl show "$unit" -p ProtectSystem --value | grep -Fxq strict
  systemctl show "$unit" -p PrivateTmp --value | grep -Fxq yes
  systemctl show "$unit" -p ProtectKernelTunables --value | grep -Fxq yes
  systemctl show "$unit" -p ProtectKernelModules --value | grep -Fxq yes
  systemctl show "$unit" -p ProtectKernelLogs --value | grep -Fxq yes
  systemctl show "$unit" -p ProtectControlGroups --value | grep -Fxq yes
  systemctl show "$unit" -p ProtectClock --value | grep -Fxq yes
  systemctl show "$unit" -p RestrictRealtime --value | grep -Fxq yes
done

# Exercise the installed site helpers and selected webserver configuration,
# not just the panel daemon. This is intentionally a synthetic site.
site=ci-smoke
/usr/local/sbin/stepanel-sitectl prepare "$site"
mkdir -p "/var/www/sites/$site/public"
printf '%s\n' 'smoke' > "/var/www/sites/$site/public/index.html"
/usr/local/sbin/stepanel-sitectl seal "$site"

# prepare-root is what the typed broker runs before staged activation: the
# full isolation contract without creating public/.
root_site=ci-smoke-root
root_user=$(/usr/local/sbin/stepanel-sitectl prepare-root "$root_site" | tail -n 1)
[[ $(stat -c %U "/var/www/sites/$root_site") == "$root_user" ]]
[[ -d "/var/www/sites/$root_site/.php/sessions" && ! -e "/var/www/sites/$root_site/public" ]]
getfacl -p "/var/www/sites/$root_site" | grep -Fq 'user:stepanel:rwx'
/usr/local/sbin/stepanel-sitectl delete "$root_site"
bash /work/deploy/lab/sftp-access-smoke.sh
bash /work/deploy/lab/sitectl-acl-smoke.sh "$site"
if [[ ${STEPANEL_RUN_FTPS_SMOKE:-0} == 1 ]]; then
  bash /work/deploy/lab/ftps-access-smoke.sh
fi

/usr/local/sbin/stepanel-vhostctl apply "$site" ci-smoke.example.test
/usr/local/sbin/stepanel-appctl resource-apply "$site" 100 100 128 512 100 256
bash /work/deploy/lab/task-safeguards-smoke.sh "$site"
if [[ $STEPANEL_WEBSERVER == apache ]]; then
  apachectl -t 2>/dev/null || httpd -t
else
  caddy validate --config /etc/caddy/Caddyfile
  systemctl is-active --quiet stepanel-nosymfollow.service
  findmnt -no OPTIONS -T /var/www | tr ',' '\n' | grep -Fxq nosymfollow
  sensitive_file="/var/www/sites/$site/public/.env"
  symlink_target="/var/www/sites/$site/.stepanel-caddy-outside-secret"
  symlink_path="/var/www/sites/$site/public/stepanel-outside-secret"
  printf '%s\n' 'must-not-be-served' > "$sensitive_file"
  printf '%s\n' 'must-not-be-followed' > "$symlink_target"
  ln -s ../.stepanel-caddy-outside-secret "$symlink_path"
  trap 'rm -f "$sensitive_file" "$symlink_target" "$symlink_path"' EXIT
  caddy_status=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' --max-time 5 -H 'Host: ci-smoke.example.test' http://127.0.0.1/.env || true)
  [[ $caddy_status == 403 || $caddy_status == 404 ]] || { echo "Caddy served a sensitive file with HTTP $caddy_status" >&2; exit 1; }
  caddy_status=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' --max-time 5 -H 'Host: ci-smoke.example.test' http://127.0.0.1/stepanel-outside-secret || true)
  [[ $caddy_status == 403 || $caddy_status == 404 ]] || { echo "Caddy followed an out-of-root symlink with HTTP $caddy_status" >&2; exit 1; }
  rm -f "$sensitive_file" "$symlink_target" "$symlink_path"
  trap - EXIT
fi

# Exercise the installed HTTP upload and durable cpmove worker path. This
# creates a separate disposable site so the helper smoke remains intact.
bash /work/deploy/lab/cpmove-import-smoke.sh
bash /work/deploy/lab/cpmove-recovery-smoke.sh
bash /work/deploy/lab/dbctl-lock-smoke.sh
DATABASE_RESTORE_RECOVERY=1 bash /work/deploy/lab/backup-recovery-smoke.sh
bash /work/deploy/lab/account-suspension-recovery-smoke.sh
bash /work/deploy/lab/deploy-recovery-smoke.sh
# The deploy recovery drill above deliberately kills the panel while holding
# the ci-smoke site lease. The fenced lease remains unavailable until its
# expiry, so isolate the app lifecycle drill on its own disposable site rather
# than turning a valid post-crash contention response into a false failure.
app_smoke_site=ci-app-smoke
/usr/local/sbin/stepanel-sitectl prepare "$app_smoke_site"
mkdir -p "/var/www/sites/$app_smoke_site/public"
printf '%s\n' 'app lifecycle smoke' > "/var/www/sites/$app_smoke_site/public/index.html"
/usr/local/sbin/stepanel-sitectl seal "$app_smoke_site"
set +e
APP_LIFECYCLE_SMOKE_SITE="$app_smoke_site" bash /work/deploy/lab/app-lifecycle-smoke.sh
app_smoke_status=$?
set -e
/usr/local/sbin/stepanel-sitectl delete "$app_smoke_site"
if (( app_smoke_status != 0 )); then
  exit "$app_smoke_status"
fi
if [[ ${STEPANEL_RUN_RECOVERY_MATRIX:-0} == 1 ]]; then
  if [[ ${STEPANEL_RUN_RECOVERY_MATRIX_FULL:-0} == 1 ]]; then
    RECOVERY_MATRIX_FULL=1 bash /work/deploy/lab/recovery-matrix-smoke.sh
  else
    bash /work/deploy/lab/recovery-matrix-smoke.sh
  fi
fi
