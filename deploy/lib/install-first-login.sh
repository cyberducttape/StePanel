#!/usr/bin/env bash

# Print concrete, stack-specific instructions after the native install has
# committed. Keep TLS setup explicit: only Caddy obtains the panel certificate
# automatically; Apache and OpenLiteSpeed require operator action first.
install_print_first_login_steps() {
  local hostname=${1:?panel hostname is required}
  local web_server=${2:?web server is required}
  local certbot_available=${3:-0}
  local web_service=${4:-}
  local database_service=${5:-}

  printf '\nNext steps to open StePanel:\n'
  printf '1. Point the A record for %s to this server. Add an AAAA record only if this host accepts public IPv6 traffic, and allow inbound ports 80 and 443.\n' "$hostname"
  case "$web_server" in
    caddy)
      printf '2. Once public DNS resolves to this server, Caddy obtains HTTPS automatically. Open https://%s.\n' "$hostname"
      ;;
    apache)
      if [[ $certbot_available == 1 ]]; then
        printf '2. Issue the panel certificate before login: sudo stepanel-certbot "%s" "you@example.com"\n' "$hostname"
      else
        printf '2. Configure trusted TLS termination for %s before login; then open https://%s.\n' "$hostname" "$hostname"
      fi
      ;;
    *)
      printf '2. Configure and verify HTTPS for %s in %s before login; then open https://%s.\n' "$hostname" "$web_server" "$hostname"
      ;;
  esac
  printf '3. Sign in at https://%s with the administrator password and authenticator code you configured.\n' "$hostname"
  printf '4. Follow the First-run checklist to resolve host health, create or migrate a website, and verify its first recovery point.\n'
  printf 'If a service is unavailable, inspect it with: systemctl --no-pager status stepanel.service stepanel-worker.service stepanel-root-broker.service'
  [[ -z $web_service ]] || printf ' %s' "$web_service"
  [[ -z $database_service ]] || printf ' %s' "$database_service"
  printf '\nKeep /etc/ste-panel.env and your installer settings backed up securely; the backup encryption key is required for recovery.\n'
}
