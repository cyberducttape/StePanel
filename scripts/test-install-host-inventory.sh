#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck disable=SC1091
. "$ROOT_DIR/deploy/lib/install-host-inventory.sh"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/existing"
touch "$tmp/existing/one" "$tmp/existing/two"

[[ $(install_count_directory_entries "$tmp/missing") == 0 ]]
[[ $(install_count_directory_entries "$tmp/existing") == 2 ]]
grep -Fq ". \"\$ROOT_DIR/deploy/lib/install-host-inventory.sh\"" "$ROOT_DIR/install.sh"
grep -Fq "site_entries=\$(install_count_directory_entries /var/www/sites)" "$ROOT_DIR/install.sh"

echo 'installer host-inventory contract passed'
