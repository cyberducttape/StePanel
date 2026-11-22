#!/usr/bin/env bash
set -Eeuo pipefail

# Build StePanel as the checkout owner, then elevate only for the native host
# installer. This keeps the Go toolchain and module cache out of the root
# account while making the supported source-based guided path one command.
if (( EUID == 0 )); then
  echo 'Run this command as your normal user; it invokes sudo only for install.sh.' >&2
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

go_bin=${GO:-go}
sudo_bin=${SUDO:-sudo}
if [[ ${GOTOOLCHAIN:-auto} == local ]]; then
  echo 'GOTOOLCHAIN=local disables the pinned Go toolchain download; run `unset GOTOOLCHAIN` and try again.' >&2
  exit 1
fi
command -v -- "$go_bin" >/dev/null 2>&1 || {
  echo 'Go is not installed. The bootstrap needs Go 1.21 or later; it will download the pinned Go 1.26.11 toolchain automatically.' >&2
  echo 'On the recommended Ubuntu 24.04 profile, install the prerequisites with:' >&2
  echo '  sudo apt-get update && sudo apt-get install -y git golang-go' >&2
  echo 'For other systems, see the prerequisites in docs/INSTALLATION.md.' >&2
  exit 1
}
go_version=$("$go_bin" version 2>/dev/null || true)
if [[ $go_version =~ go([0-9]+)\.([0-9]+) ]]; then
  go_version_found=${BASH_REMATCH[0]}
  go_major=${BASH_REMATCH[1]}
  go_minor=${BASH_REMATCH[2]}
  if (( go_major < 1 || (go_major == 1 && go_minor < 21) )); then
    echo "Go 1.21 or later is required for automatic toolchain selection; found $go_version_found" >&2
    echo 'On the recommended Ubuntu 24.04 profile, install the prerequisites with:' >&2
    echo '  sudo apt-get update && sudo apt-get install -y git golang-go' >&2
    echo 'For other systems, see the prerequisites in docs/INSTALLATION.md.' >&2
    exit 1
  fi
else
  echo "Could not determine the installed Go version from: ${go_version:-<no output>}" >&2
  echo 'Install Go 1.21 or later, then retry the guided install.' >&2
  exit 1
fi
command -v -- "$sudo_bin" >/dev/null 2>&1 || {
  echo 'sudo is required to configure host services.' >&2
  exit 1
}

build_dir=$(mktemp -d "${TMPDIR:-/tmp}/stepanel-guided-build.XXXXXX")
panel_tmp=
root_tmp=
cleanup() {
  [[ -z $panel_tmp ]] || rm -f -- "$panel_tmp"
  [[ -z $root_tmp ]] || rm -f -- "$root_tmp"
  rm -rf -- "$build_dir"
}
trap cleanup EXIT

echo 'Building StePanel with the repository-pinned Go toolchain as your user...'
(
  cd "$repo_root"
  "$go_bin" build -trimpath -o "$build_dir/stepanel" ./cmd/stepanel
  "$go_bin" build -trimpath -o "$build_dir/stepanel-root" ./cmd/stepanel-root
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

echo 'Build complete. Starting the guided native installer.'
cd "$repo_root"
"$sudo_bin" ./install.sh --guided "${takeover[@]}"
