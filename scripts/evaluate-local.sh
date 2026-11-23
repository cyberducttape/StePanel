#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
if (( EUID == 0 )); then
  echo 'Local evaluation must not run as root. Run bash scripts/evaluate-local.sh as your normal user.' >&2
  exit 1
fi
[[ -f $ROOT_DIR/go.mod ]] || {
  echo 'Run local evaluation from a StePanel source checkout.' >&2
  exit 1
}

build_dir=$(mktemp -d "${TMPDIR:-/tmp}/stepanel-evaluate-build.XXXXXX")
cleanup() { rm -rf -- "$build_dir"; }
trap cleanup EXIT

. "$ROOT_DIR/scripts/go-toolchain.sh"
stepanel_prepare_go "$ROOT_DIR" "$build_dir"

build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
commit=${GIT_COMMIT:-dev}
ldflags="-s -w -X github.com/cyberducttape/StePanel.Commit=$commit -X github.com/cyberducttape/StePanel.BuildDate=$build_date"
echo 'Building the local evaluation binary as your user...'
(
  cd "$ROOT_DIR"
  stepanel_go build -trimpath -ldflags="$ldflags" -o "$build_dir/stepanel" ./cmd/stepanel
)
bash "$ROOT_DIR/scripts/evaluate.sh" "$build_dir/stepanel"
