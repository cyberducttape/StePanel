#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
guided="$ROOT_DIR/scripts/install-guided.sh"
preflight_line=$(grep -n 'install-host-preflight.sh' "$guided" | cut -d: -f1)
toolchain_line=$(grep -n 'stepanel_prepare_go ' "$guided" | cut -d: -f1)
build_line=$(grep -n 'run_go build' "$guided" | head -n1 | cut -d: -f1)

[[ -n $preflight_line && -n $toolchain_line && -n $build_line ]]
(( preflight_line < toolchain_line && preflight_line < build_line ))
grep -Fq 'install_preflight_user_quotas /var/www /home' "$ROOT_DIR/scripts/install-host-preflight.sh"
grep -Fq 'install_guided_host_support' "$ROOT_DIR/scripts/install-host-preflight.sh"
grep -Fq 'install_preflight_systemd' "$ROOT_DIR/scripts/install-host-preflight.sh"
echo 'guided host preflight ordering contract passed'
