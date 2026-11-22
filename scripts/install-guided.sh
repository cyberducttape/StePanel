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
command -v -- "$sudo_bin" >/dev/null 2>&1 || {
  echo 'sudo is required to configure host services.' >&2
  exit 1
}

build_dir=$(mktemp -d "${TMPDIR:-/tmp}/stepanel-guided-build.XXXXXX")
panel_tmp=
root_tmp=
use_local_toolchain=0
cleanup() {
  [[ -z $panel_tmp ]] || rm -f -- "$panel_tmp"
  [[ -z $root_tmp ]] || rm -f -- "$root_tmp"
  rm -rf -- "$build_dir"
}
trap cleanup EXIT

toolchain_version=go1.26.11
toolchain_go_version=
if command -v -- "$go_bin" >/dev/null 2>&1; then
  go_version=$("$go_bin" version 2>/dev/null || true)
  if [[ $go_version =~ go([0-9]+)\.([0-9]+) ]]; then
    go_version_found=${BASH_REMATCH[0]}
    go_major=${BASH_REMATCH[1]}
    go_minor=${BASH_REMATCH[2]}
    if (( go_major > 1 || (go_major == 1 && go_minor >= 21) )); then
      toolchain_go_version=$go_version_found
    else
      echo "Found $go_version_found; bootstrapping the repository-pinned $toolchain_version locally..."
    fi
  else
    echo "Could not identify the installed Go version; bootstrapping $toolchain_version locally..."
  fi
else
  echo "Go is not installed; bootstrapping the repository-pinned $toolchain_version locally..."
fi

if [[ -z $toolchain_go_version || ${GOTOOLCHAIN:-auto} == local ]]; then
  [[ $(uname -s) == Linux ]] || {
    echo 'Automatic Go bootstrap is currently supported on Linux only.' >&2
    exit 1
  }
  go_arch=
  go_sha256=
  case "$(uname -m)" in
    x86_64|amd64)
      go_arch=amd64
      go_sha256=9849f4e04ee35303714e34fbc3ac8ab44ea3e448a95aa182f91e3b9b79c710ec
      ;;
    aarch64|arm64)
      go_arch=arm64
      go_sha256=6c2f29b458b5add0835f18b776f69c8881a6d88428f1484a6da55755e620757e
      ;;
    *)
      echo "Automatic Go bootstrap supports Linux amd64 and arm64; found $(uname -m)." >&2
      echo 'Install Go 1.21 or later from your distribution, then retry the guided install.' >&2
      exit 1
      ;;
  esac
  if ! command -v sha256sum >/dev/null 2>&1 || ! command -v tar >/dev/null 2>&1; then
    echo 'The Go bootstrap requires sha256sum and tar. Install coreutils and tar, then retry.' >&2
    exit 1
  fi
  go_archive="$build_dir/$toolchain_version.linux-$go_arch.tar.gz"
  go_url="https://go.dev/dl/$toolchain_version.linux-$go_arch.tar.gz"
  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --silent --show-error --retry 3 --connect-timeout 20 --max-time 600 --output "$go_archive" "$go_url"
  elif command -v wget >/dev/null 2>&1; then
    wget --tries=3 --timeout=600 --output-document="$go_archive" "$go_url"
  else
    echo 'The Go bootstrap needs curl or wget to download the pinned compiler.' >&2
    echo 'On Ubuntu 24.04, install curl with: sudo apt-get install -y curl' >&2
    exit 1
  fi
  if ! printf '%s  %s\n' "$go_sha256" "$go_archive" | sha256sum --check --status; then
    echo "The downloaded $toolchain_version archive failed its SHA-256 check; it was not extracted." >&2
    exit 1
  fi
  tar -xzf "$go_archive" -C "$build_dir"
  go_bin="$build_dir/go/bin/go"
  [[ -x $go_bin ]] || { echo 'The verified Go archive did not contain the expected compiler.' >&2; exit 1; }
  toolchain_go_version=$("$go_bin" version 2>/dev/null || true)
  [[ $toolchain_go_version == *"$toolchain_version"* ]] || {
    echo "The bootstrapped compiler did not report $toolchain_version: ${toolchain_go_version:-<no output>}" >&2
    exit 1
  }
  use_local_toolchain=1
  echo "Verified $toolchain_version ($go_arch) in the temporary build directory."
fi

run_go() {
  if (( use_local_toolchain )); then
    GOTOOLCHAIN=local "$go_bin" "$@"
  else
    "$go_bin" "$@"
  fi
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

echo 'Build complete. Starting the guided native installer.'
cd "$repo_root"
"$sudo_bin" ./install.sh --guided "${takeover[@]}"
