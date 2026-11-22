#!/usr/bin/env bash

# Source this file and call stepanel_prepare_go REPO_ROOT WORK_DIR before
# invoking stepanel_go. A bootstrapped compiler lives only inside WORK_DIR.
stepanel_prepare_go() {
  local repo_root=$1 work_dir=$2
  local go_bin=${GO:-go} go_version go_version_found go_major go_minor
  local toolchain_version go_arch go_sha256 go_archive go_url

  STEPANEL_GO_BIN=$go_bin
  STEPANEL_GO_LOCAL=0
  toolchain_version=$(sed -n 's/^toolchain \(go[0-9][0-9.]*\)$/\1/p' "$repo_root/go.mod")
  [[ $toolchain_version =~ ^go[0-9]+\.[0-9]+\.[0-9]+$ ]] || {
    echo 'Could not read a supported Go toolchain pin from go.mod.' >&2
    return 1
  }

  go_version_found=
  if command -v -- "$go_bin" >/dev/null 2>&1; then
    go_version=$("$go_bin" version 2>/dev/null || true)
    if [[ $go_version =~ go([0-9]+)\.([0-9]+) ]]; then
      go_version_found=${BASH_REMATCH[0]}
      go_major=${BASH_REMATCH[1]}
      go_minor=${BASH_REMATCH[2]}
      if (( go_major > 1 || (go_major == 1 && go_minor >= 21) )) &&
        [[ ${GOTOOLCHAIN:-auto} != local ]]; then
        return 0
      fi
      echo "Found $go_version_found; bootstrapping the repository-pinned $toolchain_version locally..."
    else
      echo "Could not identify the installed Go version; bootstrapping $toolchain_version locally..."
    fi
  else
    echo "Go is not installed; bootstrapping the repository-pinned $toolchain_version locally..."
  fi

  [[ $(uname -s) == Linux ]] || {
    echo 'Automatic Go bootstrap is currently supported on Linux only.' >&2
    return 1
  }
  case "$(uname -m)" in
    x86_64|amd64) go_arch=amd64 ;;
    aarch64|arm64) go_arch=arm64 ;;
    *)
      echo "Automatic Go bootstrap supports Linux amd64 and arm64; found $(uname -m)." >&2
      echo 'Install Go 1.21 or later from your distribution, then retry.' >&2
      return 1
      ;;
  esac
  case "$toolchain_version:$go_arch" in
    go1.26.11:amd64) go_sha256=9849f4e04ee35303714e34fbc3ac8ab44ea3e448a95aa182f91e3b9b79c710ec ;;
    go1.26.11:arm64) go_sha256=6c2f29b458b5add0835f18b776f69c8881a6d88428f1484a6da55755e620757e ;;
    *)
      echo "No verified Go archive checksum is configured for $toolchain_version linux/$go_arch." >&2
      echo 'Update the bootstrap checksum map before changing the repository toolchain pin.' >&2
      return 1
      ;;
  esac
  if ! command -v sha256sum >/dev/null 2>&1 || ! command -v tar >/dev/null 2>&1; then
    echo 'The Go bootstrap requires sha256sum and tar. Install coreutils and tar, then retry.' >&2
    return 1
  fi

  go_archive="$work_dir/$toolchain_version.linux-$go_arch.tar.gz"
  go_url="https://go.dev/dl/$toolchain_version.linux-$go_arch.tar.gz"
  echo "Downloading $toolchain_version for Linux $go_arch (temporary, SHA-256 verified)..."
  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --show-error --progress-bar --retry 3 --connect-timeout 20 --max-time 600 --output "$go_archive" "$go_url"
  elif command -v wget >/dev/null 2>&1; then
    wget --tries=3 --timeout=600 --output-document="$go_archive" "$go_url"
  else
    echo 'The Go bootstrap needs curl or wget to download the pinned compiler.' >&2
    echo 'On Ubuntu 24.04, install curl with: sudo apt-get install -y curl' >&2
    return 1
  fi
  if ! printf '%s  %s\n' "$go_sha256" "$go_archive" | sha256sum --check --status; then
    echo "The downloaded $toolchain_version archive failed its SHA-256 check; it was not extracted." >&2
    return 1
  fi
  tar -xzf "$go_archive" -C "$work_dir"
  STEPANEL_GO_BIN="$work_dir/go/bin/go"
  [[ -x $STEPANEL_GO_BIN ]] || {
    echo 'The verified Go archive did not contain the expected compiler.' >&2
    return 1
  }
  go_version=$("$STEPANEL_GO_BIN" version 2>/dev/null || true)
  [[ $go_version == *"$toolchain_version"* ]] || {
    echo "The bootstrapped compiler did not report $toolchain_version: ${go_version:-<no output>}" >&2
    return 1
  }
  STEPANEL_GO_LOCAL=1
  echo "Verified $toolchain_version ($go_arch) in the temporary build directory."
}

stepanel_go() {
  if (( STEPANEL_GO_LOCAL )); then
    GOTOOLCHAIN=local "$STEPANEL_GO_BIN" "$@"
  else
    "$STEPANEL_GO_BIN" "$@"
  fi
}
