#!/usr/bin/env bash
set -Eeuo pipefail

# Build StePanel as the checkout owner, then use sudo for the read-only host
# preflight and privileged install or upgrade. This keeps the Go toolchain and
# module cache out of the root account while keeping source setup one command.
if (( EUID == 0 )); then
  echo 'Run this command as your normal user; it uses sudo for host checks and installation.' >&2
  exit 1
fi

takeover=()
for arg in "$@"; do
  case "$arg" in
    --take-over-host) takeover+=(--take-over-host) ;;
    *) echo "usage: $0 [--take-over-host]" >&2; exit 64 ;;
  esac
done

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
[[ -f "$repo_root/go.mod" && -f "$repo_root/install.sh" ]] || {
  echo 'Run this command from a StePanel source checkout.' >&2
  exit 1
}

sudo_bin=${SUDO:-sudo}
command -v -- "$sudo_bin" >/dev/null 2>&1 || {
  echo 'sudo is required to configure host services.' >&2
  exit 1
}

existing_install=0
if "$sudo_bin" test -f /etc/ste-panel.env; then
  existing_install=1
  echo 'Existing StePanel installation detected; preparing the standard upgrade path.'
else
  # Fail before downloading a compiler or building binaries when this host
  # cannot run a fresh native installation. The check is read-only; install.sh
  # repeats it before making any host changes.
  "$sudo_bin" bash "$repo_root/scripts/install-host-preflight.sh"
fi

build_dir=$(mktemp -d "${TMPDIR:-/tmp}/stepanel-guided-build.XXXXXX")
panel_tmp=
root_tmp=
cleanup() {
  [[ -z $panel_tmp ]] || rm -f -- "$panel_tmp"
  [[ -z $root_tmp ]] || rm -f -- "$root_tmp"
  rm -rf -- "$build_dir"
}
trap cleanup EXIT

. "$repo_root/scripts/go-toolchain.sh"
stepanel_prepare_go "$repo_root" "$build_dir"

run_go() {
  stepanel_go "$@"
}

echo 'Building StePanel with the repository-pinned Go toolchain as your user...'
(
  cd "$repo_root"
  run_go build -trimpath -o "$build_dir/stepanel" ./cmd/stepanel
  run_go build -trimpath -o "$build_dir/stepanel-root" ./cmd/stepanel-root
)

# Publish each completed binary atomically into the checkout. The installer
# expects both at its root and uses the helper and asset files beside them.
panel_tmp=$(mktemp "$repo_root/.stepanel.XXXXXX")
root_tmp=$(mktemp "$repo_root/.stepanel-root.XXXXXX")
install -m 0755 -- "$build_dir/stepanel" "$panel_tmp"
install -m 0755 -- "$build_dir/stepanel-root" "$root_tmp"
mv -f -- "$panel_tmp" "$repo_root/stepanel"
panel_tmp=
mv -f -- "$root_tmp" "$repo_root/stepanel-root"
root_tmp=

cd "$repo_root"
if (( existing_install )); then
  echo 'Build complete. Starting the native upgrade.'
  "$sudo_bin" ./install.sh
else
  echo 'Build complete. Starting the guided native installer.'
  "$sudo_bin" ./install.sh --guided "${takeover[@]}"
fi
