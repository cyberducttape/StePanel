#!/usr/bin/env bash
set -Eeuo pipefail

# Runs StePanel's install, recovery, and failure drills inside a disposable
# Rocky Linux 9 KVM guest on a developer workstation, and records a
# per-phase result table. It mirrors .github/workflows/quota-install-smoke.yml
# but uses hardware virtualization and a RHEL-family guest.
#
# Usage: deploy/lab/local-kvm-certification.sh IMAGE.qcow2 [RUN_DIR]
#
# IMAGE is a Rocky Linux 9 GenericCloud qcow2. Its SHA-256 is recorded in
# the results; verify it against the Rocky CHECKSUM file before use. RUN_DIR
# (default /var/tmp/stepanel-kvm-cert) must be on a disk-backed filesystem:
# the guest disks are several GiB.
#
# Environment: VM_MEMORY_MB (3072), VM_CPUS (4), SSH_PORT (2232),
# PHASES (space-separated subset of: boot install runner abrupt-loss
# abrupt-loss-under-load enospc), KEEP_VM=1 to leave the guest running,
# RESUME=1 to run PHASES against the guest a previous KEEP_VM=1 run left in
# RUN_DIR (no rebuild or reboot; results are appended).
#
# Every phase runs only inside the guest. Nothing is installed on the host.

image=${1:?usage: local-kvm-certification.sh IMAGE.qcow2 [RUN_DIR]}
run_dir=${2:-/var/tmp/stepanel-kvm-cert}
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
memory=${VM_MEMORY_MB:-3072}
cpus=${VM_CPUS:-4}
port=${SSH_PORT:-2232}
phases=${PHASES:-boot install runner abrupt-loss abrupt-loss-under-load enospc}

[[ -f $image ]] || { echo "guest image $image not found" >&2; exit 1; }
[[ -r /dev/kvm && -w /dev/kvm ]] || { echo '/dev/kvm is not usable by this user' >&2; exit 1; }
for tool in qemu-system-x86_64 qemu-img genisoimage ssh scp ssh-keygen go; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 1; }
done
[[ $(findmnt -no FSTYPE --target "$(dirname "$run_dir")") != tmpfs ]] || { echo "$run_dir must not be on tmpfs" >&2; exit 1; }
results="$run_dir/results.tsv"
if [[ ${RESUME:-0} == 1 ]]; then
  [[ -s $run_dir/qemu.pid ]] && kill -0 "$(<"$run_dir/qemu.pid")" 2>/dev/null || { echo "RESUME=1 needs the running guest from a KEEP_VM=1 run in $run_dir" >&2; exit 1; }
else
  if [[ -e $run_dir ]]; then echo "refusing to reuse $run_dir; remove it first" >&2; exit 1; fi
  mkdir -p "$run_dir"
  printf 'phase\tresult\tseconds\tdetail\n' > "$results"
fi

record() { printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" >> "$results"; printf '== %s: %s (%ss) %s\n' "$1" "$2" "$3" "$4"; }

ssh_args=(-i "$run_dir/id_ed25519" -p "$port" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=3 -o LogLevel=ERROR)
guest() { ssh "${ssh_args[@]}" vmtest@127.0.0.1 "$@"; }

start_guest() {
  qemu-system-x86_64 \
    -machine accel=kvm -cpu host -smp "$cpus" -m "$memory" \
    -drive "file=$run_dir/root.qcow2,if=virtio,format=qcow2" \
    -drive "file=$run_dir/www.ext4,if=virtio,format=raw" \
    -drive "file=$run_dir/enospc.ext4,if=virtio,format=raw" \
    -drive "file=$run_dir/seed.iso,if=virtio,media=cdrom,readonly=on" \
    -netdev "user,id=net0,hostfwd=tcp:127.0.0.1:$port-:22" \
    -device virtio-net-pci,netdev=net0 \
    -display none -serial "file:$run_dir/console.log" -monitor none \
    -daemonize -pidfile "$run_dir/qemu.pid"
}

kill_guest() {
  [[ -s $run_dir/qemu.pid ]] || return 0
  local pid; pid=$(<"$run_dir/qemu.pid")
  kill -KILL "$pid" 2>/dev/null || true
  for _ in $(seq 1 30); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
  ! kill -0 "$pid" 2>/dev/null || { echo 'QEMU did not stop' >&2; return 1; }
  rm -f "$run_dir/qemu.pid"
}

cleanup() {
  if [[ ${KEEP_VM:-0} != 1 ]]; then kill_guest || true; fi
}
trap cleanup EXIT

wait_ready() {
  local deadline=$((SECONDS + $1))
  while (( SECONDS < deadline )); do
    if timeout --foreground 20s ssh "${ssh_args[@]}" vmtest@127.0.0.1 \
      'sudo systemctl is-active --quiet stepanel.service stepanel-worker.service stepanel-root-broker.service && curl --fail --silent http://127.0.0.1:8090/readyz >/dev/null' 2>/dev/null; then
      return 0
    fi
    sleep 2
  done
  return 1
}

has_phase() { [[ " $phases " == *" $1 "* ]]; }

# guest_job NAME MINUTES COMMAND runs COMMAND as root inside the guest,
# detached from the SSH session (a dropped connection must not stall or kill
# a multi-hour drill), then polls for its exit status and copies its log to
# $run_dir/NAME.log. It returns the command's exit status.
guest_job() {
  local name=$1 minutes=$2 command=$3 deadline status
  # Clear a previous run's status first, or the poll below would read it.
  printf '%s\n' "$command" | guest "sudo install -d -m 0700 /var/log/stepanel-cert && sudo rm -f /var/log/stepanel-cert/$name.status /var/log/stepanel-cert/$name.log && sudo tee /var/log/stepanel-cert/$name.cmd >/dev/null"
  guest "sudo setsid nohup bash -c 'bash /var/log/stepanel-cert/$name.cmd > /var/log/stepanel-cert/$name.log 2>&1; echo \$? > /var/log/stepanel-cert/$name.status' < /dev/null > /dev/null 2>&1 &"
  deadline=$((SECONDS + minutes * 60))
  status=''
  while (( SECONDS < deadline )); do
    status=$(timeout --foreground 20s ssh "${ssh_args[@]}" vmtest@127.0.0.1 "sudo cat /var/log/stepanel-cert/$name.status 2>/dev/null" 2>/dev/null || true)
    [[ -n $status ]] && break
    sleep 30
  done
  timeout --foreground 60s ssh "${ssh_args[@]}" vmtest@127.0.0.1 "sudo cat /var/log/stepanel-cert/$name.log" > "$run_dir/$name.log" 2>/dev/null || true
  [[ -n $status ]] || { echo "guest job $name exceeded ${minutes} minutes" >> "$run_dir/$name.log"; return 124; }
  return "$status"
}

if [[ ${RESUME:-0} != 1 ]]; then
{
  printf 'image: %s\nimage_sha256: %s\n' "$image" "$(sha256sum "$image" | cut -d' ' -f1)"
  printf 'commit: %s\n' "$(git -C "$repo" rev-parse HEAD)"
  printf 'dirty: %s\n' "$(git -C "$repo" status --porcelain | wc -l)"
  printf 'host_qemu: %s\nhost_kernel: %s\nguest_memory_mb: %s\nguest_cpus: %s\n' "$(qemu-system-x86_64 --version | head -1)" "$(uname -r)" "$memory" "$cpus"
  printf 'started_utc: %s\n' "$(date -u +%FT%TZ)"
} > "$run_dir/environment.txt"

# --- Build the release-shaped tree on the host -----------------------------
(cd "$repo" && go build -trimpath -ldflags='-s -w' -o "$run_dir/stepanel" ./cmd/stepanel \
  && go build -trimpath -ldflags='-s -w' -o "$run_dir/stepanel-root" ./cmd/stepanel-root \
  && go test -c -trimpath -o "$run_dir/stepanel-enospc.test" .)
tar -czf "$run_dir/source.tar.gz" --exclude=.git --exclude=node_modules --exclude=test-results -C "$repo" . \
  -C "$run_dir" stepanel stepanel-root stepanel-enospc.test

# --- Boot ------------------------------------------------------------------
qemu-img create -q -f qcow2 -F qcow2 -b "$(realpath "$image")" "$run_dir/root.qcow2" 20G
qemu-img create -q -f raw "$run_dir/www.ext4" 3G
qemu-img create -q -f raw "$run_dir/enospc.ext4" 96M
ssh-keygen -q -t ed25519 -N '' -f "$run_dir/id_ed25519"
cat > "$run_dir/user-data" <<EOF
#cloud-config
users:
  - default
  - name: vmtest
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - $(<"$run_dir/id_ed25519.pub")
packages:
  - e2fsprogs
  - quota
  - podman
  - fuse-overlayfs
  - slirp4netns
  - shadow-utils
  - tar
runcmd:
  - |
    set -eux
    mkfs.ext4 -F -O quota -E quotatype=usrquota /dev/vdb
    install -d -m 0755 /var/www
    printf '%s\n' '/dev/vdb /var/www ext4 usrquota 0 2' >> /etc/fstab
    mount /var/www
    quotaon -p -u /var/www | grep -F 'is on'
    mkfs.ext4 -F /dev/vdc
    install -d -m 0700 /mnt/stepanel-enospc
    printf '%s\n' '/dev/vdc /mnt/stepanel-enospc ext4 defaults 0 2' >> /etc/fstab
    mount /mnt/stepanel-enospc
    touch /var/lib/stepanel-cert-ready
EOF
printf 'instance-id: stepanel-cert\nlocal-hostname: stepanel-cert\n' > "$run_dir/meta-data"
genisoimage -quiet -output "$run_dir/seed.iso" -volid cidata -joliet -rock "$run_dir/user-data" "$run_dir/meta-data"

start=$SECONDS
start_guest
booted=0
for _ in $(seq 1 120); do
  if timeout --foreground 20s ssh "${ssh_args[@]}" vmtest@127.0.0.1 'sudo cloud-init status --wait >/dev/null; test -e /var/lib/stepanel-cert-ready' 2>/dev/null; then booted=1; break; fi
  sleep 5
done
if (( ! booted )); then
  record boot FAIL $((SECONDS - start)) 'cloud-init did not finish; see console.log'
  exit 1
fi
record boot PASS $((SECONDS - start)) "$(guest 'uname -r; findmnt -no OPTIONS /var/www' | tr '\n' ' ')"
scp -q -P "$port" -i "$run_dir/id_ed25519" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "$run_dir/source.tar.gz" vmtest@127.0.0.1:/tmp/source.tar.gz
guest 'sudo mkdir -p /work && sudo tar -xzf /tmp/source.tar.gz -C /work'
fi

# --- Install, smoke workflows, and the full recovery matrix ------------------
if has_phase install; then
  start=$SECONDS
  # The full recovery matrix runs about 100 drills; allow four hours.
  if guest_job install 240 'env STEPANEL_UNSAFE_LAB=1 STEPANEL_QUOTA_SMOKE=1 STEPANEL_DB_ENGINE=mariadb STEPANEL_WEBSERVER=caddy STEPANEL_RUN_RECOVERY_MATRIX=1 STEPANEL_RUN_RECOVERY_MATRIX_FULL=1 bash /work/deploy/lab/install-smoke.sh'; then
    record install PASS $((SECONDS - start)) "$(grep -c 'smoke passed\|matrix passed' "$run_dir/install.log") smoke/matrix passes"
  else
    record install FAIL $((SECONDS - start)) "$(grep -E 'smoke failed|exited with status|unbound|error' "$run_dir/install.log" | tail -1)"
    exit 1
  fi
fi

if has_phase runner; then
  start=$SECONDS
  if guest_job runner 30 'env STEPANEL_UNSAFE_LAB=1 RUNNER_TEST_IMAGE=ghcr.io/containerd/busybox:1.36@sha256:907ca53d7e2947e849b839b1cd258c98fd3916c60f2e6e70c30edbf741ab6754 bash /work/deploy/lab/runner-perm-smoke.sh && env STEPANEL_UNSAFE_LAB=1 bash /work/deploy/lab/release-pipeline-smoke.sh'; then
    record runner PASS $((SECONDS - start)) 'rootless runner, failed-build retention, and release pipeline build-to-activation'
  else
    record runner FAIL $((SECONDS - start)) "$(tail -1 "$run_dir/runner.log")"
  fi
fi

# --- Abrupt guest loss while idle -------------------------------------------
if has_phase abrupt-loss; then
  start=$SECONDS
  kill_guest
  start_guest
  if wait_ready 600; then
    record abrupt-loss PASS $((SECONDS - start)) 'services and readiness recovered after QEMU SIGKILL (recovery time includes guest boot)'
  else
    record abrupt-loss FAIL $((SECONDS - start)) 'readiness did not recover'
    guest 'sudo journalctl -b --no-pager -n 200' > "$run_dir/abrupt-loss-journal.log" 2>&1 || true
  fi
fi

# --- Abrupt guest loss during a privileged backup ----------------------------
# Starts a backup of a site with enough content to take several seconds, kills
# the whole guest mid-operation, then requires readiness, a consistent backup
# index (no partial backup published), and a successful follow-up backup.
if has_phase abrupt-loss-under-load; then
  start=$SECONDS
  if guest 'sudo bash /work/deploy/lab/abrupt-loss-backup-drill.sh prepare' > "$run_dir/abrupt-load-prepare.log" 2>&1; then
    guest 'sudo bash /work/deploy/lab/abrupt-loss-backup-drill.sh start' > "$run_dir/abrupt-load-start.log" 2>&1 || true
    sleep "${ABRUPT_LOSS_DELAY:-3}"
    kill_guest
    start_guest
    if wait_ready 600 && guest 'sudo bash /work/deploy/lab/abrupt-loss-backup-drill.sh verify' > "$run_dir/abrupt-load-verify.log" 2>&1; then
      record abrupt-loss-under-load PASS $((SECONDS - start)) "$(tail -1 "$run_dir/abrupt-load-verify.log")"
    else
      record abrupt-loss-under-load FAIL $((SECONDS - start)) "$(tail -1 "$run_dir/abrupt-load-verify.log" 2>/dev/null)"
      guest 'sudo journalctl -b --no-pager -n 300' > "$run_dir/abrupt-load-journal.log" 2>&1 || true
    fi
  else
    record abrupt-loss-under-load FAIL $((SECONDS - start)) "prepare: $(tail -1 "$run_dir/abrupt-load-prepare.log")"
  fi
fi

# --- Real ENOSPC on a small dedicated filesystem ------------------------------
if has_phase enospc; then
  start=$SECONDS
  if guest_job enospc 10 'env STEPANEL_ENOSPC_BACKUP_ROOT=/mnt/stepanel-enospc /work/stepanel-enospc.test -test.run "^TestBackupRecoversFromRealENOSPC$" -test.v'; then
    record enospc PASS $((SECONDS - start)) 'backup recovered from real ENOSPC'
  else
    record enospc FAIL $((SECONDS - start)) "$(tail -1 "$run_dir/enospc.log")"
  fi
fi

printf 'finished_utc: %s\n' "$(date -u +%FT%TZ)" >> "$run_dir/environment.txt"
column -t -s $'\t' "$results"
! grep -q $'\tFAIL\t' "$results"
