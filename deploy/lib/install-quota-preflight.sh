#!/usr/bin/env bash

# Verify mandatory user-quota prerequisites without changing mount or quota
# state. The installer repeats this after package installation to catch a
# concurrent host change before it creates hosted accounts.
install_preflight_user_quotas() {
  local web_root=${1:-/var/www} home_path=${2:-/home} home_mount quota_error deferred=0
  if ! command -v findmnt >/dev/null 2>&1; then
    printf '%s' 'findmnt is required to inspect hosting filesystem quotas; install util-linux and rerun preflight.'
    return 1
  fi
  if ! quota_error=$(stepanel_user_quota_active "$web_root" 2>&1); then
    if [[ $quota_error == *'quotaon is required'* || $quota_error == *'xfs_quota is required'* ]]; then
      printf 'quota mount options are present on %s, but enforcement cannot be inspected until the installer installs quota tools: %s. Enforcement will be checked again before setup continues. ' "$web_root" "$quota_error"
      deferred=1
    else
      printf 'user quota enforcement is not ready on %s: %s. Enable and verify user quotas on this filesystem, then rerun preflight. See docs/INSTALLATION.md#prepare-filesystem-quotas.' "$web_root" "$quota_error"
      return 1
    fi
  fi
  if ! home_mount=$(findmnt -no TARGET -T "$home_path" 2>/dev/null) || [[ -z $home_mount ]]; then
    printf 'cannot determine the filesystem containing %s; create or mount it and rerun preflight.' "$home_path"
    return 1
  fi
  if ! quota_error=$(stepanel_user_quota_active "$home_mount" 2>&1); then
    if [[ $quota_error == *'quotaon is required'* || $quota_error == *'xfs_quota is required'* ]]; then
      printf 'quota mount options are present on %s (%s), but enforcement cannot be inspected until the installer installs quota tools: %s. Enforcement will be checked again before setup continues. ' "$home_path" "$home_mount" "$quota_error"
      deferred=1
    else
      printf 'user quota enforcement is not ready on the filesystem containing %s (%s): %s. Enable and verify user quotas on this filesystem, then rerun preflight. See docs/INSTALLATION.md#prepare-filesystem-quotas.' "$home_path" "$home_mount" "$quota_error"
      return 1
    fi
  fi
  if (( deferred )); then
    printf 'at least one quota check is deferred until prerequisite tools are installed; StePanel will not proceed if enforcement is inactive.'
  else
    printf 'user quotas are active on %s and %s' "$web_root" "$home_mount"
  fi
}

# Make an uninspectable quota state visibly distinct from verified enforcement
# wherever the installer reports its host requirements.
install_print_quota_preflight() {
  local result=${1:-}
  if [[ $result == *'Enforcement will be checked again before setup continues'* ]]; then
    printf 'HOST REQUIREMENT (VERIFICATION DEFERRED): %s\n' "$result"
  else
    printf 'HOST REQUIREMENT: %s\n' "$result"
  fi
}
