#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../deploy/lib/install-first-login.sh
. "$ROOT_DIR/deploy/lib/install-first-login.sh"

caddy=$(install_print_first_login_steps panel.example.com caddy 0 caddy mariadb)
grep -Fq 'https://panel.example.com' <<<"$caddy"
grep -Fq 'Caddy obtains HTTPS automatically' <<<"$caddy"
grep -Fq 'allow inbound ports 80 and 443' <<<"$caddy"
grep -Fq 'stepanel-root-broker.service caddy mariadb' <<<"$caddy"

apache_tls=$(install_print_first_login_steps panel.example.com apache 1 apache2 mariadb)
grep -Fq 'sudo stepanel-certbot "panel.example.com" "you@example.com"' <<<"$apache_tls"

apache_manual=$(install_print_first_login_steps panel.example.com apache 0 httpd mariadb)
grep -Fq 'Configure trusted TLS termination' <<<"$apache_manual"
! grep -Fq 'sudo stepanel-certbot' <<<"$apache_manual"

ols=$(install_print_first_login_steps panel.example.com openlitespeed 0 lsws mariadb)
grep -Fq 'Configure and verify HTTPS for panel.example.com in openlitespeed' <<<"$ols"

echo 'installer first-login guidance contract passed'
