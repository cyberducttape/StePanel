#!/usr/bin/env bash

# Verify mandatory user-quota prerequisites without changing mount or quota
# state. The installer repeats this after package installation to catch a
# concurrent host change before it creates hosted accounts.
install_preflight_user_quotas() {
  local web_root=${1:-/var/www} home_path=${2:-/home} home_mount quota_error
  if ! command -v findmnt >/dev/null 2>&1; then
    printf '%s' 'findmnt is required to inspect hosting filesystem quotas; install util-linux and rerun preflight.'
    return 1
  fi
  if ! quota_error=$(stepanel_user_quota_active "$web_root" 2>&1); then
    printf 'user quota enforcement is not ready on %s: %s. Enable and verify usrquota, then rerun preflight.' "$web_root" "$quota_error"
    return 1
  fi
  if ! home_mount=$(findmnt -no TARGET -T "$home_path" 2>/dev/null) || [[ -z $home_mount ]]; then
    printf 'cannot determine the filesystem containing %s; create or mount it and rerun preflight.' "$home_path"
    return 1
  fi
  if ! quota_error=$(stepanel_user_quota_active "$home_mount" 2>&1); then
    printf 'user quota enforcement is not ready on the filesystem containing %s (%s): %s. Enable and verify usrquota, then rerun preflight.' "$home_path" "$home_mount" "$quota_error"
    return 1
  fi
  printf 'user quotas are active on %s and %s' "$web_root" "$home_mount"
}
