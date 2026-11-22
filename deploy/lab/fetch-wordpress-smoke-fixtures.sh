#!/usr/bin/env bash
# Fetch the pinned application fixtures before a disposable host is mutated.
set -Eeuo pipefail

[[ $# == 1 && -n $1 ]] || {
  echo "usage: $0 FIXTURE_DIRECTORY" >&2
  exit 64
}

destination=$1
mkdir -p -- "$destination"
[[ -d $destination && ! -L $destination ]] || {
  echo 'fixture destination must be a real directory' >&2
  exit 1
}
temporary=$(mktemp -d "${destination%/}/.wordpress-fixtures.XXXXXX")
cleanup() { rm -rf -- "$temporary"; }
trap cleanup EXIT

fetch() {
  local name=$1 url=$2 digest=$3 file="$temporary/$1"
  curl --fail --silent --show-error --location \
    --retry 5 --retry-all-errors --retry-delay 2 \
    --connect-timeout 15 --max-time 180 \
    "$url" --output "$file"
  printf '%s  %s\n' "$digest" "$file" | sha256sum --check --status || {
    echo "downloaded WordPress smoke fixture failed SHA-256 verification: $name" >&2
    exit 1
  }
  chmod 0644 "$file"
}

fetch wordpress-6.8.8.tar.gz \
  https://wordpress.org/wordpress-6.8.8.tar.gz \
  5388e7a8fa462c32e41ce5cabb796e2228cd187b6b80d15d069a8d5e6ecd3e05
fetch wp-cli-2.12.0.phar \
  https://github.com/wp-cli/wp-cli/releases/download/v2.12.0/wp-cli-2.12.0.phar \
  ce34ddd838f7351d6759068d09793f26755463b4a4610a5a5c0a97b68220d85c

for name in wordpress-6.8.8.tar.gz wp-cli-2.12.0.phar; do
  install -m 0644 -- "$temporary/$name" "$destination/$name"
done
echo "Pinned WordPress recovery fixtures are ready in $destination"
