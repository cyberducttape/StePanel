#!/usr/bin/env bash

# checker is the package-manager-specific availability function. Keep the
# whole required set in one preflight result so missing PHP/database/runtime
# packages are reported together before any package is installed.
install_preflight_packages() {
  local checker=${1:?package availability checker required} package
  shift
  local -a missing=()
  for package in "$@"; do
    if ! "$checker" "$package"; then
      missing+=("$package")
    fi
  done
  if (( ${#missing[@]} )); then
    printf 'required packages are unavailable in the current repository metadata: %s. Refresh package indexes or enable the repository that provides them, then rerun preflight.' "${missing[*]}"
    return 1
  fi
  printf 'all %d required base, PHP, database, and web-server packages are available.' "$#"
}
