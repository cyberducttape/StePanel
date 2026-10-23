#!/usr/bin/env bash
# Verify enforced project quotas using the native interface for each
# supported filesystem. XFS quota state is enabled at mount time and must be
# queried with xfs_quota; quotaon is used only for ext4.
stepanel_project_quota_active() {
  local path=${1:-} mount options fstype status
  [[ $path == /* ]] || { echo 'project quota path must be absolute' >&2; return 1; }
  command -v findmnt >/dev/null 2>&1 || { echo 'findmnt is required to inspect project quotas' >&2; return 1; }
  mount=$(findmnt -no TARGET -T "$path") || { echo "cannot determine mount for $path" >&2; return 1; }
  options=$(findmnt -no OPTIONS -T "$path") || { echo "cannot inspect mount options for $path" >&2; return 1; }
  fstype=$(findmnt -no FSTYPE -T "$path") || { echo "cannot determine filesystem type for $path" >&2; return 1; }
  [[ $options =~ (^|,)(prjquota|pquota)(,|$) ]] || { echo "project quotas are not enabled in mount options for $mount" >&2; return 1; }
  case "$fstype" in
    ext4)
      command -v quotaon >/dev/null 2>&1 || { echo 'quotaon is required to inspect ext4 project quotas' >&2; return 1; }
      # quotaon -p reports state in its output but exits with the number of
      # active quota types; do not interpret that count as a boolean result.
      status=$(quotaon -p -P "$mount" 2>&1 || true)
      [[ $status == *"project quota on $mount ("*") is on"* ]] || { echo "ext4 project quotas are not active on $mount: $status" >&2; return 1; }
      ;;
    xfs)
      command -v xfs_quota >/dev/null 2>&1 || { echo 'xfs_quota is required to inspect XFS project quotas' >&2; return 1; }
      status=$(xfs_quota -x -c 'state -p' "$mount" 2>&1) || { echo "cannot inspect XFS project quotas on $mount: $status" >&2; return 1; }
      [[ $status == *"Project quota state on $mount ("* && $status == *'Accounting: ON'* && $status == *'Enforcement: ON'* ]] || {
        echo "XFS project quotas are not active and enforced on $mount: $status" >&2
        return 1
      }
      ;;
    *) echo "project quotas are unsupported on filesystem type $fstype" >&2; return 1 ;;
  esac
}

stepanel_user_quota_active() {
  local path=${1:-} mount options fstype status
  [[ $path == /* ]] || { echo 'user quota path must be absolute' >&2; return 1; }
  command -v findmnt >/dev/null 2>&1 || { echo 'findmnt is required to inspect user quotas' >&2; return 1; }
  mount=$(findmnt -no TARGET -T "$path") || { echo "cannot determine mount for $path" >&2; return 1; }
  options=$(findmnt -no OPTIONS -T "$path") || { echo "cannot inspect mount options for $path" >&2; return 1; }
  fstype=$(findmnt -no FSTYPE -T "$path") || { echo "cannot determine filesystem type for $path" >&2; return 1; }
  [[ $options =~ (^|,)(usrquota|uquota)(,|$) ]] || { echo "user quotas are not enabled in mount options for $mount" >&2; return 1; }
  case "$fstype" in
    ext4)
      command -v quotaon >/dev/null 2>&1 || { echo 'quotaon is required to inspect ext4 user quotas' >&2; return 1; }
      status=$(quotaon -p -u "$mount" 2>&1 || true)
      [[ $status == *"user quota on $mount ("*") is on"* ]] || { echo "ext4 user quotas are not active on $mount: $status" >&2; return 1; }
      ;;
    xfs)
      command -v xfs_quota >/dev/null 2>&1 || { echo 'xfs_quota is required to inspect XFS user quotas' >&2; return 1; }
      status=$(xfs_quota -x -c 'state -u' "$mount" 2>&1) || { echo "cannot inspect XFS user quotas on $mount: $status" >&2; return 1; }
      [[ $status == *"User quota state on $mount ("* && $status == *'Accounting: ON'* && $status == *'Enforcement: ON'* ]] || {
        echo "XFS user quotas are not active and enforced on $mount: $status" >&2
        return 1
      }
      ;;
    *) echo "user quotas are unsupported on filesystem type $fstype" >&2; return 1 ;;
  esac
}
