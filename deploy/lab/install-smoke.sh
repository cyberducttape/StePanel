#!/usr/bin/env bash
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'install smoke must run as root' >&2; exit 1; }
cd /work

if [[ ! -f /sys/fs/cgroup/cgroup.controllers && ! -d /sys/fs/cgroup/systemd ]]; then
  echo 'install smoke requires a systemd-compatible cgroup hierarchy; use a KVM/Cloud VM or CI runner' >&2
  exit 77
fi

export STEPANEL_ENV=production
export STEPANEL_SKIP_QUOTA_CHECK=1
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

if ! ./install.sh; then
  systemctl status stepanel.service stepanel-worker.service --no-pager || true
  journalctl -u stepanel.service -u stepanel-worker.service --no-pager -n 100 || true
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
if ! grep -q '^STEPANEL_LAB_ROOT_BROKER_SOCKET=' /etc/ste-panel.env; then
  printf '%s\n' 'STEPANEL_LAB_ROOT_BROKER_SOCKET="/run/stepanel-root-broker.sock"' >> /etc/ste-panel.env
fi
# rclone treats `local:/path` as a configured remote named "local". Create
# that deliberately disposable remote so the required offsite-backup path is
# exercised against the host filesystem rather than silently bypassed.
install -d -m 0750 -o stepanel -g stepanel /opt/stepanel
install -m 0600 -o stepanel -g stepanel /dev/null /opt/stepanel/.rclone.conf
printf '[local]\ntype = local\n' > /opt/stepanel/.rclone.conf
chown stepanel:stepanel /opt/stepanel/.rclone.conf
chmod 0600 /opt/stepanel/.rclone.conf
systemctl daemon-reload
systemctl restart stepanel-root-broker.service
systemctl is-active --quiet stepanel-root-broker.service
test -S /run/stepanel-root-broker.sock
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
for unit in stepanel.service stepanel-worker.service; do
  systemctl show "$unit" -p ProtectSystem --value | grep -Fxq full
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
/usr/local/sbin/stepanel-vhostctl apply "$site" ci-smoke.example.test
bash /work/deploy/lab/task-safeguards-smoke.sh "$site"
if [[ $STEPANEL_WEBSERVER == apache ]]; then
  apachectl -t 2>/dev/null || httpd -t
else
  caddy validate --config /etc/caddy/Caddyfile
fi

# Exercise the installed HTTP upload and durable cpmove worker path. This
# creates a separate disposable site so the helper smoke remains intact.
bash /work/deploy/lab/cpmove-import-smoke.sh
bash /work/deploy/lab/cpmove-recovery-smoke.sh
bash /work/deploy/lab/dbctl-lock-smoke.sh
bash /work/deploy/lab/backup-recovery-smoke.sh
bash /work/deploy/lab/account-suspension-recovery-smoke.sh
bash /work/deploy/lab/deploy-recovery-smoke.sh
if [[ ${STEPANEL_RUN_RECOVERY_MATRIX:-0} == 1 ]]; then
  bash /work/deploy/lab/recovery-matrix-smoke.sh
fi
