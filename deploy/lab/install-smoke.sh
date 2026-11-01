#!/usr/bin/env bash
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'install smoke must run as root' >&2; exit 1; }
cd /work
. /work/deploy/integrations/stepanel-quota-state.sh
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
  mount_options=$(findmnt -no OPTIONS -T /var/www)
  if [[ ! $mount_options =~ (^|,)(usrquota|uquota)(,|$) ]]; then
    echo '/var/www is not mounted with user quotas enabled' >&2
    exit 1
  fi
  if [[ ! $mount_options =~ (^|,)(prjquota|pquota)(,|$) ]]; then
    echo '/var/www is not mounted with project quotas enabled' >&2
    exit 1
  fi
  stepanel_project_quota_active /var/www
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
export STEPANEL_INSTALL_FTP=1
export STEPANEL_ACTIVATE_FTP=0
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

install_args=(--unsafe-lab)
if [[ ${STEPANEL_INSTALLER_PREEXISTING_APACHE_SMOKE:-0} == 1 ]]; then
  [[ $STEPANEL_WEBSERVER == caddy && -x /usr/bin/apt-get ]] || {
    echo 'pre-existing Apache takeover probe is defined for the Ubuntu/Caddy host only' >&2
    exit 1
  }
  export DEBIAN_FRONTEND=noninteractive
  apt-get install -y apache2 dovecot-core
  legacy_vhost=/etc/apache2/sites-available/legacy-customer.conf
  legacy_root=/srv/legacy-customer/public
  install -d -m 0755 "$legacy_root"
  printf '%s\n' 'legacy-customer-content-preserved' > "$legacy_root/index.html"
  cat > "$legacy_vhost" <<EOF
<VirtualHost *:80>
  ServerName legacy-customer.example.test
  DocumentRoot $legacy_root
  <Directory $legacy_root>
    Require all granted
  </Directory>
</VirtualHost>
EOF
  a2ensite legacy-customer >/dev/null
  apachectl -t
  systemctl enable --now apache2
  legacy_vhost_sha=$(sha256sum "$legacy_vhost" | cut -d' ' -f1)

  # Model an existing, intentionally inactive partial mail stack. Installing
  # Exim and SpamAssassin later must not activate Dovecot or replace its
  # operator configuration just because package scripts ran.
  legacy_mail_config=/etc/dovecot/conf.d/99-legacy-operator.conf
  printf '%s\n' '# legacy operator configuration; preserve byte-for-byte' > "$legacy_mail_config"
  systemctl disable --now dovecot 2>/dev/null || true
  legacy_mail_sha=$(sha256sum "$legacy_mail_config" | cut -d' ' -f1)

  # A pre-existing customer service must remain untouched until the operator
  # explicitly consents to takeover. This attempt must stop at preflight.
  if ./install.sh --unsafe-lab > /tmp/stepanel-no-consent.log 2>&1; then
    echo 'installer accepted active pre-existing Apache without takeover consent' >&2
    exit 1
  fi
  grep -Fq 'rerun with --take-over-host to stop and disable: apache2' /tmp/stepanel-no-consent.log || {
    cat /tmp/stepanel-no-consent.log >&2
    echo 'installer refusal did not explain the required takeover consent' >&2
    exit 1
  }
  systemctl is-active --quiet apache2
  systemctl is-enabled --quiet apache2
  [[ $(sha256sum "$legacy_vhost" | cut -d' ' -f1) == "$legacy_vhost_sha" ]]
  [[ $(<"$legacy_root/index.html") == legacy-customer-content-preserved ]]
  systemctl is-active --quiet dovecot && { echo 'installer activated a previously inactive partial mail service' >&2; exit 1; }
  systemctl is-enabled --quiet dovecot && { echo 'installer enabled a previously disabled partial mail service' >&2; exit 1; }
  [[ $(sha256sum "$legacy_mail_config" | cut -d' ' -f1) == "$legacy_mail_sha" ]]
  install_args+=(--take-over-host)
  export STEPANEL_INSTALL_MAIL=1
  export STEPANEL_ACTIVATE_MAIL=0
fi

if ! ./install.sh "${install_args[@]}"; then
  systemctl status stepanel.service stepanel-worker.service --no-pager || true
  journalctl -u stepanel.service -u stepanel-worker.service --no-pager -n 100 || true
  exit 1
