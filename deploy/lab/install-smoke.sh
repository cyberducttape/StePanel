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
if ! systemctl is-active --quiet stepanel.service; then
  systemctl status stepanel.service stepanel-worker.service --no-pager || true
  journalctl -u stepanel.service -u stepanel-worker.service --no-pager -n 100 || true
  exit 1
fi

wait_for_panel_health() {
  local endpoint=$1
  local attempts=${2:-30}
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
for _ in $(seq 1 30); do
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

# Exercise the installed site helpers and selected webserver configuration,
# not just the panel daemon. This is intentionally a synthetic site.
site=ci-smoke
/usr/local/sbin/stepanel-sitectl prepare "$site"
mkdir -p "/var/www/sites/$site/public"
printf '%s\n' 'smoke' > "/var/www/sites/$site/public/index.html"
/usr/local/sbin/stepanel-sitectl seal "$site"
/usr/local/sbin/stepanel-vhostctl apply "$site" ci-smoke.example.test
if [[ $STEPANEL_WEBSERVER == apache ]]; then
  apachectl -t 2>/dev/null || httpd -t
else
  caddy validate --config /etc/caddy/Caddyfile
fi

# Exercise the installed HTTP upload and durable cpmove worker path. This
# creates a separate disposable site so the helper smoke remains intact.
bash /work/deploy/lab/cpmove-import-smoke.sh
