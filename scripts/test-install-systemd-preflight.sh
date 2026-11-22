#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../deploy/lib/install-systemd-preflight.sh
. "$ROOT_DIR/deploy/lib/install-systemd-preflight.sh"
systemd_check_line=$(grep -n 'install_preflight_systemd); then' "$ROOT_DIR/install.sh" | cut -d: -f1)
package_mutation_line=$(grep -n 'apt-get update; apt-get install' "$ROOT_DIR/install.sh" | cut -d: -f1)
(( systemd_check_line < package_mutation_line ))
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/systemd"
cat > "$tmp/bin/systemctl" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod 0700 "$tmp/bin/systemctl"

PATH="$tmp/bin:$PATH"
export PATH
[[ $(install_preflight_systemd "$tmp/systemd" systemctl) == 'systemd manager detected' ]]
if message=$(install_preflight_systemd "$tmp/missing" 2>&1); then
  echo 'missing systemd runtime directory was accepted' >&2
  exit 1
fi
grep -Fq 'use `make evaluate` for a local UI-only demo' <<<"$message"

if message=$(install_preflight_systemd "$tmp/systemd" missing-stepanel-systemctl 2>&1); then
  echo 'missing systemctl was accepted' >&2
  exit 1
fi
grep -Fq 'systemctl is missing' <<<"$message"
grep -Fq 'use `make evaluate` for a local UI-only demo' <<<"$message"

echo 'installer systemd preflight contract passed'
