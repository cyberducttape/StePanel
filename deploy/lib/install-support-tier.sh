#!/usr/bin/env bash

# Print the evidence tier for a requested install tuple. This is intentionally
# advisory: package-manager support does not imply installed-host certification.
install_support_tier() {
  local os_id=${1,,} os_version=$2 web_server=${3,,} db_engine=${4,,}
  local platform="${os_id} ${os_version}"

  case "$os_id:$os_version:$web_server:$db_engine" in
    ubuntu:24.04:caddy:mariadb)
      printf '%s\n' "RECOMMENDED PROFILE: Ubuntu 24.04 + Caddy + MariaDB (current-code host smoke-tested; 1 recovery iteration)."
      ;;
    ubuntu:24.04:apache:mariadb|almalinux:9:caddy:mariadb|almalinux:9:apache:mariadb|rocky:9:caddy:mariadb|rocky:9:apache:mariadb)
      printf '%s\n' "SUPPORT TIER: ${platform} + ${web_server} + MariaDB is in the current-code Operator Beta host-smoke matrix (1 recovery iteration)."
      ;;
    *)
      printf '%s\n' "SUPPORT TIER: ${platform} + ${web_server} + ${db_engine} is outside the current six-combination installed-host smoke matrix."
      printf '%s\n' "This installer may support the packages, but this combination needs operator validation; review docs/INSTALLATION.md before proceeding."
      ;;
  esac
  printf '%s\n' "RELEASE STATUS: Operator Beta; smoke results are not production certification. See docs/V1_PRODUCTION_GATES.md."
}
