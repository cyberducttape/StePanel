#!/usr/bin/env bash
# Run the upgrade smoke on a disposable Ubuntu 24.04 QEMU guest.
#
#   deploy/lab/upgrade-vm.sh VM_ROOT PREVIOUS_TREE CANDIDATE_TREE BROKEN_TREE PREVIOUS_VERSION
#
# The systemd container cannot expose the LoadCredential database-credential
# handoff of v0.6.0 and v0.7.0 to their service, so the upgrade of that real
# handoff is tested on a guest kernel and systemd instead (with the helper
# fixture from deploy/lab/upgrade-fixture.sh applied by the caller).
# Runs on a disposable CI runner: it installs QEMU and boots a guest with
# passwordless sudo. Uses KVM when the runner exposes it, otherwise TCG.
#
# The web server defaults to Apache (STEPANEL_WEBSERVER overrides it). On a
# real Ubuntu 24.04 host the PHP packages v0.6.0 and v0.7.0 install pull in
# and start apache2, which holds port 80, so those releases cannot complete a
# fresh Caddy install there; existing hosts of those releases run Apache.
set -Eeuo pipefail

vm_root=${1:?VM working directory is required}
previous_tree=${2:?previous release tree is required}
candidate_tree=${3:?candidate release tree is required}
broken_tree=${4:?broken release tree is required}
previous_version=${5:?previous version is required}
[[ $previous_version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "invalid previous version: $previous_version" >&2; exit 1; }
for tree in "$previous_tree" "$candidate_tree" "$broken_tree"; do
  [[ -x $tree/install.sh ]] || { echo "not a release tree: $tree" >&2; exit 1; }
done
mkdir -p "$vm_root"

sudo apt-get update
sudo apt-get install -y cloud-image-utils openssh-client qemu-system-x86 qemu-utils
accel=tcg
if [[ -e /dev/kvm ]]; then
  # Hosted runners expose /dev/kvm to root only.
  echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' | sudo tee /etc/udev/rules.d/99-stepanel-kvm.rules >/dev/null
  sudo udevadm control --reload-rules
  sudo udevadm trigger --name-match=kvm
  [[ -w /dev/kvm ]] && accel=kvm
fi
echo "guest acceleration: $accel"

image="$vm_root/noble-server-cloudimg-amd64.img"
curl --fail --silent --show-error --location https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img -o "$image"
curl --fail --silent --show-error --location https://cloud-images.ubuntu.com/noble/current/SHA256SUMS -o "$vm_root/SHA256SUMS"
(cd "$vm_root" && grep 'noble-server-cloudimg-amd64.img$' SHA256SUMS | sha256sum --check --status)
qemu-img resize "$image" 16G

ssh-keygen -q -t ed25519 -N '' -f "$vm_root/id_ed25519"
cat > "$vm_root/user-data" <<EOF
#cloud-config
users:
  - default
  - name: vmtest
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - $(<"$vm_root/id_ed25519.pub")
package_update: true
packages:
  - acl
  - curl
runcmd:
  - touch /run/stepanel-upgrade-guest-ready
EOF
cloud-localds "$vm_root/seed.iso" "$vm_root/user-data"

qemu-system-x86_64 \
  -machine "accel=$accel" -cpu max -smp 2 -m 4096 \
  -drive "file=$image,if=virtio,format=qcow2" \
  -drive "file=$vm_root/seed.iso,if=virtio,media=cdrom,readonly=on" \
  -netdev user,id=net0,hostfwd=tcp:127.0.0.1:2222-:22 \
  -device virtio-net-pci,netdev=net0 \
  -display none -serial "file:$vm_root/console.log" -monitor none \
  -daemonize -pidfile "$vm_root/qemu.pid"

ssh_options=(-i "$vm_root/id_ed25519" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=3 -o LogLevel=ERROR)
ssh_args=("${ssh_options[@]}" -p 2222)
guest() { timeout --foreground --kill-after=5s "$1" ssh "${ssh_args[@]}" vmtest@127.0.0.1 "${@:2}"; }
cleanup() {
  local status=$?
  if (( status != 0 )); then
    tail -n 80 "$vm_root/console.log" >&2 2>/dev/null || true
    guest 30s 'sudo systemctl status stepanel.service stepanel-worker.service caddy.service apache2.service --no-pager; sudo journalctl -u stepanel.service -u stepanel-worker.service -u caddy.service -u apache2.service --no-pager -n 200; sudo ss -ltnp' >&2 || true
  fi
  if [[ -s $vm_root/qemu.pid ]]; then kill "$(<"$vm_root/qemu.pid")" 2>/dev/null || true; fi
}
trap cleanup EXIT

# Poll without `cloud-init status --wait`, which can hold the connection for
# its whole timeout on every attempt.
ready=0
for _ in $(seq 1 240); do
  status=$(guest 20s 'sudo cloud-init status --format json' 2>/dev/null || true)
  if grep -Eq '"(status|extended_status)"[[:space:]]*:[[:space:]]*"(error|degraded)"' <<<"$status"; then
    echo 'guest cloud-init failed' >&2
    guest 20s 'sudo cloud-init status --long; sudo tail -n 120 /var/log/cloud-init-output.log' >&2 || true
    exit 1
  fi
  if grep -Eq '"status"[[:space:]]*:[[:space:]]*"done"' <<<"$status" && guest 20s 'test -e /run/stepanel-upgrade-guest-ready'; then
    ready=1
    break
  fi
  sleep 5
done
(( ready )) || { echo 'guest did not finish cloud-init' >&2; exit 1; }

tar -czf "$vm_root/trees.tar.gz" \
  --transform "s|^${previous_tree#/}|previous|;s|^${candidate_tree#/}|candidate|;s|^${broken_tree#/}|broken|" \
  "$previous_tree" "$candidate_tree" "$broken_tree"
timeout --foreground --kill-after=5s 120s scp "${ssh_options[@]}" -P 2222 "$vm_root/trees.tar.gz" vmtest@127.0.0.1:/tmp/trees.tar.gz
guest 120s 'sudo install -d -m 0755 /work && sudo tar -xzf /tmp/trees.tar.gz -C /work && ls /work'

web_server=${STEPANEL_WEBSERVER:-apache}
[[ $web_server =~ ^(apache|caddy|openlitespeed)$ ]] || { echo "invalid web server: $web_server" >&2; exit 1; }
guest 80m "sudo env STEPANEL_PREVIOUS_VERSION=$previous_version STEPANEL_WEBSERVER=$web_server STEPANEL_UPGRADE_ROLLBACK=1 bash /work/candidate/deploy/lab/upgrade-smoke.sh /work/previous /work/candidate /work/broken"
