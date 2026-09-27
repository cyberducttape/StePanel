#!/usr/bin/env bash
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'upgrade smoke must run as root' >&2; exit 1; }
previous_root=${1:?previous release tree is required}
candidate_root=${2:?candidate release tree is required}
broken_root=${3:-}

[[ -x "$previous_root/install.sh" && -x "$previous_root/stepanel" ]] || { echo "invalid previous release tree: $previous_root" >&2; exit 1; }
[[ -x "$candidate_root/install.sh" && -x "$candidate_root/stepanel" && -x "$candidate_root/stepanel-root" ]] || { echo "invalid candidate release tree: $candidate_root" >&2; exit 1; }
if [[ -n "$broken_root" && ( ! -x "$broken_root/install.sh" || ! -x "$broken_root/stepanel" ) ]]; then
  echo "invalid deliberately broken release tree: $broken_root" >&2
  exit 1
fi
if [[ ! -f /sys/fs/cgroup/cgroup.controllers && ! -d /sys/fs/cgroup/systemd ]]; then
  echo 'upgrade smoke requires a systemd-compatible cgroup hierarchy' >&2
  exit 77
fi

export STEPANEL_ENV=production
export STEPANEL_LISTEN=127.0.0.1:8090
export STEPANEL_TLS_TERMINATED=1
export STEPANEL_ADMIN_PASSWORD=ci-upgrade-only-password
export STEPANEL_ADMIN_TOTP_SECRET=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP
export STEPANEL_SESSION_SECRET=ci-upgrade-session-secret-012345678901234567890123
export STEPANEL_AUDIT_KEY=ci-upgrade-audit-key-012345678901234567890123456789
export STEPANEL_ACCOUNT_KEY=ci-upgrade-account-key-012345678901234567890123456
# Keep the systemd credential file non-empty even though local socket database
# administration does not use a database password. The service deliberately
# rejects an invalid credential file during startup.
export STEPANEL_DB_PASSWORD=ci-upgrade-db-credential
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

cd "$previous_root"
./install.sh
systemctl is-active --quiet stepanel.service
"/opt/stepanel/stepanel" version | grep -F '0.6.0'
curl --fail --silent --max-time 5 http://127.0.0.1:8090/readyz >/dev/null

# Exercise the N-1 state path before the candidate opens the durable database.
install -d -m 0750 -o stepanel -g stepanel /var/lib/ste-panel
printf '%s\n' '[]' > /var/lib/ste-panel/jobs.json
chown stepanel:stepanel /var/lib/ste-panel/jobs.json
chmod 0600 /var/lib/ste-panel/jobs.json

cd "$candidate_root"
./install.sh
systemctl is-active --quiet stepanel.service stepanel-worker.service
curl --fail --silent --max-time 5 http://127.0.0.1:8090/readyz >/dev/null
test -s /var/lib/ste-panel/stepanel-control.db

# The installed service environment is root-owned. Load it for the CLI so the
# commands examine the production paths rather than development defaults.
set -a
# shellcheck disable=SC1091
. /etc/ste-panel.env
set +a
/opt/stepanel/stepanel dr-check >/tmp/stepanel-upgrade-dr.json
/opt/stepanel/stepanel backup-control-plane /tmp/stepanel-upgrade-control.db
/opt/stepanel/stepanel restore-control-plane /tmp/stepanel-upgrade-control.db --dry-run

# Exercise the installer's transaction rollback using a release tree whose
# binary always fails its post-install health check. The previously upgraded
# candidate must remain active and ready after the failed replacement.
if [[ -n "$broken_root" ]]; then
  candidate_version=$("$candidate_root/stepanel" version | awk 'NR == 1 { print $2 }')
  if (cd "$broken_root" && ./install.sh); then
    echo 'broken candidate unexpectedly installed successfully' >&2
    exit 1
  fi
  systemctl is-active --quiet stepanel.service stepanel-worker.service
  /opt/stepanel/stepanel version | grep -Fx "StePanel $candidate_version"
  curl --fail --silent --max-time 5 http://127.0.0.1:8090/readyz >/dev/null
fi
