#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../deploy/lib/install-support-tier.sh
. "$ROOT_DIR/deploy/lib/install-support-tier.sh"

# Every setup entry point must use the same evidence-backed database default.
grep -Fq 'Database engine [mariadb/mysql/postgresql] (mariadb)' "$ROOT_DIR/install.sh"
grep -Fq 'DB_ENGINE="${DB_ENGINE:-mariadb}"' "$ROOT_DIR/install.sh"

recommended=$(install_support_tier ubuntu 24.04 caddy mariadb)
grep -Fq 'RECOMMENDED BETA PROFILE: Ubuntu 24.04 + Caddy + MariaDB' <<<"$recommended"
grep -Fq '1 recovery iteration' <<<"$recommended"
grep -Fq 'repeated acceptance evidence is still pending' <<<"$recommended"
grep -Fq 'not production certification' <<<"$recommended"

for os in 'ubuntu 24.04' 'almalinux 9' 'rocky 9'; do
  read -r id version <<<"$os"
  for web in apache caddy; do
    result=$(install_support_tier "$id" "$version" "$web" mariadb)
    if [[ $id == ubuntu && $version == 24.04 && $web == caddy ]]; then
      grep -Fq 'RECOMMENDED BETA PROFILE:' <<<"$result"
    else
      grep -Fq 'BETA PROFILE — REPEATED ACCEPTANCE PENDING:' <<<"$result"
      grep -Fq 'not yet in the recommended first-install tier' <<<"$result"
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

fixture=$(mktemp)
cat >"$fixture" <<'EOF'
ID=ubuntu
VERSION_ID=24.04
PRETTY_NAME="Ubuntu 24.04 LTS"
EOF
early=$(install_guided_host_support "$fixture")
grep -Fq 'HOST DETECTED: Ubuntu 24.04 LTS (ubuntu 24.04)' <<<"$early"
grep -Fq 'RECOMMENDED BETA PROFILE: Ubuntu 24.04 + Caddy + MariaDB' <<<"$early"
rm -f "$fixture"

# The current host/profile check must precede setup's questions and secrets.
host_check_line=$(grep -n 'install_guided_host_support$' "$ROOT_DIR/install.sh" | head -1 | cut -d: -f1)
installed_guard_line=$(grep -n 'StePanel is already installed on this server' "$ROOT_DIR/install.sh" | head -1 | cut -d: -f1)
native_preflight_line=$(grep -n 'Checking native host requirements before asking setup questions' "$ROOT_DIR/install.sh" | head -1 | cut -d: -f1)
setup_line=$(grep -n '"$ROOT_DIR/stepanel" setup --output' "$ROOT_DIR/install.sh" | head -1 | cut -d: -f1)
[[ -n $host_check_line && -n $installed_guard_line && -n $native_preflight_line && -n $setup_line && \
  $host_check_line -lt $installed_guard_line && $installed_guard_line -lt $native_preflight_line && \
  $native_preflight_line -lt $setup_line ]] || {
  echo 'guided mode must show support, direct installed hosts to upgrade, and check prerequisites before setup collects answers' >&2
  exit 1
}

echo 'installer support-tier contract passed'