fi
if [[ ${STEPANEL_INSTALLER_PREEXISTING_APACHE_SMOKE:-0} == 1 ]]; then
  if systemctl is-active --quiet apache2 || systemctl is-enabled --quiet apache2; then
    echo 'consented takeover did not stop and disable pre-existing Apache' >&2
    exit 1
  fi
  [[ $(sha256sum /etc/apache2/sites-available/legacy-customer.conf | cut -d' ' -f1) == "$legacy_vhost_sha" ]]
  [[ $(< /srv/legacy-customer/public/index.html) == legacy-customer-content-preserved ]]
  systemctl is-active --quiet dovecot && { echo 'partial mail service activation state changed during installation' >&2; exit 1; }
  systemctl is-enabled --quiet dovecot && { echo 'partial mail service enablement changed during installation' >&2; exit 1; }
  [[ $(sha256sum /etc/dovecot/conf.d/99-legacy-operator.conf | cut -d' ' -f1) == "$legacy_mail_sha" ]]
  takeover_journal=$(grep -l 'taking over pre-existing web service apache2' /var/lib/stepanel/install-journal/*.tsv | tail -n 1)
  [[ -n $takeover_journal ]]
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
if [[ $STEPANEL_WEBSERVER == caddy ]]; then
  caddy_local_ca=/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt
  if [[ ! -f $caddy_local_ca || -L $caddy_local_ca ]]; then
    echo 'Caddy local root CA is missing or is a symlink' >&2
    exit 1
  fi
  if command -v update-ca-certificates >/dev/null 2>&1; then
    system_ca_bundle=/etc/ssl/certs/ca-certificates.crt
  else
    system_ca_bundle=/etc/pki/tls/certs/ca-bundle.crt
  fi
  if ! openssl verify -CAfile "$system_ca_bundle" "$caddy_local_ca"; then
    echo 'Caddy local root CA is not trusted by the operating-system CA bundle' >&2
    exit 1
  fi
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
root_broker_props=$(systemctl show stepanel-root-broker.service -p NoNewPrivileges -p DevicePolicy -p PrivateTmp -p ProtectSystem -p RestrictNamespaces)
root_broker_rw_paths=$(systemctl show stepanel-root-broker.service -p ReadWritePaths --value)
root_broker_caps=$(systemctl show stepanel-root-broker.service -p CapabilityBoundingSet --value)
grep -Fxq 'NoNewPrivileges=yes' <<<"$root_broker_props"
# The broker sees host device nodes for quota tools but cannot open disks.
grep -Fxq 'DevicePolicy=closed' <<<"$root_broker_props"
if grep -Eq '(^| )cap_(mknod|sys_rawio)( |$)' <<<"$root_broker_caps"; then echo "root broker keeps raw device capabilities: $root_broker_caps" >&2; exit 1; fi
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
test "$(stat -c '%U:%a' /var/lib/ste-panel/stepanel-control.db)" = stepanel:600
test -s /var/lib/ste-panel/audit.jsonl
bash /work/deploy/lab/control-plane-db-outage-smoke.sh
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
site_user=$(stat -c %U "/var/www/sites/$site")

# This fixture is created directly through the host helper after the panel's
# startup identity migration. Restart once so the legacy site account is
# durably registered before later Git deployment tests use the broker's
# persisted-identity contract.
systemctl restart stepanel.service
for _ in $(seq 1 60); do
  if systemctl is-active --quiet stepanel.service && \
     curl --fail --silent --max-time 2 http://127.0.0.1:8090/readyz >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail --silent --show-error --max-time 10 http://127.0.0.1:8090/readyz >/dev/null

if [[ ${STEPANEL_QUOTA_SMOKE:-0} == 1 ]]; then
  stage_parent=/var/www/sites/.stepanel-manager-staging
  install -d -o stepanel -g stepanel -m 0700 "$stage_parent"
  chmod 0711 "$stage_parent"
  quota_stage="$stage_parent/.stepanel-release-$site-424242"
  install -d -o stepanel -g stepanel -m 0700 "$quota_stage"
  web_group=$(sed -n 's/^STEPANEL_WEB_GROUP="\([a-z_][a-z0-9_-]*\)"$/\1/p' /etc/ste-panel.env)
  web_gid=$(getent group "$web_group" | cut -d: -f3)
  project_id=$(/usr/local/sbin/stepanel-gitctl stage-limit "$site" "$site_user" "$quota_stage" 1048576)
  [[ $project_id =~ ^1[0-9]{9}$ ]] || { echo 'Git stage quota helper returned a nonnumeric project ID' >&2; exit 1; }
  runuser -u stepanel -- touch "$quota_stage/panel-acl-test"
  runuser -u "$site_user" -- dd if=/dev/zero of="$quota_stage/first.bin" bs=700K count=1 conv=fsync status=none
  setpriv --reuid=65534 --regid=65534 --groups="$web_gid" -- test -r "$quota_stage/first.bin"
  if runuser -u "$site_user" -- dd if=/dev/zero of="$quota_stage/second.bin" bs=700K count=1 conv=fsync status=none; then
    echo 'aggregate Git project quota did not reject two individually sub-limit files' >&2
    exit 1
  fi
  /usr/local/sbin/stepanel-gitctl stage-clear "$site" "$site_user" "$quota_stage" "$project_id"
  rm -rf -- "$quota_stage"

  # Private clone through the production helper against a local SSH Git
  # server: the clone must run as the unprivileged stepanel-git identity and
  # leave the tree owned by the site with no clone process or key copy left.
  id stepanel-git >/dev/null 2>&1 || { echo 'installer did not create the stepanel-git clone identity' >&2; exit 1; }
  if ! command -v sshd >/dev/null 2>&1 && [[ ! -x /usr/sbin/sshd ]]; then
    if command -v dnf >/dev/null 2>&1; then dnf install -y openssh-server git >/dev/null; else apt-get install -y openssh-server git >/dev/null; fi
  fi
  systemctl start sshd 2>/dev/null || systemctl start ssh
  created_git_user=0
  if ! id git >/dev/null 2>&1; then useradd --create-home --shell /usr/bin/git-shell git 2>/dev/null || useradd --create-home --shell /bin/sh git; created_git_user=1; fi
  git_home=$(getent passwd git | cut -d: -f6)
  known_hosts=/etc/stepanel/git-known-hosts
  known_hosts_backup=
  [[ -e $known_hosts ]] && { known_hosts_backup=$(mktemp); cp -p "$known_hosts" "$known_hosts_backup"; }
  clone_smoke_cleanup() {
    /usr/local/sbin/stepanel-gitctl delete "$site" >/dev/null 2>&1 || true
    if [[ -n $known_hosts_backup ]]; then mv -f "$known_hosts_backup" "$known_hosts"; else rm -f "$known_hosts"; fi
    rm -rf -- "$git_home/smoke.git" "$git_home/.ssh/authorized_keys"
    (( created_git_user )) && userdel -r git 2>/dev/null || true
  }
  /usr/local/sbin/stepanel-gitctl delete "$site" >/dev/null 2>&1 || true
  deploy_public=$(/usr/local/sbin/stepanel-gitctl generate "$site")
  install -d -m 0700 -o git -g git "$git_home/.ssh"
  printf '%s\n' "$deploy_public" > "$git_home/.ssh/authorized_keys"
  chown git:git "$git_home/.ssh/authorized_keys"; chmod 0600 "$git_home/.ssh/authorized_keys"
  work_repo=$(mktemp -d)
  git -C "$work_repo" init -q -b main
  printf 'cloned by stepanel-git\n' > "$work_repo/README"
  git -C "$work_repo" -c user.name=smoke -c user.email=smoke@localhost add README
  git -C "$work_repo" -c user.name=smoke -c user.email=smoke@localhost commit -q -m smoke
  git clone -q --bare "$work_repo" "$git_home/smoke.git"
  chown -R git:git "$git_home/smoke.git"
  rm -rf -- "$work_repo"
  install -d -m 0755 /etc/stepanel
  ssh-keyscan -T 10 localhost 2>/dev/null > "$known_hosts"
  [[ -s $known_hosts ]] || { clone_smoke_cleanup; echo 'could not read the local SSH host keys' >&2; exit 1; }
  chmod 0644 "$known_hosts"
  clone_stage="$stage_parent/.stepanel-release-$site-434343"
  install -d -o stepanel -g stepanel -m 0700 "$clone_stage"
  clone_project=$(/usr/local/sbin/stepanel-gitctl stage-limit "$site" "$site_user" "$clone_stage" 1048576)
  if ! STEPANEL_GIT_ALLOWED_HOSTS=localhost /usr/local/sbin/stepanel-gitctl clone "$site" git@localhost:smoke.git main "$clone_stage" localhost 1048576 "$clone_project"; then
    clone_smoke_cleanup
    echo 'private Git clone through stepanel-gitctl failed' >&2
    exit 1
  fi
  clone_failures=()
  grep -qx 'cloned by stepanel-git' "$clone_stage/README" || clone_failures+=('cloned content is missing')
  [[ $(stat -c '%U' "$clone_stage/README") != root && $(stat -c '%U' "$clone_stage/README") != stepanel-git ]] || clone_failures+=("clone left files owned by $(stat -c '%U' "$clone_stage/README")")
  ! pgrep -u stepanel-git >/dev/null || clone_failures+=('a stepanel-git process outlived the clone')
  [[ -z $(find /run -maxdepth 1 -name 'stepanel-git-clone.*' -print -quit) ]] || clone_failures+=('the per-clone key copy was not removed')
  /usr/local/sbin/stepanel-gitctl stage-clear "$site" "$site_user" "$clone_stage" "$clone_project"
  rm -rf -- "$clone_stage"

  # A clone whose processes cannot be reaped must fail closed. A process in
  # uninterruptible I/O cannot be produced on demand, so a stub ps reports a
  # live clone process from its second call on: the clone's own reaping and
  # its exit cleanup both fail, and the stage must stay root-owned and
  # quarantined until clone-recover sees no clone process.
  stuck_stage="$stage_parent/.stepanel-release-$site-545454"
  install -d -o stepanel -g stepanel -m 0700 "$stuck_stage"
  stuck_project=$(/usr/local/sbin/stepanel-gitctl stage-limit "$site" "$site_user" "$stuck_stage" 1048576)
  stuck_owner=$(stat -c '%u:%g:%a' "$stuck_stage")
  fake_ps_dir=$(mktemp -d)
  printf '%s\n' '#!/bin/sh' 'n=$(( $(cat "$STEPANEL_SMOKE_PS_COUNT" 2>/dev/null || echo 0) + 1 ))' 'echo "$n" > "$STEPANEL_SMOKE_PS_COUNT"' '[ "$n" -ge "$STEPANEL_SMOKE_PS_ALIVE_FROM" ] && echo S' 'exit 0' > "$fake_ps_dir/ps"
  chmod 0755 "$fake_ps_dir/ps"
  if PATH="$fake_ps_dir:$PATH" STEPANEL_SMOKE_PS_COUNT="$fake_ps_dir/count" STEPANEL_SMOKE_PS_ALIVE_FROM=2 STEPANEL_GIT_ALLOWED_HOSTS=localhost \
    /usr/local/sbin/stepanel-gitctl clone "$site" git@localhost:smoke.git main "$stuck_stage" localhost 1048576 "$stuck_project" 2>"$fake_ps_dir/stderr"; then
    clone_failures+=('a clone with unreaped processes reported success')
  fi
  grep -q 'staging tree is quarantined' "$fake_ps_dir/stderr" || clone_failures+=("an unreaped clone was not quarantined: $(tr '\n' ' ' < "$fake_ps_dir/stderr")")
  # Quarantined: root-owned with no group or other access (chmod keeps a
  # directory's setgid bit, so the mode may read 2700).
  stage_quarantined() { [[ $(stat -c '%U' "$1") == root ]] && (( (8#$(stat -c '%a' "$1") & 8#077) == 0 )); }
  stage_quarantined "$stuck_stage" || clone_failures+=("a quarantined stage was handed back as $(stat -c '%U:%a' "$stuck_stage")")
  compgen -G '/var/lib/stepanel/git-clone-quarantine/clone.*' >/dev/null || clone_failures+=('no quarantine record was written')
  # Recovery refuses while a clone process is still reported alive.
  rm -f "$fake_ps_dir/count"
  if PATH="$fake_ps_dir:$PATH" STEPANEL_SMOKE_PS_COUNT="$fake_ps_dir/count" STEPANEL_SMOKE_PS_ALIVE_FROM=1 /usr/local/sbin/stepanel-gitctl clone-recover 2>/dev/null; then
    clone_failures+=('clone-recover reclaimed a stage while a clone process was alive')
  fi
  stage_quarantined "$stuck_stage" || clone_failures+=('a refused recovery released the stage')
  # With no clone process left, recovery empties the stage and returns it.
  /usr/local/sbin/stepanel-gitctl clone-recover >/dev/null || clone_failures+=('clone-recover failed with no clone process alive')
  [[ $(stat -c '%u:%g:%a' "$stuck_stage") == "$stuck_owner" ]] || clone_failures+=("recovered stage is $(stat -c '%u:%g:%a' "$stuck_stage"), want $stuck_owner")
  [[ -z $(find "$stuck_stage" -mindepth 1 -print -quit) ]] || clone_failures+=('a recovered stage kept the failed clone files')
  ! compgen -G '/var/lib/stepanel/git-clone-quarantine/clone.*' >/dev/null || clone_failures+=('the quarantine record outlived recovery')
  [[ -z $(find /run -maxdepth 1 -name 'stepanel-git-clone.*' -print -quit) ]] || clone_failures+=('the quarantined key copy outlived recovery')
  rm -rf -- "$fake_ps_dir"
  /usr/local/sbin/stepanel-gitctl stage-clear "$site" "$site_user" "$stuck_stage" "$stuck_project"
  rm -rf -- "$stuck_stage"
  clone_smoke_cleanup
  (( ${#clone_failures[@]} == 0 )) || { printf 'Git clone smoke: %s\n' "${clone_failures[@]}" >&2; exit 1; }
  echo 'Private Git clone ran as stepanel-git and handed the tree to the site; an unreaped clone was quarantined and recovered'
fi

# prepare-root is what the typed broker runs before staged activation: the
# full isolation contract without creating public/.
root_site=ci-smoke-root
root_user=$(/usr/local/sbin/stepanel-sitectl prepare-root "$root_site" | tail -n 1)
[[ $(stat -c %U "/var/www/sites/$root_site") == "$root_user" ]]
[[ -d "/var/www/sites/$root_site/.php/sessions" && ! -e "/var/www/sites/$root_site/public" ]]
getfacl -p "/var/www/sites/$root_site" | grep -Fq 'user:stepanel:rwx'
/usr/local/sbin/stepanel-sitectl delete "$root_site"
bash /work/deploy/lab/sftp-access-smoke.sh
bash /work/deploy/lab/filectl-smoke.sh
bash /work/deploy/lab/sitectl-acl-smoke.sh "$site"
bash /work/deploy/lab/ftps-access-smoke.sh

apply_smoke_vhost() {
  local target_site=$1 target_domain=$2
  if [[ $STEPANEL_WEBSERVER == caddy ]]; then
    # Use the production Unix-socket RPC. Directly running the helper as root
    # skips the broker's mount namespace and cannot validate host mount
    # publication through systemd mount units.
    runuser -u stepanel -- python3 - "$target_site" "$target_domain" <<'PY'
import json, socket, sys
import os, sqlite3, time
# The broker admits a mutation only under a lease on its target: lease the
# site itself, as the panel does.
resource_key = sys.argv[1]
owner_id = "install-smoke:" + str(os.getpid())
now = time.time_ns()
database = sqlite3.connect("/var/lib/ste-panel/stepanel-control.db")
database.execute("DELETE FROM resource_locks WHERE resource_key=? AND lease_until<?", (resource_key, now))
database.execute(
    "INSERT INTO resource_locks(resource_key, owner_id, lease_until, generation, acquired_at) VALUES(?,?,?,?,?)",
    (resource_key, owner_id, now + 300_000_000_000, 1, now),
)
database.commit()
request = {
    "type": "vhost",
    "vhost": {"action": "apply", "site": sys.argv[1], "domain": sys.argv[2], "webserver": "caddy"},
    "fencing": [{"resource_key": resource_key, "owner_id": owner_id, "generation": 1}],
}
try:
    connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    connection.settimeout(120)
    connection.connect("/run/stepanel-root-broker.sock")
    connection.sendall(json.dumps(request, separators=(",", ":")).encode() + b"\n")
    response = json.loads(connection.makefile("rb").readline())
    if not response.get("ok"):
        raise SystemExit("root broker vhost apply failed: " + str(response.get("error", "unknown error")))
finally:
    database.execute("DELETE FROM resource_locks WHERE resource_key=? AND owner_id=?", (resource_key, owner_id))
    database.commit()
    database.close()
PY
  else
    /usr/local/sbin/stepanel-vhostctl apply "$target_site" "$target_domain"
  fi
}

apply_smoke_vhost "$site" ci-smoke.localhost
/usr/local/sbin/stepanel-appctl resource-apply "$site" 100 100 128 512 100 256
socket_root=$(sed -n 's/^STEPANEL_PHP_SOCKET_ROOT="\([^"]*\)"$/\1/p' /etc/ste-panel.env)
[[ $socket_root == /run/php || $socket_root == /run/php-fpm ]] || { echo "unexpected PHP socket root: $socket_root" >&2; exit 1; }
for fpm_unit in $(systemctl list-unit-files --type=service --no-legend 'php*-fpm.service' 'php-fpm.service' | awk '{print $1}'); do
  systemctl cat "$fpm_unit" | grep -Fq "/usr/local/sbin/stepanel-ensure-php-socket-root $socket_root" || {
    echo "PHP-FPM unit $fpm_unit has no socket-root startup guard" >&2
    exit 1
  }
  systemctl restart "$fpm_unit"
done
[[ $(stat -c '%u:%g:%a' "$socket_root") == 0:0:755 ]] || { echo "PHP socket root is not root:root 0755 after FPM restart: $socket_root" >&2; exit 1; }
runuser -u stepanel -- python3 - "$site" <<'PY'
import json, socket, sys
request = {"type": "site", "site": {"action": "fpm-status", "site": sys.argv[1]}}
connection = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
connection.settimeout(5)
connection.connect("/run/stepanel-root-broker.sock")
connection.sendall(json.dumps(request, separators=(",", ":")).encode() + b"\n")
response = json.loads(connection.makefile("rb").readline())
if not response.get("ok"):
    raise SystemExit("PHP-FPM status broker request failed: " + str(response.get("error", "unknown error")))
status = response.get("details", {}).get("fpm_status", {})
if not status.get("available") or not status.get("pools"):
    raise SystemExit("installed managed PHP-FPM pool did not return live status: " + json.dumps(status))
if any(pool.get("slow_request_threshold") != "5s" for pool in status["pools"] if pool.get("state") == "available"):
    raise SystemExit("installed PHP-FPM status does not expose the configured five-second slow-request threshold: " + json.dumps(status))
print("Installed PHP-FPM status probe passed: " + ", ".join(pool.get("php_version", "default") for pool in status["pools"] if pool.get("state") == "available"))
PY
public_status=000
for _ in {1..20}; do
  if [[ $STEPANEL_WEBSERVER == caddy ]]; then
    public_status=$(curl --insecure --silent --output /dev/null --write-out '%{http_code}' --connect-timeout 1 --max-time 2 \
      --resolve ci-smoke.localhost:443:127.0.0.1 https://ci-smoke.localhost/stepanel-fpm-status 2>/dev/null || true)
  else
    public_status=$(curl --silent --output /dev/null --write-out '%{http_code}' --connect-timeout 1 --max-time 2 \
      --resolve ci-smoke.localhost:80:127.0.0.1 http://ci-smoke.localhost/stepanel-fpm-status 2>/dev/null || true)
  fi
  [[ $public_status != 000 ]] && break
  sleep 1
done
[[ $public_status == 403 || $public_status == 404 ]] || {
  if [[ $STEPANEL_WEBSERVER == apache ]]; then
    web_unit=apache2.service
    systemctl cat "$web_unit" >/dev/null 2>&1 || web_unit=httpd.service
  else
    web_unit=caddy.service
  fi
  systemctl status "$web_unit" --no-pager >&2 || true
  journalctl -u "$web_unit" --no-pager -n 80 >&2 || true
  echo "public PHP-FPM status URL returned HTTP $public_status; expected 403 or 404" >&2
  exit 1
}
bash /work/deploy/lab/task-safeguards-smoke.sh "$site"
if [[ $STEPANEL_WEBSERVER == apache ]]; then
  apachectl -t 2>/dev/null || httpd -t
else
  caddy validate --config /etc/caddy/Caddyfile
  public_root="/var/www/sites/$site/public"
  site_home="/var/www/sites/$site"
  site_directory=public
  activation_old_root="$site_home/.stepanel-public-activation-old"
  activation_marker="$public_root/stepanel-activation-marker.txt"
  sensitive_file="$public_root/.env"
  nested_env="$public_root/nested/.env.production"
  nested_git_config="$public_root/nested/.git/config"
  database_export="$public_root/database/export.sql"
  archived_backup="$public_root/archives/backup.tar.gz"
  symlink_target="/var/www/sites/$site/.stepanel-caddy-outside-secret"
  symlink_path="$public_root/stepanel-outside-secret"
  mkdir -p "$public_root/nested/.git" "$public_root/database" "$public_root/archives"
  printf '%s\n' 'must-not-be-served' > "$sensitive_file"
  printf '%s\n' 'nested-secret' > "$nested_env"
  printf '%s\n' 'git-secret' > "$nested_git_config"
  printf '%s\n' 'database-secret' > "$database_export"
  printf '%s\n' 'backup-secret' > "$archived_backup"
  printf '%s\n' 'must-not-be-followed' > "$symlink_target"
  runuser -u "$site_user" -- ln -s ../.stepanel-caddy-outside-secret "$symlink_path"
  trap 'rm -f "$sensitive_file" "$nested_env" "$nested_git_config" "$database_export" "$archived_backup" "$symlink_target" "$symlink_path"; if [[ -d "$activation_old_root" ]]; then rm -rf "$public_root"; mv "$activation_old_root" "$public_root"; fi; rm -f "$activation_marker"' EXIT
  caddy_https_status() {
    local request_path=$1 status=000
    for _ in {1..15}; do
      status=$(curl --insecure --silent --show-error --output /dev/null --write-out '%{http_code}' --max-time 5 \
        --resolve ci-smoke.localhost:443:127.0.0.1 "https://ci-smoke.localhost$request_path" 2>/dev/null || true)
      [[ $status == 000 ]] || { printf '%s' "$status"; return; }
      sleep 1
    done
    printf '%s' "$status"
  }
  for sensitive_path in /.env /nested/.env.production /nested/.git/config /database/export.sql /archives/backup.tar.gz; do
    caddy_status=$(caddy_https_status "$sensitive_path")
    [[ $caddy_status == 404 ]] || { echo "Caddy did not deny HTTPS access to $sensitive_path (HTTP $caddy_status)" >&2; exit 1; }
  done
  view_root="/var/www/.stepanel-caddy-views/$site"
  findmnt -no OPTIONS -T "$view_root" | tr ',' '\n' | grep -Fxq nosymfollow || { echo 'Caddy document root is not mounted with nosymfollow' >&2; exit 1; }
  caddy_activation_diagnostics() {
    local caddy_pid route_file
    route_file="/etc/caddy/stepanel.d/site-${site}-ci-smoke_localhost.caddy"
    echo 'Caddy activation probe diagnostics:' >&2
    [[ ! -f $route_file ]] || sed -n '1,100p' "$route_file" >&2
    stat -Lc '%A %u:%g %d:%i %n' "$site_home" "$public_root" "$activation_marker" >&2 || true
    namei -l "$view_root/$site_directory/stepanel-activation-marker.txt" >&2 || true
    findmnt -T "$view_root" -o TARGET,SOURCE,FSTYPE,OPTIONS >&2 || true
    id caddy >&2 || true
    runuser -u caddy -- test -r "$view_root/$site_directory/stepanel-activation-marker.txt" \
      && echo 'Caddy account can read the host-namespace view path' >&2 \
      || echo 'Caddy account cannot read the host-namespace view path' >&2
    systemctl show caddy.service -p MainPID -p PrivateMounts -p ProtectSystem -p ReadOnlyPaths -p InaccessiblePaths >&2 || true
    caddy_pid=$(systemctl show caddy.service -p MainPID --value 2>/dev/null || true)
    if [[ $caddy_pid =~ ^[1-9][0-9]*$ ]] && command -v nsenter >/dev/null 2>&1; then
      nsenter --target "$caddy_pid" --mount -- stat -Lc '%A %u:%g %d:%i %n' \
        "$view_root" "$view_root/$site_directory" \
        "$view_root/$site_directory/stepanel-activation-marker.txt" >&2 || true
      nsenter --target "$caddy_pid" --mount -- findmnt -T "$view_root" \
        -o TARGET,SOURCE,FSTYPE,OPTIONS >&2 || true
    fi
    journalctl -u caddy.service --no-pager -n 50 >&2 || true
  }
  site_home_identity=$(stat -c '%u:%g:%a' "$site_home")
  rm -f "$symlink_path"
  apply_smoke_vhost "$site" ci-smoke.localhost
  [[ $(stat -c '%u:%g:%a' "$site_home") == "$site_home_identity" ]] || { echo 'reapplying the Caddy route changed site-parent ownership or mode' >&2; exit 1; }
  runuser -u "$site_user" -- ln -s ../.stepanel-caddy-outside-secret "$symlink_path"
  printf '%s' before-activation > "$activation_marker"
  activation_response=$(curl --insecure --silent --show-error --max-time 10 --write-out $'\n%{http_code}' \
    --resolve ci-smoke.localhost:443:127.0.0.1 https://ci-smoke.localhost/stepanel-activation-marker.txt)
  activation_status=${activation_response##*$'\n'}
  activation_body=${activation_response%$'\n'*}
  [[ $activation_status == 200 && $activation_body == before-activation ]] || {
    echo "Caddy initial activation fixture returned HTTP $activation_status with body: $activation_body" >&2
    caddy_activation_diagnostics
    exit 1
  }
  [[ ! -e $activation_old_root && ! -L $activation_old_root ]] || { echo 'stale Caddy activation fixture exists' >&2; exit 1; }
  activation_uid=$(stat -c %u "$public_root")
  activation_gid=$(stat -c %g "$public_root")
  activation_mode=$(stat -c %a "$public_root")
  mv "$public_root" "$activation_old_root"
  install -d -m "$activation_mode" -o "$activation_uid" -g "$activation_gid" "$public_root"
  printf '%s' after-activation > "$activation_marker"
  activation_response=$(curl --insecure --silent --show-error --max-time 10 --write-out $'\n%{http_code}' \
    --resolve ci-smoke.localhost:443:127.0.0.1 https://ci-smoke.localhost/stepanel-activation-marker.txt)
  activation_status=${activation_response##*$'\n'}
  activation_body=${activation_response%$'\n'*}
  [[ $activation_status == 200 && $activation_body == after-activation ]] || {
    echo "Caddy post-activation fixture returned HTTP $activation_status with body: $activation_body" >&2
    caddy_activation_diagnostics
    exit 1
  }
  rm -rf "$public_root"
  mv "$activation_old_root" "$public_root"
  rm -f "$activation_marker"
  symlink_response=$(curl --insecure --silent --show-error --max-time 10 --write-out $'\n%{http_code}' \
    --resolve ci-smoke.localhost:443:127.0.0.1 https://ci-smoke.localhost/stepanel-outside-secret)
  symlink_status=${symlink_response##*$'\n'}
  symlink_body=${symlink_response%$'\n'*}
  [[ $symlink_status =~ ^[45][0-9][0-9]$ && $symlink_body != *must-not-be-followed* ]] || {
    echo "Caddy served or did not safely reject a document-root symlink over HTTPS (HTTP $symlink_status)" >&2
    exit 1
  }
  if apply_smoke_vhost "$site" ci-smoke.localhost 2>/dev/null; then
    echo 'Caddy vhost helper published a document root containing a symlink' >&2
    exit 1
  fi
  rm -f "$sensitive_file" "$nested_env" "$nested_git_config" "$database_export" "$archived_backup" "$symlink_target" "$symlink_path"
  trap - EXIT
fi

# Exercise the installed HTTP upload and durable cpmove worker path. This
# creates a separate disposable site so the helper smoke remains intact.
CPMOVE_SMOKE_SITE=ci-import bash /work/deploy/lab/cpmove-import-smoke.sh
bash /work/deploy/lab/dbctl-client-command-smoke.sh
# Re-import into the same tenant to prove that existing live trees are moved
# into recovery storage through the privileged broker, not by the worker.
# The disposable administrator's TOTP code is single-use; wait for the next
# counter before the second independent smoke login.
sleep $((31 - $(date +%s) % 30))
CPMOVE_SMOKE_SITE=ci-import bash /work/deploy/lab/cpmove-import-smoke.sh
bash /work/deploy/lab/cpmove-recovery-smoke.sh
bash /work/deploy/lab/dbctl-lock-smoke.sh
DATABASE_RESTORE_RECOVERY=1 bash /work/deploy/lab/backup-recovery-smoke.sh
if [[ $STEPANEL_DB_ENGINE == mysql || $STEPANEL_DB_ENGINE == mariadb ]] && \
   [[ $STEPANEL_WEBSERVER == caddy || $STEPANEL_WEBSERVER == apache ]]; then
  bash /work/deploy/lab/wordpress-recovery-proof-smoke.sh
else
  echo "installed WordPress application proof skipped for unsupported lab pair: $STEPANEL_WEBSERVER + $STEPANEL_DB_ENGINE"
fi
bash /work/deploy/lab/account-suspension-recovery-smoke.sh
bash /work/deploy/lab/deploy-recovery-smoke.sh
# The deploy recovery drill above deliberately kills the panel while holding
# the ci-smoke site lease. The fenced lease remains unavailable until its
# expiry, so isolate the app lifecycle drill on its own disposable site rather
# than turning a valid post-crash contention response into a false failure.
app_smoke_site=ci-app-smoke
set +e
APP_LIFECYCLE_SMOKE_SITE="$app_smoke_site" bash /work/deploy/lab/app-lifecycle-smoke.sh
app_smoke_status=$?
set -e
if (( app_smoke_status != 0 )); then
  exit "$app_smoke_status"
fi
# A customer's site lives in the customer's hosting account under the plan's
# account quota, which needs user quotas on /home (only the quota smoke VM
# has them; elsewhere plan enforcement suspends the account, by design).
if [[ ${STEPANEL_QUOTA_SMOKE:-0} == 1 ]]; then
  bash /work/deploy/lab/customer-site-smoke.sh
fi
if [[ ${STEPANEL_RUN_RECOVERY_MATRIX:-0} == 1 ]]; then
  if [[ ${STEPANEL_RUN_RECOVERY_MATRIX_FULL:-0} == 1 ]]; then
    RECOVERY_MATRIX_FULL=1 bash /work/deploy/lab/recovery-matrix-smoke.sh
  else
    bash /work/deploy/lab/recovery-matrix-smoke.sh
  fi
fi
