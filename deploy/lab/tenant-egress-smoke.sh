#!/usr/bin/env bash
# Prove the tenant egress policy on an installed host with a real systemd:
#
#   1. the PHP-FPM service carries the installed policy
#      (/etc/stepanel/tenant-egress.units);
#   2. a transient unit with exactly that policy, running as an unprivileged
#      user, cannot connect to a private address or to the cloud metadata
#      address, can connect to a public address, and can still resolve DNS
#      through the host's nameservers.
#
# The listeners sit on a dummy interface inside this disposable host, so no
# packet leaves it. Changes network interfaces and restores them on exit. Run
# only on a disposable --unsafe-lab install.
set -Eeuo pipefail

[[ $EUID -eq 0 ]] || { echo 'tenant egress smoke must run as root' >&2; exit 1; }
grep -q '^STEPANEL_UNSAFE_LAB="\?1"\?$' /etc/ste-panel.env || { echo 'refusing to run: not an --unsafe-lab install' >&2; exit 1; }
for tool in ip python3 systemd-run getent; do command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }; done
policy_file=/etc/stepanel/tenant-egress.units
[[ -f $policy_file ]] || { echo "tenant egress policy $policy_file is missing" >&2; exit 1; }

link=stepegress0
private_addr=10.250.77.1
metadata_addr=169.254.169.254
public_addr=22.255.0.11
port=18080
work=$(mktemp -d)
probe_dir=/run/stepanel-egress-probe
listener=
fail() { echo "tenant egress smoke: $*" >&2; exit 1; }
cleanup() {
  local status=$?
  [[ -z $listener ]] || kill "$listener" 2>/dev/null || true
  ip link del "$link" 2>/dev/null || true
  rm -f /run/systemd/system/stepanel-egress-probe-*.service
  systemctl daemon-reload 2>/dev/null || true
  rm -rf -- "$work" "$probe_dir"
  exit "$status"
}
trap cleanup EXIT

allow=$(sed -n 's/^IPAddressAllow=//p' "$policy_file")
deny=$(sed -n 's/^IPAddressDeny=//p' "$policy_file")
[[ -n $deny ]] || fail "$policy_file has no IPAddressDeny line"

# 1. PHP-FPM carries the policy.
fpm_units=$(systemctl list-units --type=service --no-legend --plain 'php*-fpm.service' 'php-fpm.service' 2>/dev/null | awk '{print $1}')
[[ -n $fpm_units ]] || fail 'no running PHP-FPM service'
for unit in $fpm_units; do
  shown=$(systemctl show "$unit" -p IPAddressDeny --value)
  for range in 10.0.0.0/8 192.168.0.0/16 169.254.0.0/16; do
    [[ " $shown " == *" $range"* ]] || fail "$unit IPAddressDeny lacks $range: $shown"
  done
  # "localhost" is unit-file syntax; systemd must have expanded it.
  allowed=$(systemctl show "$unit" -p IPAddressAllow --value)
  [[ " $allowed " == *" 127.0.0.0/8"* ]] || fail "$unit IPAddressAllow lacks loopback (was localhost parsed?): $allowed"
done
echo "ok: PHP-FPM ($fpm_units) carries the tenant egress policy"

# 2. Listeners on private, metadata and public addresses.
ip link add "$link" type dummy
for address in "$private_addr" "$metadata_addr" "$public_addr"; do
  ip addr add "$address/32" dev "$link"
done
ip link set "$link" up
python3 -I -c '
import socketserver, sys
class Handler(socketserver.BaseRequestHandler):
    def handle(self):
        self.request.sendall(b"ok\n")
socketserver.ThreadingTCPServer.allow_reuse_address = True
with socketserver.ThreadingTCPServer(("0.0.0.0", int(sys.argv[1])), Handler) as server:
    server.serve_forever()
' "$port" &
listener=$!
for _ in $(seq 1 50); do (exec 3<>"/dev/tcp/$public_addr/$port") 2>/dev/null && break; sleep 0.1; done
(exec 3<>"/dev/tcp/$public_addr/$port") 2>/dev/null || fail 'listener did not start'

# probe NAME POLICY COMMAND runs COMMAND as nobody in a oneshot unit file
# (not systemd-run properties: those reject unit-file syntax such as
# "localhost") carrying POLICY: "full" for the installed policy lines
# verbatim, "deny" for its IPAddressDeny line alone, or "none". The unit's
# exit status and journal are kept in $work/NAME.out.
install -d -m 0755 -o root -g root "$probe_dir"
probe() {
  local name=$1 mode=$2 command=$3 unit status=0
  unit="stepanel-egress-probe-${name//./-}"
  printf '#!/bin/bash\n%s\n' "$command" > "$probe_dir/$name.sh"
  chmod 0755 "$probe_dir/$name.sh"
  {
    printf '[Service]\nType=oneshot\nUser=nobody\nExecStart=/bin/bash %s\n' "$probe_dir/$name.sh"
    case $mode in
      full) cat "$policy_file" ;;
      deny) grep '^IPAddressDeny=' "$policy_file" ;;
      none) ;;
    esac
  } > "/run/systemd/system/$unit.service"
  systemctl daemon-reload
  systemctl start "$unit.service" >/dev/null 2>&1 || status=$?
  { echo "exit status $status"; journalctl --no-pager -o cat -u "$unit.service" 2>/dev/null | tail -20; } > "$work/$name.out"
  systemctl reset-failed "$unit.service" 2>/dev/null || true
  return "$status"
}
connect_command() { printf "timeout 5 bash -c 'exec 3<>/dev/tcp/%s/%s' && echo connected" "$1" "$port"; }
show() { sed 's/^/    /' "$work/$1.out" >&2; }

# The probe itself must work before its refusals mean anything.
probe baseline none "$(connect_command "$public_addr")" || { show baseline; fail 'a probe unit cannot reach the public listener even without a policy; the probe proves nothing'; }
probe baseline-private none "$(connect_command "$private_addr")" || { show baseline-private; fail 'a probe unit cannot reach the private listener even without a policy; the probe proves nothing'; }
if ! probe public full "$(connect_command "$public_addr")"; then
  show public
  probe public-deny-only deny "$(connect_command "$public_addr")" && deny_only=reachable || deny_only=refused
  fail "the installed policy refused a public address (deny line alone: $deny_only; allow=$allow; deny=$deny)"
fi
for address in "$private_addr" "$metadata_addr"; do
  if probe "refused-$address" full "$(connect_command "$address")"; then
    fail "tenant code reached $address under the installed policy"
  fi
done
probe dns full 'getent ahosts github.com' \
  || { show dns; fail 'DNS resolution failed under the installed policy (are the nameservers allowed?)'; }
echo "ok: under the installed policy tenant code reached the public address and DNS, and was refused $private_addr and $metadata_addr"
echo 'tenant egress smoke passed'
