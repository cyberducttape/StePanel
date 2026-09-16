#!/usr/bin/env bash
set -Eeuo pipefail

if [[ $EUID -ne 0 ]]; then echo 'Run as root.' >&2; exit 1; fi
APP_USER=${1:-stepanel}
NVM_DIR=${2:-/opt/stepanel/.nvm}
VERSIONS=${3:-20.18.0}
NVM_COMMIT='179d45050be0a71fd57591b0ed8aedf9b177ba10'

id "$APP_USER" >/dev/null 2>&1 || { echo "user not found: $APP_USER" >&2; exit 1; }
# 0755 (not 0750): isolated per-site identities (sp-*) are not members of
# $APP_USER's group, so they need read/traverse access on the shared Node
# runtime to source nvm.sh and run node/npm. Only $APP_USER can write here.
install -d -m 0755 -o "$APP_USER" -g "$APP_USER" "$NVM_DIR"
if [[ ! -s "$NVM_DIR/nvm.sh" ]]; then
  installer=$(mktemp)
  trap 'rm -f "$installer"' EXIT
  curl --proto '=https' --tlsv1.2 -fsSL "https://raw.githubusercontent.com/nvm-sh/nvm/$NVM_COMMIT/install.sh" -o "$installer"
  runuser -u "$APP_USER" -- env NVM_DIR="$NVM_DIR" bash "$installer"
fi
for version in ${VERSIONS//,/ }; do
  [[ $version =~ ^v?[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "invalid Node version: $version" >&2; exit 1; }
  runuser -u "$APP_USER" -- env NVM_DIR="$NVM_DIR" bash -c '. "$NVM_DIR/nvm.sh"; nvm install "$1"; nvm alias default "$1"' _ "$version"
done
