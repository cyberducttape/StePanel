#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../deploy/lib/install-database-version-preflight.sh
. "$ROOT_DIR/deploy/lib/install-database-version-preflight.sh"
version_check_line=$(grep -n 'install_preflight_database_version "\$PKG"' "$ROOT_DIR/install.sh" | cut -d: -f1)
package_mutation_line=$(grep -n 'apt_update; apt-get install' "$ROOT_DIR/install.sh" | cut -d: -f1)
(( version_check_line < package_mutation_line ))

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin"
cat > "$tmp/bin/apt-cache" <<'EOF'
#!/usr/bin/env bash
if [[ $1 == madison && $2 == mariadb-server ]]; then
  printf '%s\n' ' mariadb-server | 1:10.11.8-0ubuntu0 | repo'
  exit 0
fi
exit 1
EOF
cat > "$tmp/bin/dnf" <<'EOF'
#!/usr/bin/env bash
if [[ $1 == module && $2 == list && $3 == postgresql ]]; then
  printf '%s\n' 'postgresql 16 [d] default'
  exit 0
fi
if [[ $1 == --assumeno && $2 == install && $3 == mariadb-server-10.11 ]]; then
  exit 0
fi
exit 1
EOF
chmod 0700 "$tmp/bin/apt-cache" "$tmp/bin/dnf"
PATH="$tmp/bin:$PATH"
export PATH

grep -Fq 'package-manager default selected' \
  <<<"$(install_preflight_database_version apt mariadb mariadb-server default)"
grep -Fq 'available in APT metadata' \
  <<<"$(install_preflight_database_version apt mariadb mariadb-server 1:10.11.8-0ubuntu0)"
if message=$(install_preflight_database_version apt mariadb mariadb-server 99.99 2>&1); then
  echo 'unavailable APT version was accepted' >&2
  exit 1
fi
grep -Fq 'run apt-get update' <<<"$message"
if message=$(install_preflight_database_version apt postgresql postgresql 16 2>&1); then
  echo 'unsupported explicit APT PostgreSQL version was accepted' >&2
  exit 1
fi
grep -Fq 'versioned PostgreSQL repository' <<<"$message"
grep -Fq 'available in DNF metadata' \
  <<<"$(install_preflight_database_version dnf mariadb mariadb-server 10.11)"
grep -Fq 'AppStream stream 16 is available' \
  <<<"$(install_preflight_database_version dnf postgresql postgresql-server 16)"
if message=$(install_preflight_database_version dnf postgresql postgresql-server 99 2>&1); then
  echo 'unavailable PostgreSQL AppStream stream was accepted' >&2
  exit 1
fi
grep -Fq 'enable a repository' <<<"$message"

echo 'installer database-version preflight contract passed'
