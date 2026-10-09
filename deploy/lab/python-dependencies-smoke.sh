#!/usr/bin/env bash
set -Eeuo pipefail

lockfile=${1:-deploy/python/gunicorn-23.0.0.requirements.txt}
command -v python3 >/dev/null || { echo 'Python dependency smoke requires python3' >&2; exit 77; }
[[ -f $lockfile && ! -L $lockfile ]] || { echo "Python dependency lockfile is unavailable: $lockfile" >&2; exit 1; }
tmp=$(mktemp -d /tmp/stepanel-python-deps.XXXXXX)
cleanup() { rm -rf -- "$tmp"; }
trap cleanup EXIT
python3 -m venv "$tmp/venv"
"$tmp/venv/bin/pip" install --quiet --disable-pip-version-check --no-cache-dir --require-hashes -r "$lockfile"
"$tmp/venv/bin/gunicorn" --version
