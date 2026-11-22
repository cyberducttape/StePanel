#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# shellcheck source=../deploy/lib/install-quota-preflight.sh
. "$ROOT_DIR/deploy/lib/install-quota-preflight.sh"
quota_check_line=$(grep -n 'install_preflight_user_quotas /var/www /home' "$ROOT_DIR/install.sh" | cut -d: -f1)
package_mutation_line=$(grep -n 'apt-get update; apt-get install' "$ROOT_DIR/install.sh" | cut -d: -f1)
(( quota_check_line < package_mutation_line ))
web_root=/tmp/stepanel-test-web
test_home_mount=/tmp/stepanel-test-home
quota_failure=
findmnt() { printf '%s\n' "$test_home_mount"; }
stepanel_user_quota_active() {
  if [[ $quota_failure == tools ]]; then
    printf 'quotaon is required to inspect ext4 user quotas' >&2
    return 1
  fi
  if [[ -n $quota_failure && $1 == "$quota_failure" ]]; then
    printf 'user quotas are not enabled on mount %s' "$1" >&2
    return 1
  fi
}

result=$(install_preflight_user_quotas "$web_root" /home)
[[ $result == "user quotas are active on $web_root and $test_home_mount" ]]

quota_failure=$web_root
if message=$(install_preflight_user_quotas "$web_root" /home 2>&1); then
  echo 'missing web-root quota was accepted' >&2
  exit 1
fi
grep -Fq "$web_root" <<<"$message"
grep -Fq 'Enable and verify usrquota' <<<"$message"

quota_failure=$test_home_mount
if message=$(install_preflight_user_quotas "$web_root" /home 2>&1); then
  echo 'missing home quota was accepted' >&2
  exit 1
fi
grep -Fq "$test_home_mount" <<<"$message"
grep -Fq 'Enable and verify usrquota' <<<"$message"

quota_failure=tools
message=$(install_preflight_user_quotas "$web_root" /home)
grep -Fq 'Enforcement will be checked again before setup continues' <<<"$message"

echo 'installer quota preflight contract passed'
