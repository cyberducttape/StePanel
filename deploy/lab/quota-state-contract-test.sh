#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
# shellcheck source=../integrations/stepanel-quota-state.sh
. deploy/integrations/stepanel-quota-state.sh

mock_fstype=ext4
mock_options=usrquota,prjquota
mock_source=/dev/vdb
mock_ext4_status='project quota on /var/www (/dev/vdb) is on'
mock_xfs_status='Project quota state on /var/www (/dev/vdb): Accounting: ON Enforcement: ON'
mock_ext4_user_status='user quota on /var/www (/dev/vdb) is on'
mock_xfs_user_status='User quota state on /var/www (/dev/vdb): Accounting: ON Enforcement: ON'

findmnt() {
  case " $* " in
    *' TARGET '*) printf '%s\n' /var/www ;;
    *' OPTIONS '*) printf '%s\n' "$mock_options" ;;
    *' FSTYPE '*) printf '%s\n' "$mock_fstype" ;;
    *' SOURCE '*) printf '%s\n' "$mock_source" ;;
    *) return 2 ;;
  esac
}
quotaon() {
  case " $* " in
    *' -u '*) printf '%s\n' "$mock_ext4_user_status" ;;
    *) printf '%s\n' "$mock_ext4_status" ;;
  esac
  return 1
}
xfs_quota() {
  case " $* " in
    *' state -u '*) printf '%s\n' "$mock_xfs_user_status" ;;
    *' project -s -p '*) printf 'Setting project on %s\n' "$*" ;;
    *) printf '%s\n' "$mock_xfs_status" ;;
  esac
}

# The Git quota helper's stdout is a machine-readable numeric project ID.
# xfs_quota emits human-readable success output, so ensure set_project_id
# routes that output away from the command-substitution result.
eval "$(sed -n '/^set_project_id() {/,/^}/p' deploy/integrations/stepanel-gitctl)"
project_output=$(set_project_id xfs /var/www /tmp/stage 1000000001)
[[ -z $project_output ]] || { echo 'XFS Git quota setup polluted stdout' >&2; exit 1; }

stepanel_project_quota_active /var/www
stepanel_user_quota_active /var/www
# A systemd-private mount namespace can expose the same device under a
# different mount alias. Quota state must be matched to the filesystem source.
mock_ext4_status='project quota on /var/www/.stepanel-caddy-views/example (/dev/vdb) is on'
mock_ext4_user_status='user quota on /var/www/.stepanel-caddy-views/example (/dev/vdb) is on'
stepanel_project_quota_active /var/www
stepanel_user_quota_active /var/www
mock_fstype=xfs
stepanel_project_quota_active /var/www
stepanel_user_quota_active /var/www

mock_xfs_status='Project quota state on /var/www (/dev/vdb): Accounting: ON Enforcement: OFF'
if stepanel_project_quota_active /var/www; then
  echo 'XFS quota verifier accepted disabled enforcement' >&2
  exit 1
fi

mock_fstype=ext4
mock_ext4_status='user quota on /var/www (/dev/vdb) is on'
if stepanel_project_quota_active /var/www; then
  echo 'ext4 quota verifier accepted missing project-quota state' >&2
  exit 1
fi

mock_fstype=xfs
mock_options=usrquota
if stepanel_project_quota_active /var/www; then
  echo 'quota verifier accepted a mount without project-quota options' >&2
  exit 1
fi

mock_fstype=btrfs
mock_options=usrquota,prjquota
if stepanel_project_quota_active /var/www; then
  echo 'quota verifier accepted an unsupported filesystem' >&2
  exit 1
fi
if stepanel_user_quota_active /var/www; then
  echo 'user-quota verifier accepted an unsupported filesystem' >&2
  exit 1
fi

echo 'quota state filesystem contract tests passed'
