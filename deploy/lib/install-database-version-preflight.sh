#!/usr/bin/env bash

# Validate an explicit database version against current repository metadata
# before the installer changes packages, services, or host configuration.
install_preflight_database_version() {
  local package_manager=${1:?package manager required}
  local engine=${2:?database engine required}
  local package=${3:?database package required}
  local version=${4:?database version required}
  local versions

  [[ $version != default ]] || {
    printf 'database package-manager default selected; no explicit version check needed.'
    return 0
  }

  if [[ $package_manager == apt ]]; then
    if [[ $engine == postgresql ]]; then
      printf 'explicit PostgreSQL versions on Debian/Ubuntu require a configured versioned PostgreSQL repository; select the package-manager default or use a supported RHEL-family AppStream stream.'
      return 1
    fi
    if ! versions=$(apt-cache madison "$package" 2>/dev/null | awk '{print $3}'); then
      printf 'could not read APT version metadata for %s; run apt-get update and rerun preflight.' "$package"
      return 1
    fi
    if ! grep -Fxq -- "$version" <<<"$versions"; then
      printf 'requested %s version %s is not available in current APT metadata; configure its repository, run apt-get update, and rerun preflight.' "$package" "$version"
      return 1
    fi
    printf 'requested %s version %s is available in APT metadata.' "$package" "$version"
    return 0
  fi

  if [[ $engine == postgresql ]]; then
    if ! versions=$(dnf module list postgresql --all 2>&1); then
      printf 'PostgreSQL AppStream metadata is unavailable; enable the appropriate RHEL-family repositories and rerun preflight.'
      return 1
    fi
    if ! awk -v requested="$version" '$1 == "postgresql" && $2 == requested {found=1} END {exit(found ? 0 : 1)}' <<<"$versions"; then
      printf 'requested PostgreSQL AppStream stream %s is unavailable; enable a repository that provides it and rerun preflight.' "$version"
      return 1
    fi
    printf 'requested PostgreSQL AppStream stream %s is available.' "$version"
    return 0
  fi

  if ! dnf --assumeno install "$package-$version" >/dev/null 2>&1; then
    printf 'requested %s version %s is unavailable; enable the appropriate DNF repository/module and rerun preflight.' "$package" "$version"
    return 1
  fi
  printf 'requested %s version %s is available in DNF metadata.' "$package" "$version"
}
