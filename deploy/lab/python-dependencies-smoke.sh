#!/usr/bin/env bash
set -Eeuo pipefail

lockfile=${1:-deploy/python/gunicorn-23.0.0.requirements.txt}
command -v python3 >/dev/null || { echo 'Python dependency smoke requires python3' >&2; exit 77; }
[[ -f $lockfile && ! -L $lockfile ]] || { echo "Python dependency lockfile is unavailable: $lockfile" >&2; exit 1; }
# Debian and Ubuntu ship venv support separately (python3.X-venv); install it
# as an operator running Python applications would. RHEL includes it.
if ! python3 -c 'import ensurepip' >/dev/null 2>&1 && command -v apt-get >/dev/null 2>&1; then
  version=$(python3 -c 'import sys; print(f"{sys.version_info[0]}.{sys.version_info[1]}")')
  apt-get update >/dev/null
  DEBIAN_FRONTEND=noninteractive apt-get install -y "python${version}-venv" >/dev/null
fi
tmp=$(mktemp -d /tmp/stepanel-python-deps.XXXXXX)
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT
python3 -m venv "$tmp/venv"
"$tmp/venv/bin/pip" install --quiet --disable-pip-version-check --no-cache-dir --require-hashes -r "$lockfile"
"$tmp/venv/bin/gunicorn" --version
