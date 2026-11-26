#!/usr/bin/env bash
# Contract for deploy/lib/install-tenant-egress.sh and the policy reader in
# stepanel-appctl: tenant code is denied private networks and metadata, keeps
# loopback and the host's nameservers, and gets only valid operator exceptions.
set -Eeuo pipefail
cd "$(dirname "$0")/.."
# shellcheck source=../deploy/lib/install-tenant-egress.sh
. deploy/lib/install-tenant-egress.sh
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
fail() { echo "tenant egress contract: $*" >&2; exit 1; }

cat > "$work/resolv.conf" <<'EOF'
# managed by NetworkManager
search example.internal
nameserver 10.0.0.2
nameserver 127.0.0.53
nameserver fe80::1%eth0
nameserver 10.0.0.2
options edns0
EOF
lines=$(install_tenant_egress_lines "$work/resolv.conf" "10.20.0.5, 192.168.50.0/24")
allow=$(sed -n 's/^IPAddressAllow=//p' <<<"$lines")
deny=$(sed -n 's/^IPAddressDeny=//p' <<<"$lines")
[[ $allow == "localhost 10.0.0.2 fe80::1 10.20.0.5 192.168.50.0/24" ]] || fail "allow list = '$allow'"
for range in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 100.64.0.0/10 169.254.0.0/16 100.100.100.200/32 fc00::/7 fe80::/10; do
  [[ " $deny " == *" $range "* ]] || fail "deny list is missing $range: '$deny'"
done
[[ $(wc -l <<<"$lines") == 2 ]] || fail "expected exactly two policy lines: $lines"

# A loopback-only resolver adds nothing beyond localhost.
printf 'nameserver 127.0.0.53\n' > "$work/stub.conf"
[[ $(install_tenant_egress_lines "$work/stub.conf" "" | head -1) == "IPAddressAllow=localhost" ]] || fail 'loopback resolver was listed'
# A missing resolv.conf still yields a policy.
install_tenant_egress_lines "$work/missing.conf" "" | grep -qx 'IPAddressAllow=localhost' || fail 'missing resolv.conf broke the policy'

for bad in "10.0.0.300" "10.0.0.0/33" "db.internal" "fd00::/129" "10.0.0.1;IPAddressAllow=any" "any"; do
  if install_tenant_egress_lines "$work/stub.conf" "$bad" >/dev/null 2>&1; then
    fail "invalid allow-list entry '$bad' was accepted"
  fi
done

# Every policy line the installer writes must pass the appctl reader's check.
reader_pattern='^IPAddress(Allow|Deny)=[0-9A-Fa-f:./\ a-z]+$'
while IFS= read -r line; do
  [[ $line =~ $reader_pattern ]] || fail "appctl would reject the installer's line: $line"
done <<<"$lines"
grep -Fq "[[ \$line =~ ^IPAddress(Allow|Deny)=[0-9A-Fa-f:./\\ a-z]+\$ ]]" deploy/integrations/stepanel-appctl || fail 'appctl policy check changed; update this contract'
echo 'tenant egress contract passed'
