#!/usr/bin/env bash

# Print the evidence tier for a requested install tuple. This is intentionally
# advisory: package-manager support does not imply installed-host certification.
install_support_tier() {
  local os_id=${1,,} os_version=$2 web_server=${3,,} db_engine=${4,,}
  local platform="${os_id} ${os_version}"

  case "$os_id:$os_version:$web_server:$db_engine" in
    ubuntu:24.04:caddy:mariadb)
      printf '%s\n' "RECOMMENDED BETA PROFILE: Ubuntu 24.04 + Caddy + MariaDB (current-code host smoke-tested; 1 recovery iteration; repeated acceptance evidence is still pending)."
      ;;
    ubuntu:24.04:apache:mariadb|almalinux:9:caddy:mariadb|almalinux:9:apache:mariadb|rocky:9:caddy:mariadb|rocky:9:apache:mariadb)
      printf '%s\n' "BETA PROFILE — REPEATED ACCEPTANCE PENDING: ${platform} + ${web_server} + MariaDB passed 1 current-code host-smoke recovery iteration. This is not yet in the recommended first-install tier; repeat certification evidence is required."
      ;;
    *)
      printf '%s\n' "SUPPORT TIER: ${platform} + ${web_server} + ${db_engine} is outside the current six-combination installed-host smoke matrix."
      printf '%s\n' "This installer may support the packages, but this combination needs operator validation; review docs/INSTALLATION.md before proceeding."
      ;;
  esac
  printf '%s\n' "RELEASE STATUS: Operator Beta; smoke results are not production certification. See docs/V1_PRODUCTION_GATES.md."
}

# Show the recommended profile for the current host before guided setup asks
# for remote-storage credentials or administrator secrets. The exact selected
# web/database combination is still checked again during the normal preflight.
install_guided_host_support() {
  local os_release=${1:-/etc/os-release}
  local ID= VERSION_ID= PRETTY_NAME=
  if [[ ! -r $os_release ]]; then
    echo "Cannot identify the operating system: $os_release is unreadable." >&2
    return 1
  fi
  # shellcheck source=/etc/os-release
  if ! source "$os_release"; then
    echo "Cannot read operating-system identity from $os_release." >&2
    return 1
  fi
  if [[ -z $ID || -z $VERSION_ID ]]; then
    echo "Operating-system identity in $os_release must define ID and VERSION_ID." >&2
    return 1
  fi
  if ! command -v apt-get >/dev/null 2>&1 && ! command -v dnf >/dev/null 2>&1; then
    echo "Unsupported package manager on $PRETTY_NAME ($ID $VERSION_ID); guided install supports apt-get or dnf hosts." >&2
    return 1
  fi
  printf 'HOST DETECTED: %s (%s %s)\n' "${PRETTY_NAME:-Linux}" "$ID" "$VERSION_ID"
  install_support_tier "$ID" "$VERSION_ID" caddy mariadb
}
