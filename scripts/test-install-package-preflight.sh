#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../deploy/lib/install-package-preflight.sh
. "$ROOT_DIR/deploy/lib/install-package-preflight.sh"
package_preflight_line=$(grep -n 'install_preflight_packages web_package_available' "$ROOT_DIR/install.sh" | cut -d: -f1)
package_mutation_line=$(grep -n 'apt-get update; apt-get install' "$ROOT_DIR/install.sh" | cut -d: -f1)
(( package_preflight_line < package_mutation_line ))
missing_package=
package_available() {
  [[ $1 != "$missing_package" ]]
}

result=$(install_preflight_packages package_available php php-cli mariadb-server caddy)
[[ $result == 'all 4 required base, PHP, database, and web-server packages are available.' ]]

missing_package=php-xml
if message=$(install_preflight_packages package_available php php-xml caddy 2>&1); then
  echo 'missing required package was accepted' >&2
  exit 1
fi
grep -Fq 'php-xml' <<<"$message"
grep -Fq 'Refresh package indexes or enable the repository' <<<"$message"

echo 'installer package preflight contract passed'
