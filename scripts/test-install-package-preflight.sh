#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../deploy/lib/install-package-preflight.sh
. "$ROOT_DIR/deploy/lib/install-package-preflight.sh"
package_preflight_line=$(grep -n 'install_preflight_packages web_package_available' "$ROOT_DIR/install.sh" | cut -d: -f1)
optional_package_preflight_line=$(grep -n 'install_preflight_optional_packages "\$PKG"' "$ROOT_DIR/install.sh" | cut -d: -f1)
package_mutation_line=$(grep -n 'apt-get update; apt-get install' "$ROOT_DIR/install.sh" | cut -d: -f1)
(( optional_package_preflight_line < package_preflight_line && package_preflight_line < package_mutation_line ))
missing_package=
package_available() {
  [[ $1 != "$missing_package" ]]
}

result=$(install_preflight_packages package_available php php-cli mariadb-server caddy)
[[ $result == 'all 4 required and selected optional packages are available in repository metadata.' ]]

missing_package=php-xml
if message=$(install_preflight_packages package_available php php-xml caddy 2>&1); then
  echo 'missing required package was accepted' >&2
  exit 1
fi
grep -Fq 'php-xml' <<<"$message"
grep -Fq 'Refresh package indexes or enable the repository' <<<"$message"

mapfile -d '' -t apt_optional < <(
  install_preflight_optional_packages apt mariadb apache 1 1 1 1 1 1 1
)
[[ ${apt_optional[*]} == 'fail2ban libapache2-mod-security2 modsecurity-crs exim4 dovecot-core dovecot-imapd spamassassin vsftpd phpmyadmin clamav clamav-daemon inotify-tools certbot python3-certbot-apache' ]]
mapfile -d '' -t dnf_optional < <(
  install_preflight_optional_packages dnf postgresql apache 1 1 1 1 1 1 1
)
[[ ${dnf_optional[*]} == 'fail2ban mod_security mod_security_crs exim dovecot spamassassin vsftpd phppgadmin clamav clamav-update inotify-tools certbot python3-certbot-apache' ]]
mapfile -d '' -t no_optional < <(
  install_preflight_optional_packages apt mariadb caddy 0 0 0 0 0 0 0
)
(( ${#no_optional[@]} == 0 ))
mapfile -d '' -t no_caddy_tls < <(
  install_preflight_optional_packages apt mariadb caddy 0 0 0 0 0 0 1
)
(( ${#no_caddy_tls[@]} == 0 ))

echo 'installer package preflight contract passed'
