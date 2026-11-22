#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $EUID -ne 0 ]]; then
  echo 'Run this host preflight through sudo.' >&2
  exit 1
fi

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
. "$repo_root/deploy/lib/install-support-tier.sh"
. "$repo_root/deploy/lib/install-systemd-preflight.sh"
. "$repo_root/deploy/integrations/stepanel-quota-state.sh"
. "$repo_root/deploy/lib/install-quota-preflight.sh"

echo 'Checking host requirements before downloading the Go toolchain or building StePanel.'
if ! install_guided_host_support; then
  exit 1
fi

if systemd_preflight=$(install_preflight_systemd); then
  echo "HOST REQUIREMENT: $systemd_preflight"
else
  echo "BLOCKER: $systemd_preflight" >&2
  exit 1
fi

if quota_preflight=$(install_preflight_user_quotas /var/www /home); then
  echo "HOST REQUIREMENT: $quota_preflight"
else
  echo "BLOCKER: $quota_preflight" >&2
  exit 1
fi

echo 'Read-only host preflight passed. The full installer will repeat these checks before changing the host.'
