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
listener=
fail() { echo "tenant egress smoke: $*" >&2; exit 1; }
cleanup() {
  local status=$?
  [[ -z $listener ]] || kill "$listener" 2>/dev/null || true
  ip link del "$link" 2>/dev/null || true
  rm -rf -- "$work"
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

# connects NAME ADDRESS [systemd-run property...] tries one TCP connection to
# ADDRESS as an unprivileged user in a transient unit with the given
# properties, keeping the unit's output in $work/NAME.out.
connects() {
  local name=$1 address=$2
  shift 2
  run_unit "$name" "$@" /bin/bash -c "timeout 5 bash -c 'exec 3<>/dev/tcp/$address/$port' && echo connected"
}
# run_unit NAME [property...] COMMAND... runs COMMAND as an unprivileged user
# in a transient unit named for NAME and keeps the unit's journal and result
# in $work/NAME.out. Output goes to the journal, not a file: systemd-run
# --pipe fails when the drill runs inside a system service ("Remote peer
# disconnected"), and under SELinux PID 1 may not open a file the drill
# created, which fails the unit before its command runs.
run_unit() {
  local name=$1 unit status=0
  shift
  local -a properties=()
  while [[ $1 == -p ]]; do properties+=("$1" "$2"); shift 2; done
  unit="stepanel-egress-probe-$name-$$-$RANDOM"
  unit=${unit//./-}
  systemd-run --quiet --wait --collect --unit="$unit" -p User=nobody "${properties[@]}" -- "$@" || status=$?
  { echo "exit status $status"; journalctl --no-pager -o cat -u "$unit" 2>/dev/null | tail -20; } > "$work/$name.out"
  return "$status"
}
show() { sed 's/^/    /' "$work/$1.out" >&2; }
policy=(-p "IPAddressAllow=$allow" -p "IPAddressDeny=$deny")

# The probe itself must work before its refusals mean anything.
connects baseline "$public_addr" || { show baseline; fail 'a transient unit cannot reach the public listener even without a policy; the probe proves nothing'; }
connects baseline-private "$private_addr" || { show baseline-private; fail 'a transient unit cannot reach the private listener even without a policy; the probe proves nothing'; }
if ! connects public "$public_addr" "${policy[@]}"; then
  show public
  connects public-deny-only "$public_addr" -p "IPAddressDeny=$deny" && deny_only=reachable || deny_only=refused
  connects public-allow-only "$public_addr" -p "IPAddressAllow=$allow" && allow_only=reachable || allow_only=refused
  fail "the policy refused a public address (deny list alone: $deny_only; allow list alone: $allow_only; allow=$allow; deny=$deny)"
fi
for address in "$private_addr" "$metadata_addr"; do
  if connects "refused-$address" "$address" "${policy[@]}"; then
    fail "tenant code reached $address under the policy"
  fi
done
run_unit dns "${policy[@]}" /bin/bash -c 'getent ahosts github.com' \
  || { show dns; fail 'DNS resolution failed under the policy (are the nameservers allowed?)'; }
echo "ok: under the policy tenant code reached the public address and DNS, and was refused $private_addr and $metadata_addr"
echo 'tenant egress smoke passed'
