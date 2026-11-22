#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=./deploy/lib/install-change-plan.sh
. "$ROOT_DIR/deploy/lib/install-change-plan.sh"

all=$(install_print_optional_change_plan apache 1 1 On 1 0 1 0 1 1 1 1 '20.18.0,22.14.0')
grep -Fq 'CONFIGURE Apache ModSecurity and OWASP CRS (mode: On)' <<<"$all"
grep -Fq 'expose it through an Apache route restricted to configured IP/CIDR addresses' <<<"$all"
grep -Fq 'leave new services stopped and preserve pre-existing service state' <<<"$all"
grep -Fq 'leave a new service stopped and preserve pre-existing service state' <<<"$all"
grep -Fq 'activate stepanel-malware-guard after core commit' <<<"$all"
grep -Fq 'trusted-address allowlist' <<<"$all"
grep -Fq 'activate certbot.timer after core commit' <<<"$all"
grep -Fq 'Node.js versions 20.18.0,22.14.0' <<<"$all"

activated=$(install_print_optional_change_plan caddy 0 0 DetectionOnly 1 1 1 1 0 0 0 0 20.18.0)
grep -Fq 'ACTIVATE Exim, Dovecot, and spam filtering' <<<"$activated"
grep -Fq 'ACTIVATE vsftpd with FTPS' <<<"$activated"
if grep -Fq 'Certbot' <<<"$activated"; then
  echo 'Caddy change plan incorrectly advertises Apache Certbot activation' >&2
  exit 1
fi
apache_only=$(install_print_optional_change_plan caddy 1 0 DetectionOnly 0 0 0 0 0 0 0 0 20.18.0)
grep -Fq 'no web route is configured for caddy' <<<"$apache_only"

none=$(install_print_optional_change_plan caddy 0 0 DetectionOnly 0 0 0 0 0 0 0 0 20.18.0)
[[ $none == '  No optional integrations selected' ]]

source_line=$(grep -Fn ". \"\$ROOT_DIR/deploy/lib/install-change-plan.sh\"" "$ROOT_DIR/install.sh" | cut -d: -f1)
preview_line=$(grep -n 'install_print_optional_change_plan' "$ROOT_DIR/install.sh" | cut -d: -f1)
mutation_line=$(grep -n 'apt-get update; apt-get install' "$ROOT_DIR/install.sh" | cut -d: -f1)
(( source_line < preview_line && preview_line < mutation_line ))

echo 'installer change-plan contract passed'
