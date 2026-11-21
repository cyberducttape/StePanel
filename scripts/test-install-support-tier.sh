#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../deploy/lib/install-support-tier.sh
. "$ROOT_DIR/deploy/lib/install-support-tier.sh"

recommended=$(install_support_tier ubuntu 24.04 caddy mariadb)
grep -Fq 'RECOMMENDED PROFILE: Ubuntu 24.04 + Caddy + MariaDB' <<<"$recommended"
grep -Fq '1 recovery iteration' <<<"$recommended"
grep -Fq 'not production certification' <<<"$recommended"

for os in 'ubuntu 24.04' 'almalinux 9' 'rocky 9'; do
  read -r id version <<<"$os"
  for web in apache caddy; do
    result=$(install_support_tier "$id" "$version" "$web" mariadb)
    if [[ $id == ubuntu && $version == 24.04 && $web == caddy ]]; then
      grep -Fq 'RECOMMENDED PROFILE:' <<<"$result"
    else
      grep -Fq 'current-code Operator Beta host-smoke matrix' <<<"$result"
    fi || {
      echo "expected smoke-matrix classification for $os + $web + MariaDB" >&2
      exit 1
    }
  done
done

for tuple in \
  'ubuntu 24.04 caddy postgresql' \
  'ubuntu 24.04 openlitespeed mariadb' \
  'debian 12 caddy mariadb'; do
  read -r id version web db <<<"$tuple"
  result=$(install_support_tier "$id" "$version" "$web" "$db")
  grep -Fq 'outside the current six-combination installed-host smoke matrix' <<<"$result"
  grep -Fq 'needs operator validation' <<<"$result"
done

echo 'installer support-tier contract passed'
