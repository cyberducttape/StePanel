#!/usr/bin/env bash
# Tenant egress policy for units that run tenant code (site PHP-FPM, Node and
# Python apps, scheduled tasks). Tenant code may reach the public internet and
# loopback (local databases and caches), but not private or link-local
# networks: cloud instance metadata, other hosts on the LAN or VPC, or internal
# services. systemd applies IPAddressAllow before IPAddressDeny, so the allow
# list carries the exceptions:
#   - localhost (127.0.0.0/8, ::1);
#   - the host's nameservers from resolv.conf, which are often private (a VPC
#     resolver or LAN router) and without which every site loses DNS;
#   - STEPANEL_TENANT_EGRESS_ALLOW: comma- or space-separated private
#     addresses or CIDR ranges an operator permits, such as a remote database.

# Private, shared and link-local ranges denied to tenant code. 169.254.0.0/16
# covers every cloud metadata endpoint except Alibaba's, listed explicitly.
TENANT_EGRESS_DENY="10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 169.254.0.0/16 100.100.100.200/32 fc00::/7 fe80::/10"

# install_tenant_egress_address_valid ADDRESS succeeds for an IPv4 or IPv6
# address with an optional prefix length.
install_tenant_egress_address_valid() {
  local value=$1 address prefix octet
  address=${value%/*}
  prefix=
  [[ $value == */* ]] && prefix=${value#*/}
  if [[ $address =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]]; then
    for octet in "${BASH_REMATCH[@]:1}"; do (( 10#$octet <= 255 )) || return 1; done
    [[ -z $prefix ]] || { [[ $prefix =~ ^[0-9]{1,2}$ ]] && (( 10#$prefix <= 32 )); } || return 1
    return 0
  fi
  if [[ $address =~ ^[0-9A-Fa-f:]+$ && $address == *:* && ${#address} -le 39 ]]; then
    [[ -z $prefix ]] || { [[ $prefix =~ ^[0-9]{1,3}$ ]] && (( 10#$prefix <= 128 )); } || return 1
    return 0
  fi
  return 1
}

# install_tenant_egress_lines RESOLV_CONF ALLOW_LIST prints the IPAddressAllow
# and IPAddressDeny lines, or fails naming an invalid allow-list entry.
install_tenant_egress_lines() {
  local resolv=$1 allowlist=$2 allow="localhost" server entry
  if [[ -f $resolv ]]; then
    while read -r keyword server _; do
      [[ $keyword == nameserver ]] || continue
      server=${server%%%*}
      # Loopback resolvers (systemd-resolved, dnsmasq) are already allowed.
      [[ $server == 127.* || $server == ::1 ]] && continue
      install_tenant_egress_address_valid "$server" || continue
      [[ " $allow " == *" $server "* ]] || allow+=" $server"
    done < "$resolv"
  fi
  for entry in ${allowlist//,/ }; do
    install_tenant_egress_address_valid "$entry" || {
      echo "STEPANEL_TENANT_EGRESS_ALLOW contains an invalid address or range: $entry" >&2
      return 1
    }
    [[ " $allow " == *" $entry "* ]] || allow+=" $entry"
  done
  printf 'IPAddressAllow=%s\nIPAddressDeny=%s\n' "$allow" "$TENANT_EGRESS_DENY"
}
