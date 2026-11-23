#!/usr/bin/env bash

# A native installation manages system services and cannot run in a container
# or chroot without a live systemd manager. Keep this check read-only so it can
# run before package installation or service changes.
install_preflight_systemd() {
  local runtime_dir=${1:-/run/systemd/system} systemctl_cmd=${2:-systemctl}
  if ! command -v "$systemctl_cmd" >/dev/null 2>&1; then
    printf '%s' 'systemctl is missing. Native StePanel installation requires systemd; use `bash scripts/evaluate-local.sh` for a local UI-only demo.'
    return 1
  fi
  if [[ ! -d $runtime_dir ]]; then
    printf '%s' 'No running systemd manager was detected. Install on a systemd host; use `bash scripts/evaluate-local.sh` for a local UI-only demo.'
    return 1
  fi
  printf '%s' 'systemd manager detected'
}
