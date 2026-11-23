#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $EUID -ne 0 ]]; then
  echo 'Run this host preflight through sudo.' >&2
  exit 1
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
. "$repo_root/deploy/lib/install-support-tier.sh"
. "$repo_root/deploy/lib/install-systemd-preflight.sh"
. "$repo_root/deploy/lib/install-offsite-preflight.sh"
. "$repo_root/deploy/lib/install-doctor.sh"
. "$repo_root/deploy/integrations/stepanel-quota-state.sh"
. "$repo_root/deploy/lib/install-quota-preflight.sh"

echo 'Checking host requirements before downloading the Go toolchain or building StePanel.'
INSTALL_DOCTOR_FAILURES=0
install_doctor_check \
  'Linux distribution detected and package manager available' \
  'Use a systemd Linux distribution supported by the installer; review docs/INSTALLATION.md#supported-operating-systems.' \
  install_guided_host_support || true
install_doctor_check \
  'Running systemd manager' \
  'Boot the host with systemd and confirm /run/systemd/system is available; native installation cannot safely configure services without systemd.' \
  install_preflight_systemd || true
install_doctor_check \
  'Enforceable user quotas on hosting filesystems' \
  'Enable and verify user quotas on the filesystems containing /var/www and /home. See docs/INSTALLATION.md#prepare-filesystem-quotas; StePanel will not disable this requirement.' \
  install_preflight_user_quotas /var/www /home || true
install_doctor_check \
  'Offsite backup tooling and configuration' \
  'Install rclone, then configure a remote with sudo rclone config. The setup wizard verifies write/read/delete access to the selected destination.' \
  install_preflight_offsite || true

install_doctor_finish "$repo_root"
